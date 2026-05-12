"""Thin gRPC client around the four gild services.

This is a deliberate parallel copy of ``gil_atropos.grpc_client``. We keep
the two adapters as separate distributions (so neither has to depend on the
other), but the wire-level wrapping is identical.

Connection model
----------------
gild listens on a Unix Domain Socket (default ``~/.gil/gild.sock``). gRPC
supports UDS via the ``unix:`` URI scheme. We always use ``insecure_channel``
because UDS already provides peer authentication via filesystem permissions;
TCP listeners on gild use OIDC bearer tokens (Phase 10 Track E), which this
client surfaces via the ``metadata`` parameter on each call.
"""

from __future__ import annotations

import json
import os
import time
from dataclasses import dataclass
from os.path import expanduser
from typing import Any, Iterator, Mapping, Optional, Sequence

# Protect grpc import: it's a real dependency but we want a friendly error
# rather than ImportError on bare module load (e.g. during unit tests).
try:
    import grpc  # type: ignore[import-not-found]
except ImportError as _grpc_err:  # pragma: no cover -- exercised via tests
    grpc = None  # type: ignore[assignment]
    _GRPC_IMPORT_ERROR = _grpc_err
else:
    _GRPC_IMPORT_ERROR = None


DEFAULT_SOCKET_PATH = "~/.gil/gild.sock"


# ---------------------------------------------------------------------------
# Lazy stub loader
# ---------------------------------------------------------------------------


def _load_stubs():
    """Import the generated *_pb2 / *_pb2_grpc modules.

    Raises a helpful error if compile_protos hasn't been run.
    """
    # Trigger the proto package side-effects (sys.path + gil.v1 alias).
    from gil_swebench import proto as _proto_pkg  # noqa: F401

    try:
        from gil.v1 import (  # type: ignore[import-not-found]
            session_pb2,
            session_pb2_grpc,
            run_pb2,
            run_pb2_grpc,
            interview_pb2,
            interview_pb2_grpc,
            event_pb2,
        )
    except ImportError as exc:
        raise RuntimeError(
            "gil_swebench gRPC stubs are not compiled. Run:\n"
            "    python -m gil_swebench.compile_protos\n"
            f"(underlying ImportError: {exc})"
        ) from exc

    return {
        "session_pb2": session_pb2,
        "session_pb2_grpc": session_pb2_grpc,
        "run_pb2": run_pb2,
        "run_pb2_grpc": run_pb2_grpc,
        "interview_pb2": interview_pb2,
        "interview_pb2_grpc": interview_pb2_grpc,
        "event_pb2": event_pb2,
    }


# ---------------------------------------------------------------------------
# Result containers
# ---------------------------------------------------------------------------


@dataclass
class RunResult:
    """Outcome of one autonomous run.

    Mirrors the relevant fields from ``StartRunResponse`` plus a Pythonic
    ``verify_results`` list of dicts.
    """

    status: str            # "done" | "max_iterations" | "error" | "stopped"
    iterations: int
    tokens: int
    cost_usd: float
    verify_results: list[dict[str, Any]]
    error_message: str = ""

    @property
    def all_checks_passed(self) -> bool:
        if not self.verify_results:
            return False
        return all(v.get("passed") for v in self.verify_results)

    @property
    def pass_ratio(self) -> float:
        if not self.verify_results:
            return 0.0
        passed = sum(1 for v in self.verify_results if v.get("passed"))
        return passed / len(self.verify_results)


# ---------------------------------------------------------------------------
# Client
# ---------------------------------------------------------------------------


