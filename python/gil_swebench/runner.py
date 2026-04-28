"""SWEBenchRunner -- run one task end-to-end against a running gild.

Pipeline for a single instance::

    1. setup_workspace  -- clone task.repo, checkout task.base_commit
    2. create_session   -- gild.SessionService.Create
    3. freeze_spec      -- bypass interactive interview, freeze the goal
    4. run_session      -- gild.RunService.Start (blocking)
    5. capture_patch    -- ``git diff <base_commit>`` against the workspace
    6. record           -- append SWEBenchResult to results.jsonl

Scoring (FAIL_TO_PASS / PASS_TO_PASS check) is intentionally split out into
:mod:`gil_swebench.score` so the runner can finish quickly even when the
official tests are slow, and so users can re-score an existing results dir
without rerunning the agent.
"""

from __future__ import annotations

import json
import logging
import os
import shutil
import subprocess
import time
import uuid
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any, Mapping, Optional

from gil_swebench.grpc_client import GilGrpcClient, RunResult
from gil_swebench.task import SWEBenchTask

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Result dataclass
# ---------------------------------------------------------------------------


@dataclass
class SWEBenchResult:
    """Outcome of one SWE-bench rollout.

    The runner populates everything except the ``resolved`` / ``resolved_reason``
    fields; those come from :func:`gil_swebench.score.score_one` after the
    official tests are re-run.
    """

    instance_id: str
    repo: str
    base_commit: str

    # gil run outcome
    status: str = ""              # "done" | "max_iterations" | "error" | "stopped" | "skipped"
    iterations: int = 0
    tokens: int = 0
    cost_usd: float = 0.0
    wall_clock_seconds: float = 0.0
    error_message: str = ""

    # The agent's diff against base_commit (text, not bytes)
    model_patch: str = ""

    # Verifier outcome from gil's loop (single FAIL_TO_PASS check)
    fail_to_pass_verifier_passed: bool = False

    # Aggregate score (filled in by score module). Defaults to ``None``
    # so we can distinguish "not yet scored" from "scored and failed".
    resolved: Optional[bool] = None
    resolved_reason: str = ""

    # Provider/model used (for debugging + cost rollup)
    provider: str = ""
    model: str = ""

    # Workspace path (kept iff --keep-workspaces; otherwise cleaned up)
    workspace_path: str = ""

    extra: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------


