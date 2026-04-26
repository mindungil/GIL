# gil-atropos

Atropos RL environment adapter for [gil](../../README.md), the autonomous coding harness.

`gil-atropos` exposes gil as a HuggingFace/Atropos-style RL environment so that you can:

* Run **ad-hoc evals** of any model + harness combo against a coding-task dataset (no Atropos training stack required).
* Drop gil into a **full Atropos training run** as a `HermesAgentBaseEnv` subclass, scoring rollouts by verifier outcome.

Each rollout is one full gil session:

```
create_session(working_dir)
    -> freeze_spec(spec_dict)        # bypass interactive interview, scripted spec
    -> run_session(model, ...)       # autonomous loop in gil
    -> compute_reward(verifier)      # 1.0 if all checks passed; pass_ratio for partial credit
```

---

## Install

From the gil repo root:

```bash
pip install -e python/gil_atropos
```

This pulls in the runtime deps (`grpcio`, `grpcio-tools`, `protobuf`, `pydantic`).

### Optional extras

| Extra      | Use case                                                                 |
|------------|--------------------------------------------------------------------------|
| `atropos`  | Full Atropos / hermes-agent training integration                         |
| `datasets` | Load HuggingFace datasets like `openai_humaneval` or `mbpp`              |
| `dev`      | Test runner (`pytest`, `pytest-asyncio`)                                 |

```bash
pip install -e 'python/gil_atropos[datasets,dev]'
```

`hermes-agent` is **not on PyPI**; install it from git:

```bash
pip install "hermes-agent @ git+https://github.com/NousResearch/hermes-agent.git"
# (or your fork)
```

Eval-only mode works fine without `hermes-agent` -- `GilCodingEnv` just doesn't inherit from `HermesAgentBaseEnv` in that case.

---

## Compile the gRPC stubs

The generated `*_pb2.py` / `*_pb2_grpc.py` files are **not committed**. Generate them once after install:

```bash
python -m gil_atropos.compile_protos
```

This calls `grpc_tools.protoc` against `proto/gil/v1/*.proto` and writes the stubs into `python/gil_atropos/proto/`. Re-run after editing the .proto files.

A Makefile target is wired into the gil top-level:

```bash
make python-protos
```

---

## Quick eval

Make sure `gild` is running locally (default UDS at `~/.gil/gild.sock`):

```bash
gild &
```

Then run one rollout against the bundled fibonacci task:

```bash
gil-atropos-eval --num 1
```

Sample output:

```
[1/1] PASS task=fibonacci          reward=1.00 pass_ratio=1.00 iters=4 tokens=2103 cost=$0.0123 elapsed=18.4s status=done

--- summary ---
  rollouts:     1
  mean reward:  1.000
  full passes:  1/1
  total tokens: 2103
  total cost:   $0.0123
```

Useful flags:

```bash
# Pin the model (and provider)
gil-atropos-eval --num 5 --model claude-sonnet-4 --provider anthropic

# Use HumanEval (falls back to bundled if `datasets` not installed)
gil-atropos-eval --dataset openai_humaneval --num 10

# Talk to a remote gild over TCP with OIDC bearer auth
gil-atropos-eval --target gild.example.com:7777 --bearer-token "$GIL_TOKEN"

# Machine-readable output for CI
gil-atropos-eval --num 3 --json | jq '.[] | {task_id, reward}'
```

---

## Bundled tasks

Five tiny self-contained coding tasks ship with the package as a no-network fallback:

| `task_id`         | what                                                |
|-------------------|-----------------------------------------------------|
| `fibonacci`       | `fib(n)` returning the n-th Fibonacci number        |
| `reverse_string`  | `reverse(s)` returning the reverse of a string      |
| `is_palindrome`   | `is_palindrome(s)` ignoring case and punctuation    |
| `fizzbuzz`        | `fizzbuzz(n)` returning the canonical sequence      |
| `sum_csv_column`  | `sum_column(path, col)` summing a numeric CSV col   |

Each task ships with a self-contained `pytest` verifier (no external fixtures needed).

---

## Atropos integration

When `hermes-agent` is installed, `GilCodingEnv` becomes a full `HermesAgentBaseEnv` subclass.

### Register and run training

```python
# my_train.py
from gil_atropos import GilCodingEnv

if __name__ == "__main__":
    GilCodingEnv.cli()  # provided by HermesAgentBaseEnv
```

Then use the standard hermes-agent CLI modes:

```bash
# SERVE -- full training loop, connects to Atropos API
python my_train.py serve --openai.base_url http://localhost:8000/v1

# PROCESS -- offline data generation
python my_train.py process \
    --env.total_steps 10 \
    --env.group_size 1 \
    --env.use_wandb false \
    --env.data_path_to_save_groups gil_rollouts.jsonl \
    --openai.base_url <YOUR_BASE_URL> \
    --openai.model_name <YOUR_MODEL> \
    --openai.server_type openai \
    --openai.health_check false

# EVALUATE -- standalone eval
python my_train.py evaluate \
    --env.eval_size 10 \
    --env.data_dir_to_save_evals /tmp/gil_eval \
    --openai.base_url <YOUR_BASE_URL> \
    --openai.model_name <YOUR_MODEL>
```

If your Atropos installation supports environment registration by name:

```bash
atropos register gil_coding --module gil_atropos --class GilCodingEnv
atropos train gil_coding --model <YOUR_MODEL>
```

(Exact CLI depends on your Atropos version; consult `atropos --help`.)

---

## Reward function

Default reward shape is verifier-driven:

| Outcome                        | Reward         |
|--------------------------------|----------------|
| All verifier checks pass       | `1.0`          |
| All checks fail                | `0.0`          |
| Mixed                          | `passed / total` |
| Run errored before any check   | `0.0`          |

Override `compute_reward` to add bonuses for efficiency, token usage, etc.

---

## Architecture notes

* **gRPC over UDS**: `GilGrpcClient` defaults to `unix:~/.gil/gild.sock` so no port allocation is needed. TCP is supported via `--target host:port`.
* **Interview-skipping path**: `GilCodingEnv.format_prompt` returns a complete `spec_dict` and `freeze_spec()` drives the `InterviewService.Start -> Confirm` flow without a chat back-and-forth.
* **Per-rollout workspaces**: a fresh tmp dir is allocated for every rollout under `--workspace-root` (default: a `mkdtemp()`), then deleted after scoring unless `--keep-workspaces` is set.
* **Synchronous gRPC + async wrapper**: `evaluate()` is `async` (Atropos compatibility), but the actual rollout runs synchronously inside `asyncio.to_thread(...)`.

---

## Testing

```bash
pip install -e 'python/gil_atropos[dev]'
pytest python/gil_atropos/tests -v
```

The smoke tests use `unittest.mock` to fake the gRPC client, so they don't require a running `gild`.

---

## See also

* gil top-level README: `../../README.md`
* gil proto definitions: `../../proto/gil/v1/`
* Phase 10 plan (Track F): `../../docs/plans/phase-10-cloud-real-vscode-packaging.md`
* Hermes Atropos environments skill: `/home/ubuntu/research/hermes-agent/optional-skills/mlops/hermes-atropos-environments/SKILL.md`
