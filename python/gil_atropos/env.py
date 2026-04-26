"""Atropos RL environment that uses gil as the coding-agent backend.

This module defines :class:`GilCodingEnv`. It has two modes:

* **Full Atropos mode** (hermes-agent installed):
  ``GilCodingEnv`` subclasses :class:`hermes_agent.environments.HermesAgentBaseEnv`
  and plugs into the Atropos training pipeline (``setup`` / ``get_next_item``
  / ``format_prompt`` / ``compute_reward`` / ``evaluate`` / ``wandb_log``).

* **Standalone eval mode** (no hermes-agent):
  ``GilCodingEnv`` is just a plain class. ``evaluate(task)`` runs one full
  rollout via :class:`GilGrpcClient` and returns a :class:`ScoredRollout`.
  Useful for ad-hoc benchmarking without setting up the full Atropos stack.

Each rollout is one full gil session::

    create_session(working_dir)        # fresh tmpdir per task
        -> freeze_spec(spec_dict)      # bypass interactive interview
        -> run_session(model, ...)     # autonomous loop in gil
        -> compute_reward(verifier)    # 1.0 if all checks passed; partial credit otherwise
"""

from __future__ import annotations

import asyncio
import logging
import os
import random
import shutil
import tempfile
import time
import uuid
from dataclasses import asdict, dataclass, field
from typing import Any, Iterable, Mapping, Optional

from .grpc_client import DEFAULT_SOCKET_PATH, GilGrpcClient, RunResult
from .samples import BUNDLED_TASKS, CodingTask

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Optional hermes-agent / Atropos base import
# ---------------------------------------------------------------------------
#
# The Atropos training stack (atroposlib + hermes-agent) is heavy and not on
# PyPI. We import it lazily so that ``import gil_atropos`` works in eval-only
# environments. When unavailable, GilCodingEnv simply doesn't inherit from
# the base env -- everything else still works.

_HERMES_BASE: Optional[type] = None
_HERMES_IMPORT_ERROR: Optional[Exception] = None
try:  # pragma: no cover -- environment-dependent
    from hermes_agent.environments.hermes_base_env import (  # type: ignore[import-not-found]
        HermesAgentBaseEnv as _HermesBase,
    )
    _HERMES_BASE = _HermesBase
except ImportError as _err:
    try:  # alternate import path used by some hermes-agent layouts
        from environments.hermes_base_env import (  # type: ignore[import-not-found]
            HermesAgentBaseEnv as _HermesBase,
        )
        _HERMES_BASE = _HermesBase
    except ImportError as _err2:
        _HERMES_IMPORT_ERROR = _err2

HERMES_AVAILABLE = _HERMES_BASE is not None


def _atropos_base():
    """Return a base class for GilCodingEnv.

    Returns the real HermesAgentBaseEnv if available; otherwise a plain
    ``object`` so the class is still importable + instantiable (eval-only).
    """
    return _HERMES_BASE if HERMES_AVAILABLE else object


# ---------------------------------------------------------------------------
# Result dataclass
# ---------------------------------------------------------------------------


@dataclass
class ScoredRollout:
    """Outcome of evaluating one task."""

    task_id: str
    prompt: str
    reward: float
    status: str
    iterations: int
    tokens: int
    cost_usd: float
    pass_ratio: float
    verify_results: list[dict[str, Any]] = field(default_factory=list)
    working_dir: str = ""
    wall_clock_seconds: float = 0.0
    error_message: str = ""

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


# ---------------------------------------------------------------------------
# Environment
# ---------------------------------------------------------------------------


