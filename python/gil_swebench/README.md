# gil-swebench

[SWE-bench](https://www.swebench.com/) harness adapter for [gil](../../README.md), the autonomous coding harness.

`gil-swebench` runs SWE-bench instances against a live `gild`, collects the
agent's patch + cost, and scores `pass@1` against the official `FAIL_TO_PASS`
/ `PASS_TO_PASS` test sets.

Each rollout is one full gil session:

```
clone repo @ base_commit
    -> freeze spec (problem_statement + verifier)
    -> autonomous run
    -> capture git diff
    -> apply diff + test_patch on a clean clone
    -> run FAIL_TO_PASS and PASS_TO_PASS via pytest
    -> resolved iff both sets pass
```

This package mirrors the structure of [`gil_atropos`](../gil_atropos/) — the
gRPC client, proto bridge, and `compile_protos` story are intentionally
identical so the two adapters can evolve together.

---

## Status (pre-1.0)

This is **harness, not benchmark**. Phase 23 Track C delivers the pipeline; we
have not yet run a real benchmark against `gild`. The first runs are expected
to be in the **0–20% pass@1** range — that's a starting line, not an end state.

`gil-swebench` is for *validating that gil's pipeline works on hard, realistic
tasks*, not for chasing leaderboard numbers. SOTA published on SWE-bench-Lite
is far above what a pre-1.0 harness will produce; please do not cite results
from this tool out-of-context.

---

## Install

From the gil repo root:

```bash
pip install -e python/gil_swebench
```

Optional extras:

| Extra      | Purpose                                                            |
|------------|--------------------------------------------------------------------|
| `datasets` | Pull `princeton-nlp/SWE-bench_Lite` from HuggingFace               |
| `dev`      | Test runner (`pytest`, `pytest-asyncio`)                           |

```bash
pip install -e 'python/gil_swebench[datasets,dev]'
```

The `datasets` extra is **opt-in** so installing the package never reaches
the network; the bundled `fixtures/smoke.jsonl` covers offline smoke runs.

### Compile the gRPC stubs

The generated `*_pb2.py` / `*_pb2_grpc.py` are not committed:

```bash
python -m gil_swebench.compile_protos
```

(Runs against `<gil-repo>/proto/gil/v1/*.proto`.)

---

## Quickstart

### 1. Single bundled smoke task (no network, no gild)

```bash
gil-swebench run --instance-id smoke__addition-1 --dry-run
```

Renders the rendered spec dict so you can sanity-check goal / verifier /
budget without spending tokens.

### 2. Single live SWE-bench-Lite task

Make sure `gild` is running locally and pick an instance id from the dataset:

```bash
gild &

gil-swebench run \
    --instance-id django__django-12345 \
    --dataset swebench-lite \
    --provider anthropic \
    --model claude-haiku-4
```

Output (truncated):

```
results dir: results/run-20260427-103045-a1b2c3
[1/1] VERIFY-OK   instance=django__django-12345          status=done           iters=11 tokens=287340 cost=$1.4123 elapsed= 412.8s patch_bytes=2418

--- scoring ---
--- summary ---
  instances:       1
  resolved:        1/1 (100.0%)
  ...
```

### 3. 10-task batch

```bash
gil-swebench batch \
    --num 10 \
    --dataset swebench-lite \
    --provider anthropic \
    --model claude-haiku-4
```

Streams `results/<run-id>/instances.jsonl` so a crash mid-batch still leaves
usable data. After the loop, scoring is invoked automatically and writes
`summary.json` + `results.csv`.

### 4. Re-score an existing results dir

If your run crashed mid-scoring or you want to swap in a different test
runner, re-score without rerunning the agent:

```bash
gil-swebench score results/run-20260427-103045-a1b2c3 \
    --dataset swebench-lite --rescore
```

---

## Provider configuration

Any gil-supported provider works. For non-trivial tasks we strongly recommend
**Anthropic** (Haiku for cost, Sonnet for quality):

| Provider      | Notes                                                          |
|---------------|----------------------------------------------------------------|
| `anthropic`   | Recommended. Set `ANTHROPIC_API_KEY` for gild.                 |
| `openai`      | Works; pricing comparable on capable models.                   |
| `vllm`        | Local inference; only worthwhile with a strong code model.     |
| `mock`        | gil's mock provider; will not solve real SWE-bench tasks.      |

**Cost estimate (claude-haiku-4 assumption):**

| Per task             |                                |
|----------------------|--------------------------------|
| Token budget         | 500k                           |
| Haiku token price    | ~$1/M (in) + ~$5/M (out, mixed)|
| Realistic cost/task  | $1–3                           |
| **100 tasks**        | **~$150–300**                  |
| **300 tasks (Lite)** | **~$450–900**                  |

Sonnet-class models will cost roughly 5× more per task and (probably) score
meaningfully higher; the current pipeline is bottlenecked by gil's autonomy
loop quality more than by model choice, so start with Haiku.

---

## What gets recorded

Each run writes:

```
results/<run-id>/
├── instances.jsonl              one line per task -- raw runner output
├── instances.scored.jsonl       same rows, with `resolved` populated
├── summary.json                 aggregate pass@1, cost, time
└── results.csv                  spreadsheet-friendly view
```

Each JSONL row contains:

| Field                            | Meaning                                            |
|----------------------------------|----------------------------------------------------|
| `instance_id`, `repo`, `base_commit` | from the SWE-bench record                       |
| `status`                         | `done` / `max_iterations` / `error` / `stopped`    |
| `iterations`, `tokens`, `cost_usd`, `wall_clock_seconds` | gil run totals      |
| `model_patch`                    | `git diff <base_commit>` after the run             |
| `fail_to_pass_verifier_passed`   | whether gil's in-loop verifier returned 0          |
| `resolved`                       | true iff FAIL_TO_PASS+PASS_TO_PASS both clean      |
| `resolved_reason`                | short string (e.g. `"FAIL_TO_PASS still failing"`) |

---

## How scoring works

gil's verifier loop only runs `FAIL_TO_PASS` (PASS_TO_PASS would be far too
slow on big repos). For the headline `pass@1` we re-do scoring offline:

