"""Smoke tests for gil_swebench.

These do NOT require a running gild, real network, or grpcio. The gRPC
client is mocked end-to-end. The aim is to catch obvious wiring bugs
(imports break, dataclasses missing fields, spec_dict missing required
keys, score logic off-by-one, ...).

Run::

    pip install -e 'python/gil_swebench[dev]'
    pytest python/gil_swebench/tests -v
"""

from __future__ import annotations

import json
import sys
import textwrap
from pathlib import Path
from unittest.mock import MagicMock

import pytest

# Make the package importable when running pytest directly without `pip install -e`.
_HERE = Path(__file__).resolve().parent
_PKG_PARENT = _HERE.parent.parent  # .../python
if str(_PKG_PARENT) not in sys.path:
    sys.path.insert(0, str(_PKG_PARENT))


# ---------------------------------------------------------------------------
# Imports + public surface
# ---------------------------------------------------------------------------


def test_package_imports():
    import gil_swebench

    for name in (
        "GilGrpcClient",
        "SWEBenchTask",
        "SWEBenchRunner",
        "SWEBenchResult",
        "load_fixture_tasks",
        "score_results",
        "score_one",
    ):
        assert hasattr(gil_swebench, name), f"missing: {name}"
    assert gil_swebench.__version__ == "0.1.0"


# ---------------------------------------------------------------------------
# Task loader + fixture
# ---------------------------------------------------------------------------


def test_fixture_tasks_load():
    from gil_swebench import load_fixture_tasks

    tasks = load_fixture_tasks()
    assert len(tasks) >= 3
    ids = {t.instance_id for t in tasks}
    assert "smoke__addition-1" in ids
    # Smoke tasks have non-empty FAIL_TO_PASS sets.
    for t in tasks:
        assert t.fail_to_pass, f"{t.instance_id} has no FAIL_TO_PASS"
        assert t.repo
        assert t.problem_statement


def test_task_to_spec_dict_shape():
    from gil_swebench.task import find_task

    task = find_task("smoke__addition-1")
    spec = task.to_spec_dict(working_dir="/tmp/x")
    for key in ("goal", "constraints", "verification", "workspace", "budget", "tools", "risk"):
        assert key in spec, f"missing key: {key}"
    checks = spec["verification"]["checks"]
    assert checks and checks[0]["kind"] == "SHELL"
    assert checks[0]["expected_exit_code"] == 0
    assert spec["risk"]["autonomy"] == "ASK_DESTRUCTIVE_ONLY"
    # Budget reserve is set (per Phase 23 Track C spec).
    assert spec["budget"]["max_reserve_tokens"] == 20_000
    # Verifier embeds the FAIL_TO_PASS list.
    assert "test_calc.py::test_add_positive" in checks[0]["command"]
    # Working dir is plumbed through.
    assert spec["workspace"]["path"] == "/tmp/x"
    # SWE-bench metadata stashed for the runner / agent.
    assert spec["_swebench_meta"]["instance_id"] == "smoke__addition-1"


def test_task_record_parses_json_encoded_lists():
    """SWE-bench's HF schema stores FAIL_TO_PASS as a JSON-encoded string."""
    from gil_swebench.task import _from_record

    rec = {
        "instance_id": "x__y-1",
        "repo": "x/y",
        "base_commit": "abc",
        "problem_statement": "fix it",
        "FAIL_TO_PASS": json.dumps(["t::a", "t::b"]),
        "PASS_TO_PASS": json.dumps(["t::c"]),
    }
    t = _from_record(rec)
    assert t.fail_to_pass == ["t::a", "t::b"]
    assert t.pass_to_pass == ["t::c"]


# ---------------------------------------------------------------------------
# Runner -- with a mock gRPC client (no gild required)
# ---------------------------------------------------------------------------


def _make_mock_client(*, status: str = "done", verifier_passed: bool = True) -> MagicMock:
    from gil_swebench.grpc_client import RunResult

    verify_results = [{
        "name": "swebench:smoke__addition-1:fail_to_pass",
        "passed": verifier_passed,
        "exit_code": 0 if verifier_passed else 1,
        "stdout": "",
        "stderr": "",
    }]
    client = MagicMock()
    client.create_session.return_value = "sess-123"
    client.freeze_spec.return_value = {"spec_id": "spec-abc", "content_sha256": "deadbeef"}
    client.run_session.return_value = RunResult(
        status=status,
        iterations=5,
        tokens=12345,
        cost_usd=0.07,
        verify_results=verify_results,
        error_message="" if status != "error" else "boom",
    )
    return client


def test_runner_skips_clone_and_records_result(tmp_path):
    from gil_swebench import SWEBenchRunner
    from gil_swebench.task import find_task

    client = _make_mock_client(status="done", verifier_passed=True)
    runner = SWEBenchRunner(
        client,
        workspace_root=str(tmp_path),
        keep_workspaces=True,
        skip_clone=True,
    )

    task = find_task("smoke__addition-1")
    # Pre-populate the workspace as if it had been cloned: empty git repo.
    wd = tmp_path / "wd"
    wd.mkdir()

    result = runner.run_one(task, workspace_path=str(wd))
    assert result.instance_id == "smoke__addition-1"
    assert result.status == "done"
    assert result.iterations == 5
    assert result.tokens == 12345
    assert result.fail_to_pass_verifier_passed is True
    # gRPC client should have been used in the right order.
    client.create_session.assert_called_once()
    client.freeze_spec.assert_called_once()
    client.run_session.assert_called_once()