class GilCodingEnv(_atropos_base()):  # type: ignore[misc]
    """gil-as-Atropos-environment.

    Parameters
    ----------
    socket_path:
        gild UDS path. Default ``~/.gil/gild.sock``.
    model:
        Model to pass to ``RunService.Start``. Empty string -> server default.
    provider:
        Provider hint (``"anthropic"``, ``"mock"``, ...). Empty -> server default.
    workspace_root:
        Base directory under which per-rollout working_dirs are created.
        Default: a fresh ``tempfile.mkdtemp()``.
    keep_workspaces:
        If True, don't delete the per-rollout working_dir after the run
        completes (useful for debugging).
    dataset_name:
        Optional HF dataset name (e.g. ``"openai_humaneval"`` or ``"mbpp"``).
        Falls back to bundled tasks if loading fails.
    eval_size:
        Number of items reserved for eval (the rest go into the train pool).
    seed:
        RNG seed for the train/eval split.
    """

    name: Optional[str] = "gil-coding"

    def __init__(
        self,
        *args: Any,
        socket_path: str = DEFAULT_SOCKET_PATH,
        model: str = "",
        provider: str = "",
        workspace_root: Optional[str] = None,
        keep_workspaces: bool = False,
        dataset_name: Optional[str] = None,
        eval_size: int = 5,
        seed: int = 0,
        client: Optional[GilGrpcClient] = None,
        run_timeout_sec: float = 600.0,
        **kwargs: Any,
    ) -> None:
        # Pass through to HermesAgentBaseEnv if present; in standalone mode the
        # base is `object`, which takes no args.
        if HERMES_AVAILABLE and (args or kwargs):
            super().__init__(*args, **kwargs)
        elif HERMES_AVAILABLE:
            # The base needs (config, server_configs, ...) -- if the caller
            # didn't supply them they probably want a config-less standalone
            # eval. Skip super().__init__() in that case.
            pass

        self._socket_path = socket_path
        self._model = model
        self._provider = provider
        self._workspace_root = workspace_root or tempfile.mkdtemp(prefix="gil-atropos-")
        os.makedirs(self._workspace_root, exist_ok=True)
        self._keep_workspaces = keep_workspaces
        self._dataset_name = dataset_name
        self._eval_size = max(1, int(eval_size))
        self._seed = seed
        self._run_timeout_sec = run_timeout_sec

        # gRPC client is lazy: tests inject a mock; real callers can let
        # `setup()` (or the first `evaluate`) create one on demand.
        self._client: Optional[GilGrpcClient] = client
        self._owns_client = client is None

        # Populated by setup()
        self._items: list[CodingTask] = []
        self._eval_items: list[CodingTask] = []
        self._index: int = 0

        # Reward buffer for wandb_log()
        self._reward_buffer: list[float] = []

    # ------------------------------------------------------------------
    # Public client helper
    # ------------------------------------------------------------------

    def client(self) -> GilGrpcClient:
        """Return (lazily creating) the gRPC client."""
        if self._client is None:
            self._client = GilGrpcClient(self._socket_path)
            self._owns_client = True
        return self._client

    def close(self) -> None:
        """Close gRPC channel + (optionally) clean up the workspace root."""
        if self._client is not None and self._owns_client:
            self._client.close()
            self._client = None
        if not self._keep_workspaces:
            shutil.rmtree(self._workspace_root, ignore_errors=True)

    # ------------------------------------------------------------------
    # Atropos lifecycle methods
    # ------------------------------------------------------------------

    async def setup(self) -> None:
        """Load the dataset and split into train / eval pools."""
        items = await asyncio.to_thread(self._load_items)
        rng = random.Random(self._seed)
        rng.shuffle(items)

        # Reserve at most eval_size items for eval; ensure at least one
        # train item if the dataset is small.
        n = len(items)
        eval_n = min(self._eval_size, max(1, n // 2)) if n > 1 else 0
        self._eval_items = items[:eval_n]
        self._items = items[eval_n:] or items  # fall back to using all items for train
        self._index = 0
        logger.info(
            "GilCodingEnv setup: %d train items, %d eval items (source=%s)",
            len(self._items),
            len(self._eval_items),
            self._dataset_name or "bundled",
        )

    def _load_items(self) -> list[CodingTask]:
        """Try to load a HuggingFace dataset; fall back to bundled tasks."""
        if self._dataset_name:
            try:
                from datasets import load_dataset  # type: ignore[import-not-found]

                ds = load_dataset(self._dataset_name, split="test")
                converted = [_hf_record_to_task(self._dataset_name, i, rec) for i, rec in enumerate(ds)]
                if converted:
                    return converted
            except Exception as exc:
                logger.warning(
                    "Failed to load HF dataset %r: %s. Falling back to bundled tasks.",
                    self._dataset_name,
                    exc,
                )
        return list(BUNDLED_TASKS)

    async def get_next_item(self) -> CodingTask:
        if not self._items:
            await self.setup()
        item = self._items[self._index % len(self._items)]
        self._index += 1
        return item

    def format_prompt(self, item: CodingTask) -> dict[str, Any]:
        """Return the spec_dict for this task (interview-skipping path)."""
        # Working dir is allocated lazily in evaluate() -- format_prompt should
        # be idempotent and not have side effects.
        return item.to_spec_dict()

    async def compute_reward(
        self,
        item: CodingTask,
        rollout_result: RunResult,
        ctx: Any = None,  # ToolContext when running inside hermes-agent; ignored otherwise
    ) -> float:
        """Score a rollout from its verifier outcome.

        Reward shape:
          * all checks passed -> 1.0
          * none passed       -> 0.0
          * mixed             -> ratio passed / total
          * run errored before any check -> 0.0
        """
        if rollout_result.status == "error":
            return 0.0
        if not rollout_result.verify_results:
            return 0.0
        return rollout_result.pass_ratio

    async def evaluate(self, task: Optional[CodingTask] = None, *args: Any, **kwargs: Any) -> ScoredRollout:
        """Run one full rollout and return the scored result.

        ``task`` may be omitted, in which case the next item from the eval
        pool is used. This dual signature lets the same method serve as both
        "Atropos periodic eval" and "ad-hoc CLI eval".
        """
        if task is None:
            if not self._eval_items:
                await self.setup()
            task = self._eval_items[self._index % max(1, len(self._eval_items))]
            self._index += 1

        return await asyncio.to_thread(self._run_one, task)

    # ------------------------------------------------------------------
    # The actual rollout (synchronous; called from evaluate via to_thread)
    # ------------------------------------------------------------------

    def _run_one(self, task: CodingTask) -> ScoredRollout:
        client = self.client()
        working_dir = self._allocate_workspace(task)
        spec_dict = task.to_spec_dict(working_dir=working_dir)

        t0 = time.time()
        result = RunResult(status="error", iterations=0, tokens=0, cost_usd=0.0, verify_results=[], error_message="")
        try:
            session_id = client.create_session(working_dir=working_dir, goal_hint=task.prompt)
            client.freeze_spec(session_id, spec_dict)
            result = client.run_session(
                session_id,
                model=self._model,
                provider=self._provider,
                detach=False,
                timeout_sec=self._run_timeout_sec,
            )
        except Exception as exc:
            logger.exception("rollout failed for task %s", task.task_id)
            result = RunResult(
                status="error",
                iterations=0,
                tokens=0,
                cost_usd=0.0,
                verify_results=[],
                error_message=str(exc),
            )

        elapsed = time.time() - t0
        # Score synchronously: compute_reward is async but only does arithmetic.
        try:
            reward = asyncio.get_event_loop().run_until_complete(self.compute_reward(task, result))
        except RuntimeError:
            # No running loop -- create one for this synchronous call.
            reward = asyncio.new_event_loop().run_until_complete(self.compute_reward(task, result))
        self._reward_buffer.append(reward)

        scored = ScoredRollout(
            task_id=task.task_id,
            prompt=task.prompt,
            reward=reward,
            status=result.status,
            iterations=result.iterations,
            tokens=result.tokens,
            cost_usd=result.cost_usd,
            pass_ratio=result.pass_ratio,
            verify_results=list(result.verify_results),
            working_dir=working_dir,
            wall_clock_seconds=elapsed,
            error_message=result.error_message,
        )

        if not self._keep_workspaces:
            shutil.rmtree(working_dir, ignore_errors=True)

        return scored

    def _allocate_workspace(self, task: CodingTask) -> str:
        """Allocate a fresh working_dir under workspace_root for this rollout."""
        sub = f"{task.task_id}-{uuid.uuid4().hex[:8]}"
        path = os.path.join(self._workspace_root, sub)
        os.makedirs(path, exist_ok=True)
        return path

    # ------------------------------------------------------------------
    # wandb_log -- only meaningful in full-Atropos mode
    # ------------------------------------------------------------------

    async def wandb_log(self, wandb_metrics: Optional[dict[str, Any]] = None) -> None:
        if wandb_metrics is None:
            wandb_metrics = {}
        if self._reward_buffer:
            n = len(self._reward_buffer)
            wandb_metrics["train/mean_reward"] = sum(self._reward_buffer) / n
            wandb_metrics["train/rollouts"] = n
            self._reward_buffer.clear()
        if HERMES_AVAILABLE:
            await super().wandb_log(wandb_metrics)  # type: ignore[misc]


# ---------------------------------------------------------------------------
# HF dataset record -> CodingTask
# ---------------------------------------------------------------------------


def _hf_record_to_task(dataset_name: str, idx: int, record: Mapping[str, Any]) -> CodingTask:
    """Best-effort conversion of a HF record to our CodingTask schema.

    Supports the two datasets called out in the spec: ``humaneval`` and
    ``mbpp``. Unknown schemas fall back to a generic mapping.
    """
    name_lower = (dataset_name or "").lower()

    if "humaneval" in name_lower:
        prompt = record.get("prompt", "")
        test = record.get("test", "")
        entry_point = record.get("entry_point", "candidate")
        canonical = record.get("canonical_solution", "")
        verifier = (
            "set -e; cat > _humaneval_test.py <<'PY'\n"
            f"{prompt}{canonical if False else ''}\n"  # canonical excluded -- agent must produce it
            f"{test}\n"
            f"check({entry_point})\n"
            "PY\n"
            "python _humaneval_test.py"
        )
        return CodingTask(
            task_id=f"humaneval_{idx}_{record.get('task_id', '').replace('/', '_')}",
            prompt=f"HumanEval task: {prompt.splitlines()[0] if prompt else ''}",
            detailed=prompt,
            target_file="solution.py",
            verifier_cmd=verifier,
            success_criteria=["All HumanEval tests pass."],
        )

    if "mbpp" in name_lower:
        text = record.get("text") or record.get("prompt", "")
        tests: Iterable[str] = record.get("test_list") or []
        verifier_lines = ["set -e", "cat > _mbpp_test.py <<'PY'"]
        verifier_lines.append("from solution import *")
        verifier_lines.extend(list(tests))
        verifier_lines.append("PY")
        verifier_lines.append("python _mbpp_test.py")
        return CodingTask(
            task_id=f"mbpp_{idx}_{record.get('task_id', '')}",
            prompt=text.splitlines()[0] if text else "MBPP task",
            detailed=text,
            target_file="solution.py",
            verifier_cmd="\n".join(verifier_lines),
            success_criteria=["All MBPP assertions pass."],
        )

    # Generic fallback -- best we can do without knowing the schema.
    return CodingTask(
        task_id=f"{dataset_name}_{idx}",
        prompt=str(record.get("prompt") or record.get("text") or "Coding task"),
        detailed=str(record.get("prompt") or record.get("text") or ""),
        target_file="solution.py",
        verifier_cmd="echo 'no verifier available' && false",
    )
