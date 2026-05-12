"""``gil-swebench`` -- command-line driver for the SWE-bench harness.

Three subcommands:

* ``run``    -- run a single instance by id (default fixture; use
                ``--dataset swebench-lite`` for the live HF split).
* ``batch``  -- run N instances and aggregate.
* ``score``  -- (re-)score an existing results dir without rerunning gil.

All commands write JSONL to ``results/<run-id>/instances.jsonl`` plus a
``summary.json`` + ``results.csv`` after scoring.

Examples
--------
Single smoke task (no network, no real gild needed if --dry-run)::

    gil-swebench run --instance-id smoke__addition-1 --dry-run

Single live task against a running gild::

    gil-swebench run --instance-id django__django-12345 \\
        --dataset swebench-lite --provider anthropic --model claude-haiku-4

Ten-task batch::

    gil-swebench batch --num 10 --provider anthropic --model claude-haiku-4

Score an existing results dir (e.g. after a crash mid-batch)::

    gil-swebench score results/run-20260427-abc1234
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
import uuid
from pathlib import Path
from typing import Optional

from gil_swebench.grpc_client import DEFAULT_SOCKET_PATH, GilGrpcClient
from gil_swebench.runner import SWEBenchRunner, SWEBenchResult, write_jsonl
from gil_swebench.score import score_results
from gil_swebench.task import (
    SWEBenchTask,
    find_task,
    load_fixture_tasks,
    load_swebench_lite,
)

logger = logging.getLogger("gil_swebench.cli")


# ---------------------------------------------------------------------------
# Dataset selection
# ---------------------------------------------------------------------------


def _load_tasks(dataset: str, *, fixture_path: Optional[str] = None) -> list[SWEBenchTask]:
    if dataset in ("fixture", "smoke"):
        return load_fixture_tasks(fixture_path) if fixture_path else load_fixture_tasks()
    if dataset in ("swebench-lite", "lite", "princeton-nlp/SWE-bench_Lite"):
        return load_swebench_lite("test", dataset_name="princeton-nlp/SWE-bench_Lite")
    if dataset in ("swebench-verified", "verified", "princeton-nlp/SWE-bench_Verified"):
        return load_swebench_lite("test", dataset_name="princeton-nlp/SWE-bench_Verified")
    # Treat anything else as an HF dataset id.
    return load_swebench_lite("test", dataset_name=dataset)


# ---------------------------------------------------------------------------
# Run-id + results dir
# ---------------------------------------------------------------------------


def _new_run_id() -> str:
    ts = time.strftime("%Y%m%d-%H%M%S")
    return f"run-{ts}-{uuid.uuid4().hex[:6]}"


def _ensure_results_dir(base: str, run_id: str) -> Path:
    p = Path(base) / run_id
    p.mkdir(parents=True, exist_ok=True)
    return p


# ---------------------------------------------------------------------------
# Client construction
# ---------------------------------------------------------------------------


def _make_client(args: argparse.Namespace) -> GilGrpcClient:
    metadata = []
    if getattr(args, "bearer_token", None):
        metadata.append(("authorization", f"Bearer {args.bearer_token}"))
    if getattr(args, "target", None):
        return GilGrpcClient(target=args.target, metadata=metadata, timeout_sec=args.timeout_sec)
    return GilGrpcClient(socket_path=args.socket, metadata=metadata, timeout_sec=args.timeout_sec)


# ---------------------------------------------------------------------------
# `run` -- one instance
# ---------------------------------------------------------------------------


def cmd_run(args: argparse.Namespace) -> int:
    tasks = _load_tasks(args.dataset, fixture_path=args.fixture)
    try:
        task = find_task(args.instance_id, tasks=tasks)
    except KeyError:
        print(f"instance_id not found: {args.instance_id}", file=sys.stderr)
        return 2

    run_dir = _ensure_results_dir(args.results_dir, args.run_id or _new_run_id())
    print(f"results dir: {run_dir}")

    if args.dry_run:
        # Just dump the rendered spec_dict so users can sanity-check before
        # spending tokens. No clone, no gild, no run.
        spec = task.to_spec_dict(
            working_dir="<dry-run>",
            max_iter=args.max_iter,
            max_tokens=args.max_tokens,
            max_cost_usd=args.max_cost_usd,
            autonomy=args.autonomy,
        )
        out = {
            "instance_id": task.instance_id,
            "spec": spec,
            "fail_to_pass": task.fail_to_pass,
            "pass_to_pass": task.pass_to_pass,
        }
        print(json.dumps(out, indent=2, ensure_ascii=False))
        return 0

    client = _make_client(args)
    try:
        runner = SWEBenchRunner(
            client,
            workspace_root=args.workspace_root,
            keep_workspaces=args.keep_workspaces,
            provider=args.provider,
            model=args.model,
            run_timeout_sec=args.timeout_sec,
            max_iter=args.max_iter,
            max_tokens=args.max_tokens,
            max_cost_usd=args.max_cost_usd,
            autonomy=args.autonomy,
        )
        result = runner.run_one(task)
        _print_result(result, idx=1, total=1)
        write_jsonl([result], run_dir / "instances.jsonl")
        if not args.skip_score:
            print("\n--- scoring ---")
            summary = score_results(run_dir / "instances.jsonl", tasks=tasks, output_dir=run_dir)
            _print_summary(summary)
            return 0 if summary.n_resolved == summary.n_total and summary.n_total > 0 else 1
        return 0 if result.fail_to_pass_verifier_passed else 1
    finally:
        client.close()


# ---------------------------------------------------------------------------
# `batch` -- N instances
# ---------------------------------------------------------------------------


def cmd_batch(args: argparse.Namespace) -> int:
    tasks = _load_tasks(args.dataset, fixture_path=args.fixture)

    if args.instance_ids:
        ids = [s.strip() for s in args.instance_ids.split(",") if s.strip()]
        tasks = [t for t in tasks if t.instance_id in set(ids)]
        if not tasks:
            print("No matching instance_ids found.", file=sys.stderr)
            return 2
    else:
        tasks = tasks[: max(0, args.num)]
        if not tasks:
            print("No tasks to run (dataset is empty or --num=0).", file=sys.stderr)
            return 2

    run_id = args.run_id or _new_run_id()
    run_dir = _ensure_results_dir(args.results_dir, run_id)
    print(f"results dir: {run_dir}")
    print(f"running {len(tasks)} task(s)")

    client = _make_client(args)
    try:
        runner = SWEBenchRunner(
            client,
            workspace_root=args.workspace_root,
            keep_workspaces=args.keep_workspaces,
            provider=args.provider,
            model=args.model,
            run_timeout_sec=args.timeout_sec,
            max_iter=args.max_iter,
            max_tokens=args.max_tokens,
            max_cost_usd=args.max_cost_usd,
            autonomy=args.autonomy,
        )

        # Stream-write JSONL so a crash mid-run still leaves usable data.
        jsonl_path = run_dir / "instances.jsonl"
        for i, task in enumerate(tasks, start=1):
            result = runner.run_one(task)
            _print_result(result, idx=i, total=len(tasks))
            write_jsonl([result], jsonl_path)

        if not args.skip_score:
            print("\n--- scoring ---")
            summary = score_results(jsonl_path, tasks=tasks, output_dir=run_dir)
            _print_summary(summary)
            return 0 if summary.n_resolved == summary.n_total else 1
        return 0
    finally:
        client.close()


# ---------------------------------------------------------------------------
# `score` -- (re-)score an existing results dir
# ---------------------------------------------------------------------------


def cmd_score(args: argparse.Namespace) -> int:
    results_dir = Path(args.results_dir).resolve()
    jsonl = results_dir / "instances.jsonl"
    if not jsonl.is_file():
        print(f"no instances.jsonl in {results_dir}", file=sys.stderr)
        return 2

    tasks = _load_tasks(args.dataset, fixture_path=args.fixture)
    summary = score_results(
        jsonl,
        tasks=tasks,
        output_dir=results_dir,
        rescore=args.rescore,
        skip_clone=args.skip_clone,
    )
    _print_summary(summary)
    return 0 if summary.n_resolved == summary.n_total else 1


# ---------------------------------------------------------------------------
# Pretty-printers
# ---------------------------------------------------------------------------


def _print_result(r: SWEBenchResult, *, idx: int, total: int) -> None:
    bar = "VERIFY-OK" if r.fail_to_pass_verifier_passed else (
        "ERR" if r.status == "error" else "VERIFY-FAIL"
    )
    print(
        f"[{idx}/{total}] {bar:<11} instance={r.instance_id:<32} "
        f"status={r.status:<14} iters={r.iterations:<3} "
        f"tokens={r.tokens:<8} cost=${r.cost_usd:.4f} "
        f"elapsed={r.wall_clock_seconds:6.1f}s "
        f"patch_bytes={len(r.model_patch)}"
    )
    if r.error_message:
        print(f"    error: {r.error_message[:240]}")


def _print_summary(summary) -> None:
    n = summary.n_total
    print("--- summary ---")
    print(f"  instances:       {n}")
    print(f"  resolved:        {summary.n_resolved}/{n} ({summary.pass_at_1*100:.1f}%)")
    print(f"  unresolved:      {summary.n_unresolved}")
    print(f"  errored:         {summary.n_errored}")
    print(f"  skipped:         {summary.n_skipped}")
    print(f"  total tokens:    {summary.total_tokens}")
    print(f"  total cost:      ${summary.total_cost_usd:.4f}")
    print(f"  total wall time: {summary.total_wall_clock_seconds:.1f}s")


# ---------------------------------------------------------------------------
# Argparse
# ---------------------------------------------------------------------------


def _add_common_run_args(p: argparse.ArgumentParser) -> None:
    p.add_argument(
        "--socket",
        default=DEFAULT_SOCKET_PATH,
        help="Path to gild's UDS (default: %(default)s).",
    )
    p.add_argument(
        "--target",
        default=None,
        help="Override gRPC target (e.g. 'localhost:7777').",
    )
    p.add_argument(
        "--bearer-token",
        default=None,
        help="Optional OIDC bearer token (for TCP gild with --auth enabled).",
    )
    p.add_argument(
        "--provider",
        default="",
        help="gil provider (anthropic | openai | vllm | mock). Default: server default.",
    )
    p.add_argument(
        "--model",
        default="",
        help="Model id. Default: server default.",
    )
    p.add_argument(
        "--dataset",
        default="fixture",
        help=(
            "Dataset selector. 'fixture' (default; bundled smoke tasks, no network), "
            "'swebench-lite', 'swebench-verified', or any HuggingFace dataset id."
        ),
    )
    p.add_argument(
        "--fixture",
        default=None,
        help="Override path to the fixture JSONL (only used when --dataset=fixture).",
    )
    p.add_argument(
        "--workspace-root",
        default=None,
        help="Base dir for per-instance clones (default: ~/.gil/swebench-workspaces).",
    )
    p.add_argument(
        "--keep-workspaces",
        action="store_true",
        help="Don't delete per-instance dirs after the run (debugging).",
    )
    p.add_argument(
        "--results-dir",
        default="results",
        help="Where to write per-run output dirs (default: %(default)s).",
    )
    p.add_argument(
        "--run-id",
        default=None,
        help="Pin the run dir name (default: timestamp + random suffix).",
    )
    p.add_argument(
        "--timeout-sec",
        type=float,
        default=60 * 60,
        help="Per-instance run timeout, seconds (default: %(default)s).",
    )
    p.add_argument(
        "--max-iter",
        type=int,
        default=30,
        help="Max gil agent iterations (default: %(default)s).",
    )
    p.add_argument(
        "--max-tokens",
        type=int,
        default=500_000,
        help="Per-instance token budget (default: %(default)s).",
    )
    p.add_argument(
        "--max-cost-usd",
        type=float,
        default=5.0,
        help="Per-instance USD cost ceiling (default: %(default)s).",
    )
    p.add_argument(
        "--autonomy",
        default="ASK_DESTRUCTIVE_ONLY",
        choices=["FULL", "ASK_DESTRUCTIVE_ONLY", "ASK_ALWAYS"],
        help="gil autonomy mode (default: %(default)s).",
    )
    p.add_argument(
        "--skip-score",
        action="store_true",
        help="Skip the FAIL_TO_PASS / PASS_TO_PASS scoring after the run.",
    )


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="gil-swebench",
        description="SWE-bench harness driver for gil.",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_run = sub.add_parser("run", help="Run a single SWE-bench instance.")
    p_run.add_argument(
        "--instance-id",
        required=True,
        help="The SWE-bench instance_id to run (e.g. 'smoke__addition-1' or 'django__django-12345').",
    )
    p_run.add_argument(
        "--dry-run",
        action="store_true",
        help="Don't talk to gild; just render and print the spec_dict.",
    )
    _add_common_run_args(p_run)
    p_run.set_defaults(func=cmd_run)

    p_batch = sub.add_parser("batch", help="Run N SWE-bench instances.")
    g = p_batch.add_mutually_exclusive_group()
    g.add_argument(
        "--num",
        type=int,
        default=10,
        help="Number of instances to run from the dataset head (default: %(default)s).",
    )
    g.add_argument(
        "--instance-ids",
        default=None,
        help="Comma-separated list of explicit instance_ids (overrides --num).",
    )
    _add_common_run_args(p_batch)
    p_batch.set_defaults(func=cmd_batch)

    p_score = sub.add_parser("score", help="(Re-)score an existing results dir.")
    p_score.add_argument(
        "results_dir",
        help="Path to a results dir containing instances.jsonl.",
    )
    p_score.add_argument(
        "--dataset",
        default="fixture",
        help="Dataset selector (must match the one used at run time).",
    )
    p_score.add_argument(
        "--fixture",
        default=None,
        help="Override path to the fixture JSONL.",
    )
    p_score.add_argument(
        "--rescore",
        action="store_true",
        help="Re-score even rows that already have 'resolved' set.",
    )
    p_score.add_argument(
        "--skip-clone",
        action="store_true",
        help="Skip cloning during scoring (assumes workspaces already populated).",
    )
    p_score.set_defaults(func=cmd_score)

    return parser


def main(argv: Optional[list[str]] = None) -> int:
    logging.basicConfig(
        level=os.environ.get("GIL_SWEBENCH_LOG", "WARNING").upper(),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    args = _build_parser().parse_args(argv)
    try:
        return int(args.func(args) or 0)
    except KeyboardInterrupt:
        print("\nInterrupted.", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
