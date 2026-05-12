"""Score a SWE-bench rollout: ``resolved`` iff FAIL_TO_PASS now pass AND
PASS_TO_PASS still pass.

This module is intentionally side-effect-light when given a JSONL of results
the runner has already produced. Two entry points:

* :func:`score_one` -- given a :class:`SWEBenchResult` + the original
  :class:`SWEBenchTask`, materialize a fresh clone, apply the agent's patch,
  apply the official ``test_patch``, then run the two test sets via pytest.
  Returns ``(resolved: bool, reason: str)``.

* :func:`score_results` -- iterate over a results JSONL and an instance map,
  call ``score_one`` per row, write back an updated JSONL with ``resolved``
  populated, and emit ``summary.json`` + ``results.csv``.

We never trust the runner's in-process verifier alone: gil's loop runs only
the FAIL_TO_PASS check (PASS_TO_PASS is too slow to re-run every iteration).
For the headline pass@1 we have to do both, and we have to do it on a clean
clone so the agent's other side-effects don't taint the test environment.
"""

from __future__ import annotations

import csv
import json
import logging
import os
import shutil
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable, Mapping, Optional

from gil_swebench.runner import SWEBenchResult
from gil_swebench.task import SWEBenchTask, find_task, load_fixture_tasks

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


@dataclass
class ScoreSummary:
    """Aggregate across a set of scored results."""

    n_total: int = 0
    n_resolved: int = 0
    n_unresolved: int = 0
    n_errored: int = 0
    n_skipped: int = 0
    pass_at_1: float = 0.0
    total_tokens: int = 0
    total_cost_usd: float = 0.0
    total_wall_clock_seconds: float = 0.0
    by_instance: list[dict[str, Any]] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "n_total": self.n_total,
            "n_resolved": self.n_resolved,
            "n_unresolved": self.n_unresolved,
            "n_errored": self.n_errored,
            "n_skipped": self.n_skipped,
            "pass_at_1": self.pass_at_1,
            "total_tokens": self.total_tokens,
            "total_cost_usd": self.total_cost_usd,
            "total_wall_clock_seconds": self.total_wall_clock_seconds,
            "by_instance": self.by_instance,
        }


def _run(cmd: list[str], *, cwd: Optional[str], timeout: float = 1800.0) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd,
        cwd=cwd,
        check=False,
        capture_output=True,
        text=True,
        timeout=timeout,
    )


# ---------------------------------------------------------------------------
# Per-instance scoring
# ---------------------------------------------------------------------------


