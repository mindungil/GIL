"""SWEBenchTask dataclass + dataset loaders.

A SWE-bench instance has the canonical fields:

* ``instance_id``       -- stable id, e.g. ``"django__django-12345"``
* ``repo``              -- ``"django/django"``
* ``base_commit``       -- SHA the agent should clone at
* ``problem_statement`` -- natural-language issue body (the goal)
* ``hints_text``        -- optional extra context surfaced in the issue
* ``test_patch``        -- diff that adds/edits the official tests for this fix
* ``FAIL_TO_PASS``      -- list of test-ids that should newly pass after fix
* ``PASS_TO_PASS``      -- list of test-ids that must keep passing
* ``patch``             -- the gold-truth fix (we never show this to the agent)
* ``version``           -- repo version tag (used for env image selection)

We model these as a frozen dataclass so the runner can pass it around and
the score module can re-read the FAIL_TO_PASS / PASS_TO_PASS lists.

Loaders
-------
* :func:`load_swebench_lite` -- pulls ``princeton-nlp/SWE-bench_Lite`` from
  HuggingFace. Requires the ``datasets`` extra. Network-only -- never invoked
  at install time.
* :func:`load_fixture_tasks` -- reads the bundled JSONL fixture
  (``fixtures/smoke.jsonl``) so unit tests + ``--instance-id smoke-*`` runs
  work offline.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any, Iterable, Mapping, Optional


# ---------------------------------------------------------------------------
# Dataclass
# ---------------------------------------------------------------------------


@dataclass
class SWEBenchTask:
    """One SWE-bench instance.

    All fields use the canonical SWE-bench naming so a record from
    ``princeton-nlp/SWE-bench_Lite`` maps in cleanly.
    """

    instance_id: str
    repo: str
    base_commit: str
    problem_statement: str

    # Test selectors -- two lists of pytest node ids, with semantics described
    # in the SWE-bench paper:
    #   FAIL_TO_PASS: failing on base_commit, must pass after agent's patch
    #   PASS_TO_PASS: passing on base_commit, must still pass after patch
    fail_to_pass: list[str] = field(default_factory=list)
    pass_to_pass: list[str] = field(default_factory=list)

    # Optional / metadata
    hints_text: str = ""
    test_patch: str = ""           # diff that introduces the official tests
    version: str = ""
    environment_setup_commit: str = ""
    created_at: str = ""

    # We deliberately do NOT carry the gold-truth ``patch`` here -- the runner
    # never needs it and accidentally surfacing it to the agent would invalidate
    # the benchmark. Score-side code that needs it can read the raw record.

    # ------------------------------------------------------------------
    # Spec rendering
    # ------------------------------------------------------------------

    def to_spec_dict(
        self,
        *,
        working_dir: str = "",
        max_iter: int = 30,
        max_tokens: int = 500_000,
        max_cost_usd: float = 5.0,
        per_check_timeout_seconds: int = 600,
        autonomy: str = "ASK_DESTRUCTIVE_ONLY",
    ) -> dict[str, Any]:
        """Render this task as a gil ``FrozenSpec``-shaped dict.

        The verifier here is intentionally minimal: it runs the union of
        FAIL_TO_PASS and PASS_TO_PASS via ``pytest`` and reports a single pass
        / fail. A more granular check is performed offline by
        :func:`gil_swebench.score.score_one`, which re-runs the test patch
        and the two test sets separately to compute ``resolved=true|false``.

        We pass the test-id list both as a CLI arg and by writing it into a
        file so very long lists don't blow up shell argv limits.
        """
        # Surface a short one-liner for the agent: first non-blank line of the
        # issue or the issue's first 200 chars.
        one_liner = _problem_one_liner(self.problem_statement, self.instance_id)

        # The verifier runs *inside* the cloned repo's working_dir. We write
        # the FAIL_TO_PASS list to ``.gil-swebench/fail_to_pass.txt`` so the
        # check command stays short. PASS_TO_PASS is checked separately by the
        # offline scorer; running it inside gil's verifier loop on every
        # iteration would be too slow on big repos.
        node_id_blob = "\n".join(self.fail_to_pass)
        verifier_cmd = (
            "set -e\n"
            "mkdir -p .gil-swebench\n"
            "cat > .gil-swebench/fail_to_pass.txt <<'NODES'\n"
            f"{node_id_blob}\n"
            "NODES\n"
            "if [ ! -s .gil-swebench/fail_to_pass.txt ]; then\n"
            "  echo 'no FAIL_TO_PASS tests recorded; nothing to verify' >&2\n"
            "  exit 0\n"
            "fi\n"
            "python -m pytest -q $(cat .gil-swebench/fail_to_pass.txt)"
        )

        return {
            "goal": {
                "one_liner": one_liner,
                "detailed": self.problem_statement,
                "success_criteria_natural": [
                    "All FAIL_TO_PASS tests now pass.",
                    "All PASS_TO_PASS tests still pass (verified offline).",
                ],
                "non_goals": [
                    "Do not edit tests under .gil-swebench/.",
                    "Do not modify the test_patch tests added by the benchmark harness.",
                ],
            },
            "constraints": {
                "tech_stack": ["python>=3.8"],
                "forbidden": [
                    "network access beyond the cloned repo",
                    "editing the official benchmark test files added by test_patch",
                ],
            },
            "verification": {
                "checks": [
                    {
                        "name": f"swebench:{self.instance_id}:fail_to_pass",
                        "kind": "SHELL",
                        "command": verifier_cmd,
                        "expected_exit_code": 0,
                    }
                ],
                "max_retries_per_check": 1,
                "per_check_timeout_seconds": per_check_timeout_seconds,
            },
            "workspace": {
                "backend": "LOCAL_NATIVE",
                "path": working_dir,
            },
            "budget": {
                "max_total_tokens": max_tokens,
                "max_total_cost_usd": max_cost_usd,
                # Reserve ~20k tokens for compaction / final report; gil's
                # compactor uses the same key but reads it as a separate
                # field on the spec when present.
                "max_reserve_tokens": 20_000,
                "max_wall_clock_seconds": 60 * 60,
                "max_iterations": max_iter,
                "max_subagent_depth": 1,
            },
            "tools": {"bash": True, "file_ops": True, "git": True},
            "risk": {"autonomy": autonomy},
            # Hints surfaced to the agent (consulted by gil; not part of the
            # FrozenSpec proto). The runner uses these to seed the workspace.
            "_swebench_meta": {
                "instance_id": self.instance_id,
                "repo": self.repo,
                "base_commit": self.base_commit,
                "version": self.version,
                "hints_text": self.hints_text,
            },
        }

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def _problem_one_liner(problem_statement: str, instance_id: str) -> str:
    """First non-blank line of the issue, capped at 200 chars."""
    if not problem_statement:
        return f"SWE-bench task {instance_id}"
    for line in problem_statement.splitlines():
        s = line.strip()
        if s:
            return s[:200]
    return problem_statement.strip()[:200] or f"SWE-bench task {instance_id}"


# ---------------------------------------------------------------------------
# Loaders
# ---------------------------------------------------------------------------


def _from_record(rec: Mapping[str, Any]) -> SWEBenchTask:
    """Convert a HuggingFace dataset record (dict) to a SWEBenchTask.

    SWE-bench's HF schema stores FAIL_TO_PASS / PASS_TO_PASS as JSON-encoded
    string lists -- we parse those back into Python lists.
    """

    def _coerce_list(v: Any) -> list[str]:
        if v is None:
            return []
        if isinstance(v, list):
            return [str(x) for x in v]
        if isinstance(v, str):
            v = v.strip()
            if not v:
                return []
            # SWE-bench stores these as JSON-encoded lists.
            try:
                parsed = json.loads(v)
                if isinstance(parsed, list):
                    return [str(x) for x in parsed]
            except json.JSONDecodeError:
                pass
            # Fallback: newline-separated.
            return [line.strip() for line in v.splitlines() if line.strip()]
        return [str(v)]

    return SWEBenchTask(
        instance_id=str(rec.get("instance_id", "")),
        repo=str(rec.get("repo", "")),
        base_commit=str(rec.get("base_commit", "")),
        problem_statement=str(rec.get("problem_statement", "")),
        fail_to_pass=_coerce_list(rec.get("FAIL_TO_PASS") or rec.get("fail_to_pass")),
        pass_to_pass=_coerce_list(rec.get("PASS_TO_PASS") or rec.get("pass_to_pass")),
        hints_text=str(rec.get("hints_text", "")),
        test_patch=str(rec.get("test_patch", "")),
        version=str(rec.get("version", "")),
        environment_setup_commit=str(rec.get("environment_setup_commit", "")),
        created_at=str(rec.get("created_at", "")),
    )


def load_swebench_lite(
    split: str = "test",
    *,
    dataset_name: str = "princeton-nlp/SWE-bench_Lite",
) -> list[SWEBenchTask]:
    """Pull SWE-bench-Lite from HuggingFace.

    Requires the ``datasets`` extra (``pip install gil-swebench[datasets]``)
    and *network access*. This function is never called at import or install
    time -- only when the user explicitly asks for live tasks.

    Use ``dataset_name="princeton-nlp/SWE-bench_Verified"`` for the
    500-instance human-vetted split.
    """
    try:
        from datasets import load_dataset  # type: ignore[import-not-found]
    except ImportError as exc:
        raise RuntimeError(
            "Loading SWE-bench from HuggingFace requires the 'datasets' "
            "package. Install it with: pip install 'gil-swebench[datasets]'"
        ) from exc

    ds = load_dataset(dataset_name, split=split)
    out: list[SWEBenchTask] = []
    for rec in ds:
        out.append(_from_record(rec))
    return out


def load_fixture_tasks(path: Optional[str | Path] = None) -> list[SWEBenchTask]:
    """Load tasks from the bundled (or user-supplied) JSONL fixture.

    The default fixture ships with the package and contains a handful of
    synthetic SWE-bench-shaped records that work fully offline. Real
    benchmarking should use :func:`load_swebench_lite` instead.
    """
    if path is None:
        path = Path(__file__).resolve().parent / "fixtures" / "smoke.jsonl"
    p = Path(path)
    if not p.is_file():
        raise FileNotFoundError(f"Fixture file not found: {p}")
    out: list[SWEBenchTask] = []
    with p.open("r", encoding="utf-8") as fh:
        for line_num, line in enumerate(fh, start=1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"{p}:{line_num}: invalid JSON: {exc}") from exc
            out.append(_from_record(rec))
    return out


def find_task(
    instance_id: str,
    *,
    tasks: Optional[Iterable[SWEBenchTask]] = None,
) -> SWEBenchTask:
    """Look up a task by ``instance_id`` in a list (defaults to fixture)."""
    pool = list(tasks) if tasks is not None else load_fixture_tasks()
    for t in pool:
        if t.instance_id == instance_id:
            return t
    raise KeyError(f"instance_id not found: {instance_id}")
