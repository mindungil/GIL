"""Smoke tests for gil_atropos.

These do NOT require a running gild -- the gRPC client is mocked. The aim
is to catch obvious wiring bugs (imports break, dataclasses missing fields,
spec_dict missing required keys, ...).

To run::

    pip install -e python/gil_atropos[dev]
    pytest python/gil_atropos/tests -v
"""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path
from unittest.mock import MagicMock

import pytest

# Ensure the package is importable when tests are run directly without `pip install -e`.
_HERE = Path(__file__).resolve().parent
_PKG_PARENT = _HERE.parent.parent  # .../python
if str(_PKG_PARENT) not in sys.path:
    sys.path.insert(0, str(_PKG_PARENT))


# ---------------------------------------------------------------------------
# Imports + public surface
# ---------------------------------------------------------------------------


def test_package_imports():
    import gil_atropos

    assert hasattr(gil_atropos, "GilCodingEnv")
    assert hasattr(gil_atropos, "GilGrpcClient")
    assert hasattr(gil_atropos, "ScoredRollout")
    assert hasattr(gil_atropos, "BUNDLED_TASKS")
    assert gil_atropos.__version__ == "0.1.0"


def test_bundled_tasks_loaded():
    from gil_atropos import BUNDLED_TASKS

    assert len(BUNDLED_TASKS) >= 5
    ids = {t.task_id for t in BUNDLED_TASKS}
    assert {"fibonacci", "reverse_string", "is_palindrome", "fizzbuzz", "sum_csv_column"} <= ids


def test_task_to_spec_dict_shape():
    from gil_atropos.samples import get_task

    spec = get_task("fibonacci").to_spec_dict(working_dir="/tmp/x")
    # Required top-level keys per FrozenSpec proto.
    for key in ("goal", "constraints", "verification", "workspace", "budget", "tools", "risk"):
        assert key in spec, f"missing key: {key}"
    # Verification must have at least one shell check.
    checks = spec["verification"]["checks"]
    assert checks and checks[0]["kind"] == "SHELL"
    assert checks[0]["expected_exit_code"] == 0


# ---------------------------------------------------------------------------
# Env instantiation -- with a mock gRPC client (no gild required)
# ---------------------------------------------------------------------------


def _make_mock_client(*, status: str = "done", verify_results=None) -> MagicMock:
    """Build a MagicMock that quacks like GilGrpcClient."""
    from gil_atropos.grpc_client import RunResult

    if verify_results is None:
        verify_results = [
            {"name": "v", "passed": True, "exit_code": 0, "stdout": "", "stderr": ""}
        ]

    client = MagicMock()
    client.create_session.return_value = "sess-123"
    client.freeze_spec.return_value = {"spec_id": "spec-abc", "content_sha256": "deadbeef"}
    client.run_session.return_value = RunResult(
        status=status,
        iterations=3,
        tokens=1234,
        cost_usd=0.01,
        verify_results=verify_results,
        error_message="" if status != "error" else "boom",
    )
    return client


def test_env_instantiates_without_hermes():
    """GilCodingEnv should be usable even without hermes-agent installed."""
    from gil_atropos import GilCodingEnv

    env = GilCodingEnv(client=_make_mock_client())
    try:
        assert env._items == []  # not yet loaded
        # setup() should populate items from BUNDLED_TASKS as fallback.
        asyncio.run(env.setup())
        assert env._items, "expected at least one train item after setup()"
    finally:
        env.close()


def test_env_evaluate_full_pass():
    from gil_atropos import GilCodingEnv

    env = GilCodingEnv(client=_make_mock_client(status="done"))
    try:
        asyncio.run(env.setup())
        # Pick a deterministic task.
        from gil_atropos.samples import get_task

        result = asyncio.run(env.evaluate(get_task("fibonacci")))
        assert result.task_id == "fibonacci"
        assert result.reward == pytest.approx(1.0)
        assert result.status == "done"
        assert result.pass_ratio == pytest.approx(1.0)
        assert result.verify_results
    finally:
        env.close()


def test_env_evaluate_partial_credit():
    from gil_atropos import GilCodingEnv
    from gil_atropos.samples import get_task

    mixed = [
        {"name": "a", "passed": True, "exit_code": 0, "stdout": "", "stderr": ""},
        {"name": "b", "passed": False, "exit_code": 1, "stdout": "", "stderr": "boom"},
    ]
    env = GilCodingEnv(client=_make_mock_client(status="done", verify_results=mixed))
    try:
        result = asyncio.run(env.evaluate(get_task("reverse_string")))
        assert result.reward == pytest.approx(0.5)
        assert result.pass_ratio == pytest.approx(0.5)
    finally:
        env.close()


def test_env_evaluate_error_status_zero_reward():
    from gil_atropos import GilCodingEnv
    from gil_atropos.samples import get_task

    env = GilCodingEnv(client=_make_mock_client(status="error", verify_results=[]))
    try:
        result = asyncio.run(env.evaluate(get_task("fizzbuzz")))
        assert result.reward == pytest.approx(0.0)
        assert result.status == "error"
        assert result.error_message
    finally:
        env.close()


# ---------------------------------------------------------------------------
# CLI argparse smoke
# ---------------------------------------------------------------------------


def test_cli_help_does_not_explode():
    from gil_atropos import cli

    parser = cli._build_parser()
    # Just ensure --help text renders without errors.
    help_text = parser.format_help()
    assert "gil-atropos-eval" in help_text
    assert "--num" in help_text
    assert "--model" in help_text


def test_compile_protos_module_importable():
    """compile_protos should be importable even without grpcio-tools."""
    from gil_atropos import compile_protos

    parser_args = compile_protos.main.__doc__ or ""
    assert hasattr(compile_protos, "main")
    assert hasattr(compile_protos, "compile_protos")