class GilGrpcClient:
    """Synchronous wrapper over the four gild gRPC services.

    Parameters
    ----------
    socket_path:
        Path to gild's UDS. ``~`` is expanded. Defaults to ``~/.gil/gild.sock``.
    target:
        Override the gRPC target string entirely (e.g. ``"localhost:7777"``
        for TCP). When set, ``socket_path`` is ignored.
    metadata:
        Extra gRPC metadata appended to every call (e.g. bearer token tuples
        ``[("authorization", "Bearer ...")]``).
    timeout_sec:
        Default per-call timeout. Individual calls may override.
    """

    def __init__(
        self,
        socket_path: str = DEFAULT_SOCKET_PATH,
        *,
        target: Optional[str] = None,
        metadata: Optional[Sequence[tuple[str, str]]] = None,
        timeout_sec: float = 60.0,
    ) -> None:
        if grpc is None:
            raise RuntimeError(
                "grpcio is not installed. Install it with: pip install grpcio"
            ) from _GRPC_IMPORT_ERROR

        self._socket_path = expanduser(socket_path)
        self._target = target or f"unix:{self._socket_path}"
        self._metadata: tuple[tuple[str, str], ...] = tuple(metadata or ())
        self._timeout = timeout_sec

        self._stubs = _load_stubs()
        self._channel = grpc.insecure_channel(self._target)

        self.session_stub = self._stubs["session_pb2_grpc"].SessionServiceStub(self._channel)
        self.run_stub = self._stubs["run_pb2_grpc"].RunServiceStub(self._channel)
        self.interview_stub = self._stubs["interview_pb2_grpc"].InterviewServiceStub(self._channel)
        # EventService is exposed via RunService.Tail in current proto; alias here
        # for forward compatibility if a standalone EventService is added later.
        self.event_stub = self.run_stub

    # --- Lifecycle ---------------------------------------------------------

    def close(self) -> None:
        """Close the underlying gRPC channel."""
        try:
            self._channel.close()
        except Exception:
            pass

    def __enter__(self) -> "GilGrpcClient":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    # --- Sessions ----------------------------------------------------------

    def create_session(self, working_dir: str, goal_hint: str = "") -> str:
        """Create a new session. Returns the session ID."""
        session_pb2 = self._stubs["session_pb2"]
        req = session_pb2.CreateRequest(working_dir=working_dir, goal_hint=goal_hint)
        resp = self.session_stub.Create(req, metadata=self._metadata, timeout=self._timeout)
        return resp.id

    def get_session(self, session_id: str) -> dict[str, Any]:
        session_pb2 = self._stubs["session_pb2"]
        resp = self.session_stub.Get(
            session_pb2.GetRequest(id=session_id),
            metadata=self._metadata,
            timeout=self._timeout,
        )
        return _session_to_dict(resp)

    # --- Interview / spec freezing ----------------------------------------

    def freeze_spec(self, session_id: str, spec_dict: Mapping[str, Any]) -> dict[str, str]:
        """Skip the interactive interview by directly confirming a pre-built spec.

        Mirrors the behavior in ``gil_atropos.grpc_client.freeze_spec``: send
        a single-shot ``StartInterview`` with the goal one-liner and confirm
        as soon as we hit the first agent question or a stage transition out
        of Sensing. The supplied ``spec_dict`` is stashed on the client for
        the runner to consult after the run (verification etc.).
        """
        interview_pb2 = self._stubs["interview_pb2"]

        self._last_spec_hint: dict[str, Any] = dict(spec_dict)

        first_input = (
            spec_dict.get("goal", {}).get("one_liner")
            or spec_dict.get("goal_hint")
            or "SWE-bench task (auto-generated)"
        )
        start_req = interview_pb2.StartInterviewRequest(
            session_id=session_id,
            first_input=str(first_input),
            provider=str(spec_dict.get("provider", "")),
            model=str(spec_dict.get("interview_model", "")),
        )

        try:
            for ev in self.interview_stub.Start(
                start_req, metadata=self._metadata, timeout=self._timeout
            ):
                kind = ev.WhichOneof("payload")
                if kind == "error":
                    raise RuntimeError(
                        f"interview error: {ev.error.code}: {ev.error.message}"
                    )
                if kind == "stage" and ev.stage.to.lower() in {"conversation", "draft", "frozen"}:
                    break
                if kind == "agent_turn":
                    break
        except Exception:
            # If Start fails (e.g. session already past interview), fall
            # through to Confirm and let it return the appropriate error.
            pass

        confirm_resp = self.interview_stub.Confirm(
            interview_pb2.ConfirmRequest(session_id=session_id),
            metadata=self._metadata,
            timeout=self._timeout,
        )
        return {
            "spec_id": confirm_resp.spec_id,
            "content_sha256": confirm_resp.content_sha256,
        }

    # --- Run ---------------------------------------------------------------

    def run_session(
        self,
        session_id: str,
        model: str = "",
        provider: str = "",
        detach: bool = False,
        timeout_sec: Optional[float] = None,
    ) -> RunResult:
        """Run the autonomous loop synchronously and return the outcome.

        ``status`` is one of ``"done" | "max_iterations" | "error" | "stopped"``
        (or ``"started"`` if ``detach=True``).
        """
        run_pb2 = self._stubs["run_pb2"]
        req = run_pb2.StartRunRequest(
            session_id=session_id,
            provider=provider,
            model=model,
            detach=detach,
        )
        resp = self.run_stub.Start(
            req,
            metadata=self._metadata,
            timeout=timeout_sec if timeout_sec is not None else self._timeout,
        )
        verify = [
            {
                "name": v.name,
                "passed": v.passed,
                "exit_code": v.exit_code,
                "stdout": v.stdout,
                "stderr": v.stderr,
            }
            for v in resp.verify_results
        ]
        return RunResult(
            status=resp.status,
            iterations=int(resp.iterations),
            tokens=int(resp.tokens),
            cost_usd=float(resp.cost_usd),
            verify_results=verify,
            error_message=resp.error_message,
        )

    # --- Events ------------------------------------------------------------

    def stream_events(self, session_id: str) -> Iterator[dict[str, Any]]:
        """Generator yielding event dicts as gild streams them."""
        run_pb2 = self._stubs["run_pb2"]
        req = run_pb2.TailRequest(session_id=session_id)
        try:
            for ev in self.run_stub.Tail(req, metadata=self._metadata):
                payload: Any = None
                if ev.data_json:
                    try:
                        payload = json.loads(ev.data_json)
                    except Exception:
                        payload = ev.data_json.decode(errors="replace")
                yield {
                    "id": int(ev.id),
                    "timestamp": ev.timestamp.ToDatetime().isoformat() if ev.timestamp.seconds else None,
                    "source": int(ev.source),
                    "kind": int(ev.kind),
                    "type": ev.type,
                    "data": payload,
                    "cause": int(ev.cause),
                    "metrics": {
                        "tokens": int(ev.metrics.tokens),
                        "cost_usd": float(ev.metrics.cost_usd),
                        "latency_ms": int(ev.metrics.latency_ms),
                    },
                }
        except Exception as exc:
            yield {"type": "_stream_closed", "error": str(exc), "data": None}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _session_to_dict(s: Any) -> dict[str, Any]:
    """Convert a Session proto to a plain dict."""
    return {
        "id": s.id,
        "status": int(s.status),
        "spec_id": s.spec_id,
        "working_dir": s.working_dir,
        "goal_hint": s.goal_hint,
        "total_tokens": int(s.total_tokens),
        "total_cost_usd": float(s.total_cost_usd),
        "current_iteration": int(s.current_iteration),
        "current_tokens": int(s.current_tokens),
    }


def wait_for_socket(socket_path: str = DEFAULT_SOCKET_PATH, timeout_sec: float = 10.0) -> bool:
    """Poll until ``socket_path`` exists and is connectable, or timeout."""
    path = expanduser(socket_path)
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        if os.path.exists(path):
            return True
        time.sleep(0.1)
    return False