def test_runner_handles_error_without_raising(tmp_path):
    from gil_swebench import SWEBenchRunner
    from gil_swebench.task import find_task

    # Make freeze_spec blow up to simulate a mid-pipeline failure.
    client = _make_mock_client()
    client.freeze_spec.side_effect = RuntimeError("interview blew up")

    runner = SWEBenchRunner(
        client,
        workspace_root=str(tmp_path),
        keep_workspaces=False,
        skip_clone=True,
    )
    task = find_task("smoke__greet-1")
    wd = tmp_path / "wd"
    wd.mkdir()

    result = runner.run_one(task, workspace_path=str(wd))
    assert result.status == "error"
    assert "interview blew up" in result.error_message
    assert result.fail_to_pass_verifier_passed is False


# ---------------------------------------------------------------------------
# Score -- tested with synthetic patches + a mocked-out subprocess layer
# ---------------------------------------------------------------------------


def test_score_one_empty_patch_is_unresolved(tmp_path):
    from gil_swebench import score_one
    from gil_swebench.runner import SWEBenchResult
    from gil_swebench.task import find_task

    task = find_task("smoke__addition-1")
    res = SWEBenchResult(
        instance_id=task.instance_id,
        repo=task.repo,
        base_commit=task.base_commit,
        status="done",
        model_patch="",
    )
    resolved, reason = score_one(res, task, work_dir=str(tmp_path), skip_clone=True)
    assert resolved is False
    assert "empty patch" in reason


def test_score_one_errored_run_is_unresolved(tmp_path):
    from gil_swebench import score_one
    from gil_swebench.runner import SWEBenchResult
    from gil_swebench.task import find_task

    task = find_task("smoke__addition-1")
    res = SWEBenchResult(
        instance_id=task.instance_id,
        repo=task.repo,
        base_commit=task.base_commit,
        status="error",
        model_patch="",
        error_message="provider timed out",
    )
    resolved, reason = score_one(res, task, work_dir=str(tmp_path), skip_clone=True)
    assert resolved is False
    # Either short-circuit catches it.
    assert "errored" in reason or "empty patch" in reason


def test_score_results_aggregates_jsonl(tmp_path):
    """End-to-end: write a results JSONL with pre-resolved rows, aggregate."""
    from gil_swebench import score_results
    from gil_swebench.task import load_fixture_tasks

    tasks = load_fixture_tasks()

    rows = [
        {
            "instance_id": tasks[0].instance_id,
            "repo": tasks[0].repo,
            "base_commit": tasks[0].base_commit,
            "status": "done",
            "iterations": 4,
            "tokens": 1000,
            "cost_usd": 0.05,
            "wall_clock_seconds": 30.0,
            "model_patch": "diff --git a/x b/x\n",
            "resolved": True,           # pre-scored: resolved
            "resolved_reason": "ok",
            "error_message": "",
        },
        {
            "instance_id": tasks[1].instance_id,
            "repo": tasks[1].repo,
            "base_commit": tasks[1].base_commit,
            "status": "max_iterations",
            "iterations": 30,
            "tokens": 50_000,
            "cost_usd": 0.45,
            "wall_clock_seconds": 600.0,
            "model_patch": "",
            "resolved": False,          # pre-scored: unresolved
            "resolved_reason": "agent gave up",
            "error_message": "",
        },
        {
            "instance_id": tasks[2].instance_id,
            "repo": tasks[2].repo,
            "base_commit": tasks[2].base_commit,
            "status": "error",
            "iterations": 1,
            "tokens": 100,
            "cost_usd": 0.001,
            "wall_clock_seconds": 5.0,
            "model_patch": "",
            "error_message": "provider unreachable",
        },
    ]

    jsonl = tmp_path / "instances.jsonl"
    with jsonl.open("w", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")

    summary = score_results(jsonl, tasks=tasks, output_dir=tmp_path, rescore=False)
    assert summary.n_total == 3
    assert summary.n_resolved == 1
    assert summary.n_unresolved == 1
    assert summary.n_errored == 1
    assert summary.pass_at_1 == pytest.approx(1 / 3)
    assert summary.total_tokens == 51_100
    assert summary.total_cost_usd == pytest.approx(0.501, rel=1e-3)

    # Output artifacts exist.
    assert (tmp_path / "summary.json").is_file()
    assert (tmp_path / "results.csv").is_file()
    assert (tmp_path / "instances.scored.jsonl").is_file()


# ---------------------------------------------------------------------------
# CLI argparse smoke
# ---------------------------------------------------------------------------


def test_cli_help_does_not_explode():
    from gil_swebench import cli

    parser = cli._build_parser()
    help_text = parser.format_help()
    assert "gil-swebench" in help_text
    # All three subcommands present.
    assert "run" in help_text
    assert "batch" in help_text
    assert "score" in help_text


def test_cli_dry_run_renders_spec(tmp_path, capsys):
    """`gil-swebench run --dry-run --instance-id smoke__addition-1` should
    print the spec without trying to talk to gild."""
    from gil_swebench import cli

    rc = cli.main([
        "run",
        "--instance-id", "smoke__addition-1",
        "--dry-run",
        "--results-dir", str(tmp_path),
    ])
    assert rc == 0
    out = capsys.readouterr().out
    # Spec dict was printed; key fields visible.
    assert "smoke__addition-1" in out
    assert "FAIL_TO_PASS" in out or "fail_to_pass" in out
    assert "ASK_DESTRUCTIVE_ONLY" in out


def test_cli_score_missing_dir_fails_cleanly(tmp_path, capsys):
    from gil_swebench import cli

    rc = cli.main(["score", str(tmp_path / "does-not-exist")])
    assert rc != 0


def test_compile_protos_module_importable():
    """compile_protos should be importable even without grpcio-tools."""
    from gil_swebench import compile_protos

    assert hasattr(compile_protos, "main")
    assert hasattr(compile_protos, "compile_protos")