class SWEBenchRunner:
    """Drive one or more SWE-bench tasks against a gild.

    Parameters
    ----------
    client:
        A connected :class:`GilGrpcClient`. The runner does *not* own the
        client lifecycle -- caller is responsible for ``close()``.
    workspace_root:
        Base directory under which per-instance clones live. A fresh
        ``mkdtemp()`` is used if not provided.
    keep_workspaces:
        If True, leave per-instance dirs in place after the run finishes
        (useful for inspecting agent output). Default False.
    repo_url_template:
        Template for the upstream repo URL given the ``repo`` field. The
        SWE-bench convention is ``"https://github.com/{repo}.git"``.
    git_clone_depth:
        ``--depth`` for the initial clone. We always go full-depth (``0``
        means no shallow flag) because the agent often needs ``git log``
        / ``git blame``. Override via runner arg if you really need it.
    skip_clone:
        If True, assume ``workspace_path`` is already populated (useful in
        tests). Skips both clone and checkout.
    """

    def __init__(
        self,
        client: GilGrpcClient,
        *,
        workspace_root: Optional[str] = None,
        keep_workspaces: bool = False,
        repo_url_template: str = "https://github.com/{repo}.git",
        git_clone_depth: int = 0,
        skip_clone: bool = False,
        provider: str = "",
        model: str = "",
        run_timeout_sec: float = 60 * 60,
        max_iter: int = 30,
        max_tokens: int = 500_000,
        max_cost_usd: float = 5.0,
        autonomy: str = "ASK_DESTRUCTIVE_ONLY",
    ) -> None:
        self._client = client
        self._workspace_root = workspace_root or os.path.join(
            os.path.expanduser("~"), ".gil", "swebench-workspaces"
        )
        os.makedirs(self._workspace_root, exist_ok=True)
        self._keep_workspaces = keep_workspaces
        self._repo_url_template = repo_url_template
        self._git_clone_depth = git_clone_depth
        self._skip_clone = skip_clone
        self._provider = provider
        self._model = model
        self._run_timeout_sec = run_timeout_sec
        self._max_iter = max_iter
        self._max_tokens = max_tokens
        self._max_cost_usd = max_cost_usd
        self._autonomy = autonomy

    # ------------------------------------------------------------------
    # Public entry point
    # ------------------------------------------------------------------

    def run_one(
        self,
        task: SWEBenchTask,
        *,
        workspace_path: Optional[str] = None,
    ) -> SWEBenchResult:
        """Run one instance end-to-end and return a :class:`SWEBenchResult`.

        Never raises -- on error, the result is returned with ``status="error"``
        and the exception text in ``error_message``. This keeps batch runs
        from blowing up on a single bad instance.
        """
        result = SWEBenchResult(
            instance_id=task.instance_id,
            repo=task.repo,
            base_commit=task.base_commit,
            provider=self._provider,
            model=self._model,
        )

        t0 = time.time()
        try:
            wd = workspace_path or self._allocate_workspace(task)
            result.workspace_path = wd
            if not self._skip_clone:
                self._setup_workspace(task, wd)

            spec_dict = task.to_spec_dict(
                working_dir=wd,
                max_iter=self._max_iter,
                max_tokens=self._max_tokens,
                max_cost_usd=self._max_cost_usd,
                autonomy=self._autonomy,
            )
            session_id = self._client.create_session(
                working_dir=wd,
                goal_hint=spec_dict["goal"]["one_liner"],
            )
            self._client.freeze_spec(session_id, spec_dict)
            run_result: RunResult = self._client.run_session(
                session_id,
                model=self._model,
                provider=self._provider,
                detach=False,
                timeout_sec=self._run_timeout_sec,
            )

            result.status = run_result.status
            result.iterations = run_result.iterations
            result.tokens = run_result.tokens
            result.cost_usd = run_result.cost_usd
            result.error_message = run_result.error_message or ""
            result.fail_to_pass_verifier_passed = run_result.all_checks_passed

            # Capture the agent's diff against base_commit. If the workspace
            # is no longer a git repo (shouldn't happen) or the base_commit
            # is missing locally we fall back to "" + a note in extra.
            result.model_patch = _git_diff_against(wd, task.base_commit) or ""
        except Exception as exc:
            logger.exception("rollout failed for %s", task.instance_id)
            result.status = result.status or "error"
            result.error_message = result.error_message or str(exc)
        finally:
            result.wall_clock_seconds = time.time() - t0
            if not self._keep_workspaces and result.workspace_path:
                shutil.rmtree(result.workspace_path, ignore_errors=True)

        return result

    # ------------------------------------------------------------------
    # Workspace management
    # ------------------------------------------------------------------

    def _allocate_workspace(self, task: SWEBenchTask) -> str:
        sub = f"{_safe_slug(task.instance_id)}-{uuid.uuid4().hex[:8]}"
        path = os.path.join(self._workspace_root, sub)
        os.makedirs(path, exist_ok=True)
        return path

    def _setup_workspace(self, task: SWEBenchTask, path: str) -> None:
        """Clone ``task.repo`` at ``task.base_commit`` into ``path``.

        Real benchmarking will need a working network and ~hundreds of MB
        per repo. This is the slow/IO-heavy part; pre-warming a local mirror
        and pointing ``repo_url_template`` at it is a reasonable optimization
        for repeated runs.
        """
        url = self._repo_url_template.format(repo=task.repo)
        # Empty-out path if it already exists (to keep the clone clean).
        if any(os.scandir(path)):
            shutil.rmtree(path, ignore_errors=True)
            os.makedirs(path, exist_ok=True)

        clone_cmd = ["git", "clone"]
        if self._git_clone_depth > 0:
            clone_cmd.extend(["--depth", str(self._git_clone_depth)])
        clone_cmd.extend([url, path])
        _run(clone_cmd, cwd=None, label="git clone")

        # Detach to base_commit. Do a fetch first in case the SHA is older
        # than the default branch tip (depth=0 already has full history).
        _run(["git", "checkout", task.base_commit], cwd=path, label="git checkout")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _safe_slug(s: str) -> str:
    """Make a string safe for use as a filesystem directory name."""
    out = []
    for ch in s:
        if ch.isalnum() or ch in ("-", "_", "."):
            out.append(ch)
        else:
            out.append("_")
    return "".join(out) or "task"


def _run(cmd: list[str], *, cwd: Optional[str], label: str) -> subprocess.CompletedProcess:
    """Run a subprocess, raising a clean error on non-zero exit."""
    logger.debug("%s: %s", label, " ".join(cmd))
    proc = subprocess.run(
        cmd,
        cwd=cwd,
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"{label} failed (exit {proc.returncode}): {proc.stderr.strip()}"
        )
    return proc


def _git_diff_against(repo_dir: str, base_commit: str) -> str:
    """Return ``git diff <base_commit>`` of the working tree as a string.

    Includes both staged and unstaged changes. Returns empty string if the
    diff fails (e.g. workspace was wiped) -- callers should treat empty as
    "agent did not produce a patch", which scores as resolved=False.
    """
    if not base_commit:
        return ""
    try:
        proc = subprocess.run(
            ["git", "diff", "--no-color", base_commit],
            cwd=repo_dir,
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
        if proc.returncode != 0:
            logger.warning(
                "git diff against %s failed: %s",
                base_commit,
                proc.stderr.strip(),
            )
            return ""
        return proc.stdout
    except Exception as exc:
        logger.warning("git diff exception: %s", exc)
        return ""


def write_jsonl(results: list[SWEBenchResult], path: str | Path) -> None:
    """Append ``results`` as JSONL to ``path``."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    with p.open("a", encoding="utf-8") as fh:
        for r in results:
            fh.write(json.dumps(r.to_dict(), ensure_ascii=False) + "\n")