def score_one(
    result: SWEBenchResult,
    task: SWEBenchTask,
    *,
    work_dir: Optional[str] = None,
    repo_url_template: str = "https://github.com/{repo}.git",
    pytest_extra: Optional[list[str]] = None,
    pytest_timeout: float = 1800.0,
    skip_clone: bool = False,
) -> tuple[bool, str]:
    """Apply ``result.model_patch`` + the official test_patch on a clean
    clone and re-run FAIL_TO_PASS + PASS_TO_PASS.

    Returns ``(resolved, reason)``. ``resolved`` is True iff:
        * every FAIL_TO_PASS test now passes, AND
        * every PASS_TO_PASS test still passes.

    On any infrastructure error (clone failed, base_commit missing, patch
    didn't apply, ...) returns ``(False, "<reason>")`` rather than raising,
    so batch scoring keeps making progress.
    """
    pytest_extra = pytest_extra or ["-q", "--tb=short"]

    # If the runner already produced no patch, short-circuit.
    if not result.model_patch.strip():
        return False, "agent produced empty patch"

    # If the runner errored out before producing a patch, also short-circuit.
    if result.status == "error" and not result.model_patch.strip():
        return False, f"run errored: {result.error_message[:200]}"

    cleanup = False
    if work_dir is None:
        work_dir = tempfile.mkdtemp(prefix="gil-swebench-score-")
        cleanup = True

    try:
        if not skip_clone:
            url = repo_url_template.format(repo=task.repo)
            # Wipe + clone fresh.
            shutil.rmtree(work_dir, ignore_errors=True)
            os.makedirs(work_dir, exist_ok=True)
            clone = _run(["git", "clone", url, work_dir], cwd=None)
            if clone.returncode != 0:
                return False, f"git clone failed: {clone.stderr.strip()[:200]}"
            checkout = _run(["git", "checkout", task.base_commit], cwd=work_dir)
            if checkout.returncode != 0:
                return False, f"git checkout {task.base_commit} failed: {checkout.stderr.strip()[:200]}"

        # Apply the agent's patch.
        patch_apply = _apply_patch(result.model_patch, work_dir, label="model_patch")
        if not patch_apply[0]:
            return False, patch_apply[1]

        # Apply the official test_patch (so the new tests are present).
        if task.test_patch.strip():
            tp_apply = _apply_patch(task.test_patch, work_dir, label="test_patch")
            if not tp_apply[0]:
                return False, tp_apply[1]

        # Run FAIL_TO_PASS first; bail early if any fail.
        if task.fail_to_pass:
            f2p = _run_pytest(work_dir, task.fail_to_pass, pytest_extra, pytest_timeout)
            if f2p.returncode != 0:
                return False, f"FAIL_TO_PASS still failing (rc={f2p.returncode})"

        # Then PASS_TO_PASS.
        if task.pass_to_pass:
            p2p = _run_pytest(work_dir, task.pass_to_pass, pytest_extra, pytest_timeout)
            if p2p.returncode != 0:
                return False, f"PASS_TO_PASS regressed (rc={p2p.returncode})"

        return True, "ok"
    finally:
        if cleanup:
            shutil.rmtree(work_dir, ignore_errors=True)


def _apply_patch(patch: str, repo_dir: str, *, label: str) -> tuple[bool, str]:
    """Try ``git apply``, then fall back to ``patch -p1``."""
    # Write to a temp file so we don't have to worry about shell-escaping.
    fd, patch_path = tempfile.mkstemp(prefix=f"{label}-", suffix=".diff")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(patch)
            if not patch.endswith("\n"):
                fh.write("\n")

        # Try git apply first (preferred; respects index)
        ga = _run(["git", "apply", "--whitespace=nowarn", patch_path], cwd=repo_dir)
        if ga.returncode == 0:
            return True, "ok"

        # Fall back to plain patch -p1.
        p = _run(["patch", "-p1", "-i", patch_path], cwd=repo_dir)
        if p.returncode == 0:
            return True, "ok"

        return False, (
            f"{label} did not apply (git apply rc={ga.returncode}, "
            f"patch rc={p.returncode}): {ga.stderr.strip()[:160]}"
        )
    finally:
        try:
            os.unlink(patch_path)
        except OSError:
            pass


def _run_pytest(
    repo_dir: str,
    node_ids: Iterable[str],
    extra: list[str],
    timeout: float,
) -> subprocess.CompletedProcess:
    cmd = ["python", "-m", "pytest", *extra, *list(node_ids)]
    return _run(cmd, cwd=repo_dir, timeout=timeout)


# ---------------------------------------------------------------------------
# Aggregate scoring (over a results dir)
# ---------------------------------------------------------------------------


