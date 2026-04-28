"""gil_swebench -- SWE-bench harness adapter for gil.

Wraps gil (the autonomous coding harness) as a SWE-bench evaluator. Each
benchmark instance corresponds to one full gil session::

    clone repo @ base_commit
        -> freeze spec (problem_statement + FAIL_TO_PASS verifier)
        -> autonomous run
        -> apply git-diff & re-run official tests
        -> resolved? (FAIL_TO_PASS now pass AND PASS_TO_PASS still pass)

Public API
----------
- ``SWEBenchTask``       -- one benchmark instance (loader + dataclass)
- ``SWEBenchRunner``     -- drives one rollout against a running gild
- ``SWEBenchResult``     -- per-instance outcome (resolved + cost + patch)
- ``GilGrpcClient``      -- thin gRPC wrapper, mirrors gil_atropos
- ``score_results``      -- aggregate pass@1 from a results dir / JSONL
- ``load_fixture_tasks`` -- pull bundled smoke tasks (no network)
"""

from gil_swebench.grpc_client import GilGrpcClient
from gil_swebench.task import (
    SWEBenchTask,
    load_fixture_tasks,
    load_swebench_lite,
)
from gil_swebench.runner import SWEBenchRunner, SWEBenchResult
from gil_swebench.score import score_results, score_one

__all__ = [
    "GilGrpcClient",
    "SWEBenchTask",
    "SWEBenchRunner",
    "SWEBenchResult",
    "load_fixture_tasks",
    "load_swebench_lite",
    "score_results",
    "score_one",
]

__version__ = "0.1.0"
