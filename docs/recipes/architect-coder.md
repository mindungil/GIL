# Architect/coder split — pair a strong planner with a cheap editor

gil's runner classifies each iteration as `planner`, `editor`, or `main`
based on the previous turn's tool calls (Phase 19 Track C). When you
wire different models to those roles in `spec.yaml`, the loop
automatically routes:

- **planner**: the very first turn AND any turn where the agent calls
  the `plan` tool. This is "thinking" time — write specs, decompose,
  reason. Pair with your strongest model.
- **editor**: turns where the agent only calls execution tools
  (`bash`, `edit`, `write_file`, `apply_patch`, `read_file`,
  `memory_update`). Mechanical edits — pair with your cheapest fast
  model.
- **main**: everything else (mixed turns, no-tool turns,
  non-execution tool calls like `subagent` or `web_search`). Falls
  through to your default — usually a good generalist.

The split is the same architectural pattern as
[aider's architect/editor coder pair](https://aider.chat/docs/usage/modes.html).

## Spec config

```yaml
# spec.yaml
models:
  main:
    provider: openai
    modelId: gpt-4o
  planner:
    provider: anthropic
    modelId: claude-opus-4-7    # strong reasoning for planning
  editor:
    provider: vllm
    modelId: qwen3.6-27b        # cheap+fast for grunt work
```

This routes:

- Turn 1 (planner) → claude-opus
- Any turn after `plan` tool was called (planner) → claude-opus
- Turns where the previous response only called bash/edit/write_file
  (editor) → qwen3.6-27b
- Anything else (main) → gpt-4o

## How the routing decides each turn

The runner runs `classifyTurn(iter, lastResponse)` BEFORE every
provider request:

1. `iter == 0` → `planner` (always plan before doing).
2. `lastResponse.tool_calls` includes `plan` → `planner` (still
   iterating on the plan).
3. ALL of `lastResponse.tool_calls` are in
   `{bash, edit, write_file, apply_patch, read_file, memory_update}`
   AND there's at least one → `editor`.
4. Otherwise → `main`.

A `model_switched` event fires on every transition, so a `gil events`
tail shows the breadcrumb:

```
model_switched  from="" to=planner reason=first_turn        iter=1  model=claude-opus-4-7
model_switched  from=planner to=editor reason=tool_heavy   iter=4  model=qwen3.6-27b
model_switched  from=editor to=main reason=ambiguous_turn  iter=7  model=gpt-4o
```

## Provider sharing

When two roles point at the same backend (e.g., both `planner` and
`editor` use Anthropic), the runner shares one Provider instance —
same connection pool, same auth — and only the per-call model id
changes. This means specs like

```yaml
models:
  main:    {provider: anthropic, modelId: claude-haiku-4-5}
  planner: {provider: anthropic, modelId: claude-opus-4-7}
```

create exactly one Anthropic client and route per request.

## Per-role cost reporting

`gil cost <session-id>` prints a per-role breakdown when ≥2 roles
fired during a run:

```
Session: 01HK...
Provider: anthropic
Model:    claude-opus-4-7

Tokens:
  input         42,113
  output        8,201

Cost (USD):    $0.7531  (estimate; public list prices)

By role:
  planner  claude-opus-4-7   3 call(s)  18,200 tokens  $0.3120
  editor   qwen3.6-27b      11 call(s)  20,114 tokens  $0.0010
  main     gpt-4o            1 call(s)   4,000 tokens  $0.0250
```

The JSON output (`gil cost --json`) carries `by_role` as an array of
`{role, model, calls, input_tokens, output_tokens, cost_usd, model_known}`.

## Backwards compatibility

Specs that only set `models.main` (the legacy shape) keep behaving
exactly as before. The runner's `pickProvider`/`pickModel` helpers fall
back to the single configured provider for any role left unset, so the
split is fully opt-in and pure additive.

## When NOT to use the split

- **Toy runs / smoke tests**: routing overhead and the extra
  `model_switched` events are pure noise when there's only one model
  available anyway.
- **Cost-uncapped, latency-sensitive runs**: provider-switch latency
  on cold connections can dominate. If the run is ≤3 turns, just pick
  one model.
- **Runs with no `plan` tool usage AND no execution-tool turns**:
  every turn would hit `main` anyway.
