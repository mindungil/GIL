"""``gil-atropos-eval`` -- ad-hoc evaluation CLI.

Run a small batch of coding tasks against a running ``gild`` and print the
scored summary. Useful for sanity-checking a model + harness combo without
spinning up the full Atropos training stack.

Examples
--------
Evaluate the bundled tasks::

    gil-atropos-eval --num 5

Evaluate against a specific model::

    gil-atropos-eval --model claude-sonnet-4 --provider anthropic --num 3

Use HuggingFace humaneval (falls back to bundled if datasets not installed)::

    gil-atropos-eval --dataset openai_humaneval --num 10

Connect to a remote gild over TCP::

    gil-atropos-eval --target localhost:7777 --num 1
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
from typing import Optional

from .env import GilCodingEnv, ScoredRollout
from .grpc_client import DEFAULT_SOCKET_PATH, GilGrpcClient


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="gil-atropos-eval",
        description="Ad-hoc evaluation: run gil sessions on coding tasks and score them.",
    )
    p.add_argument(
        "--socket",
        default=DEFAULT_SOCKET_PATH,
        help="Path to gild's UDS (default: %(default)s).",
    )
    p.add_argument(
        "--target",
        default=None,
        help="Override gRPC target (e.g. 'localhost:7777'). When set, --socket is ignored.",
    )
    p.add_argument(
        "--model",
        default="",
        help="Model id passed to RunService.Start (default: server default).",
    )
    p.add_argument(
        "--provider",
        default="",
        help="Provider name (anthropic | mock | ...). Default: server default.",
    )
    p.add_argument(
        "--dataset",
        default="bundled",
        help="Dataset name. Use 'bundled' for the in-tree fallback or any HuggingFace dataset id.",
    )
    p.add_argument(
        "--num",
        type=int,
        default=5,
        help="Number of tasks to evaluate (default: %(default)s).",
    )
    p.add_argument(
        "--seed",
        type=int,
        default=0,
        help="RNG seed for the train/eval split (default: %(default)s).",
    )
    p.add_argument(
        "--workspace-root",
        default=None,
        help="Base dir for per-rollout workspaces (default: a fresh tempdir).",
    )
    p.add_argument(
        "--keep-workspaces",
        action="store_true",
        help="Don't delete per-rollout workspaces (debugging).",
    )
    p.add_argument(
        "--json",
        action="store_true",
        help="Emit machine-readable JSON instead of the human summary.",
    )
    p.add_argument(
        "--timeout-sec",
        type=float,
        default=600.0,
        help="Per-rollout timeout (default: %(default)ss).",
    )
    p.add_argument(
        "--bearer-token",
        default=None,
        help="Optional OIDC bearer token (for TCP gild with --auth enabled).",
    )
    return p


async def _run(args: argparse.Namespace) -> int:
    metadata = []
    if args.bearer_token:
        metadata.append(("authorization", f"Bearer {args.bearer_token}"))

    client_kwargs: dict = {"timeout_sec": args.timeout_sec}
    if args.target:
        client = GilGrpcClient(target=args.target, metadata=metadata, **client_kwargs)
    else:
        client = GilGrpcClient(socket_path=args.socket, metadata=metadata, **client_kwargs)

    dataset_name: Optional[str]
    if args.dataset == "bundled":
        dataset_name = None
    else:
        dataset_name = args.dataset

    env = GilCodingEnv(
        socket_path=args.socket,
        model=args.model,
        provider=args.provider,
        workspace_root=args.workspace_root,
        keep_workspaces=args.keep_workspaces,
        dataset_name=dataset_name,
        eval_size=args.num,
        seed=args.seed,
        client=client,
        run_timeout_sec=args.timeout_sec,
    )

    try:
        await env.setup()
        results: list[ScoredRollout] = []
        # Evaluate up to args.num items, drawing from the eval pool first then
        # cycling through train items if eval is short.
        pool = list(env._eval_items) + list(env._items)  # noqa: SLF001 -- internal helper, fine for CLI
        if not pool:
            print("No tasks available.", file=sys.stderr)
            return 1
        for i in range(args.num):
            task = pool[i % len(pool)]
            try:
                scored = await env.evaluate(task)
            except Exception as exc:
                print(f"[task {task.task_id}] error: {exc}", file=sys.stderr)
                continue
            results.append(scored)
            if not args.json:
                _print_one(i + 1, args.num, scored)

        if args.json:
            print(json.dumps([r.to_dict() for r in results], indent=2))
        else:
            _print_summary(results)
        return 0 if all(r.reward > 0 for r in results) else 2
    finally:
        env.close()


def _print_one(idx: int, total: int, r: ScoredRollout) -> None:
    bar = "PASS" if r.reward >= 0.999 else ("FAIL" if r.reward == 0.0 else "PART")
    print(
        f"[{idx}/{total}] {bar} task={r.task_id:<24} "
        f"reward={r.reward:.2f} pass_ratio={r.pass_ratio:.2f} "
        f"iters={r.iterations} tokens={r.tokens} "
        f"cost=${r.cost_usd:.4f} elapsed={r.wall_clock_seconds:.1f}s "
        f"status={r.status}"
    )
    if r.error_message:
        print(f"    error: {r.error_message}")


def _print_summary(results: list[ScoredRollout]) -> None:
    if not results:
        print("\nNo results.")
        return
    n = len(results)
    mean_reward = sum(r.reward for r in results) / n
    full_pass = sum(1 for r in results if r.reward >= 0.999)
    total_tokens = sum(r.tokens for r in results)
    total_cost = sum(r.cost_usd for r in results)
    print("\n--- summary ---")
    print(f"  rollouts:     {n}")
    print(f"  mean reward:  {mean_reward:.3f}")
    print(f"  full passes:  {full_pass}/{n}")
    print(f"  total tokens: {total_tokens}")
    print(f"  total cost:   ${total_cost:.4f}")


def main(argv: Optional[list[str]] = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        return asyncio.run(_run(args))
    except KeyboardInterrupt:
        print("\nInterrupted.", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