1. Fresh clone of `task.repo` at `task.base_commit`.
2. `git apply` the agent's `model_patch`.
3. `git apply` the official `task.test_patch` (so the new tests are present).
4. Run `FAIL_TO_PASS` via `pytest` — every node-id must pass.
5. Run `PASS_TO_PASS` via `pytest` — every node-id must still pass.
6. `resolved = True` iff both succeed; otherwise record a short reason.

Patches that don't apply (whitespace, trailing context, wrong path) are
counted as **unresolved with `"<patch> did not apply"`** — same as if the
agent had not produced a patch.

---

## First actual benchmark run — procedure

Phase 23 Track C delivers the harness only; we have not run a real benchmark
yet. When you're ready:

1. **Pre-flight checklist**
   - `gild` running and reachable (UDS or TCP).
   - `ANTHROPIC_API_KEY` (or your provider's key) exported in gild's env.
   - Network access for `git clone github.com/...` per instance.
   - At least 5 GB free under `~/.gil/swebench-workspaces` (clones are full-depth).
   - `python -m pytest --version` works in your active venv (scoring needs it).

2. **Smoke test (no money spent)**
   ```bash
   gil-swebench run --instance-id smoke__addition-1 --dry-run
   ```
   should print a spec dict. If it does, gil-swebench is wired up.

3. **Single live instance (~$1, ~5 min)**
   ```bash
   gil-swebench run \
       --instance-id <pick-one-from-Lite> \
       --dataset swebench-lite \
       --provider anthropic --model claude-haiku-4 \
       --max-tokens 500000 --max-cost-usd 5
   ```
   Inspect `results/<run-id>/instances.jsonl`. If you see a `model_patch` of
   non-trivial size and `status=done`, you're good.

4. **10-task batch (~30 min, ~$10–30)**
   ```bash
   gil-swebench batch --num 10 --dataset swebench-lite \
       --provider anthropic --model claude-haiku-4
   ```
   Expect 0–20% resolved on the first run. That's the starting line.

5. **100-task slice (~5 hours, ~$150–300)**
   Only worth doing once the 10-task slice gives a non-zero pass@1.

6. **Full SWE-bench-Lite (300 tasks, ~15 hours, ~$450–900)**
   Reserve for when you've made meaningful pipeline improvements and want a
   real headline number. Use `--results-dir` and a fresh `--run-id` so older
   runs are preserved for comparison.

---

## Architecture notes

* **gRPC over UDS** — `GilGrpcClient` defaults to `unix:~/.gil/gild.sock`. TCP
  is supported via `--target host:port` + optional `--bearer-token`.
* **Interview-skipping** — `freeze_spec()` drives the `InterviewService.Start
  → Confirm` flow without a chat back-and-forth. The verifier comes from the
  task's `FAIL_TO_PASS` list.
* **Two-tier scoring** — gil's loop runs a fast FAIL_TO_PASS check; the
  offline scorer does the full FAIL_TO_PASS + PASS_TO_PASS dance on a clean
  clone, so PASS_TO_PASS regressions are caught.
* **Per-instance workspaces** — fresh tmp dir per task, deleted after scoring
  unless `--keep-workspaces` is set.
* **Stream-write JSONL** — a crash mid-batch leaves a usable file; rerun
  `gil-swebench score` to aggregate what you've got.
* **Dataset opt-in** — `datasets` is an extra; the package itself never
  touches HuggingFace at install time.

---

## Testing

```bash
pip install -e 'python/gil_swebench[dev]'
pytest python/gil_swebench/tests -v
```

The smoke tests use `unittest.mock` for the gRPC client and pre-resolved
JSONL rows for the scorer, so they don't need a running `gild` or network.

---

## See also

* [gil top-level README](../../README.md)
* [`gil_atropos`](../gil_atropos/README.md) — Atropos RL adapter (sister package)
* [SWE-bench](https://www.swebench.com/) — the upstream benchmark
* [SWE-bench-Lite dataset](https://huggingface.co/datasets/princeton-nlp/SWE-bench_Lite)
* [SWE-bench-Verified dataset](https://huggingface.co/datasets/princeton-nlp/SWE-bench_Verified)