def score_results(
    results_path: str | Path,
    *,
    tasks: Optional[Iterable[SWEBenchTask]] = None,
    output_dir: Optional[str | Path] = None,
    rescore: bool = False,
    skip_clone: bool = False,
) -> ScoreSummary:
    """Score every result in ``results_path`` (a JSONL) and write a summary.

    Parameters
    ----------
    results_path:
        Path to ``instances.jsonl`` produced by the runner.
    tasks:
        Iterable of :class:`SWEBenchTask` covering every ``instance_id`` in
        the results. Defaults to the bundled fixtures (only useful for
        smoke runs).
    output_dir:
        Where to write ``summary.json`` and ``results.csv``. Defaults to
        the directory containing ``results_path``.
    rescore:
        If True, re-score even rows that already have ``resolved`` set.
        Default False -- skip rows that have already been scored.
    skip_clone:
        Pass-through to :func:`score_one`. Useful when the workspace dir
        is already populated.
    """
    results_path = Path(results_path)
    if not results_path.is_file():
        raise FileNotFoundError(f"results file not found: {results_path}")

    output_dir = Path(output_dir) if output_dir else results_path.parent
    output_dir.mkdir(parents=True, exist_ok=True)

    task_pool = list(tasks) if tasks is not None else load_fixture_tasks()
    task_by_id = {t.instance_id: t for t in task_pool}

    summary = ScoreSummary()
    rescored_rows: list[dict[str, Any]] = []

    with results_path.open("r", encoding="utf-8") as fh:
        for line_num, line in enumerate(fh, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            summary.n_total += 1
            summary.total_tokens += int(row.get("tokens", 0) or 0)
            summary.total_cost_usd += float(row.get("cost_usd", 0.0) or 0.0)
            summary.total_wall_clock_seconds += float(row.get("wall_clock_seconds", 0.0) or 0.0)

            inst = row.get("instance_id", "")
            task = task_by_id.get(inst)
            if task is None:
                row["resolved"] = False
                row["resolved_reason"] = "task not found in supplied dataset"
                summary.n_skipped += 1
            elif row.get("status") == "error":
                row["resolved"] = False
                row["resolved_reason"] = (
                    row.get("resolved_reason") or f"run errored: {row.get('error_message','')[:160]}"
                )
                summary.n_errored += 1
            elif row.get("resolved") is not None and not rescore:
                # Already scored; trust the existing value.
                pass
            else:
                # Rebuild the SWEBenchResult enough to call score_one.
                fake = SWEBenchResult(
                    instance_id=inst,
                    repo=row.get("repo", task.repo),
                    base_commit=row.get("base_commit", task.base_commit),
                    status=row.get("status", ""),
                    model_patch=row.get("model_patch", ""),
                    error_message=row.get("error_message", ""),
                )
                resolved, reason = score_one(fake, task, skip_clone=skip_clone)
                row["resolved"] = resolved
                row["resolved_reason"] = reason

            if row.get("resolved") is True:
                summary.n_resolved += 1
            elif row.get("resolved") is False and row.get("status") != "error":
                summary.n_unresolved += 1

            summary.by_instance.append({
                "instance_id": inst,
                "resolved": row.get("resolved"),
                "reason": row.get("resolved_reason", ""),
                "status": row.get("status", ""),
                "tokens": row.get("tokens", 0),
                "cost_usd": row.get("cost_usd", 0.0),
                "wall_clock_seconds": row.get("wall_clock_seconds", 0.0),
            })
            rescored_rows.append(row)

    summary.pass_at_1 = (
        summary.n_resolved / summary.n_total if summary.n_total else 0.0
    )

    # Write summary.json
    (output_dir / "summary.json").write_text(
        json.dumps(summary.to_dict(), indent=2, ensure_ascii=False),
        encoding="utf-8",
    )

    # Write results.csv (one row per instance, useful for spreadsheets)
    csv_path = output_dir / "results.csv"
    with csv_path.open("w", encoding="utf-8", newline="") as fh:
        writer = csv.writer(fh)
        writer.writerow([
            "instance_id", "resolved", "reason", "status",
            "tokens", "cost_usd", "wall_clock_seconds",
        ])
        for r in summary.by_instance:
            writer.writerow([
                r["instance_id"],
                r["resolved"],
                r["reason"],
                r["status"],
                r["tokens"],
                f"{float(r['cost_usd']):.6f}",
                f"{float(r['wall_clock_seconds']):.2f}",
            ])

    # Write back the (possibly updated) JSONL alongside the original.
    rescored_path = output_dir / "instances.scored.jsonl"
    with rescored_path.open("w", encoding="utf-8") as fh:
        for row in rescored_rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")

    return summary
