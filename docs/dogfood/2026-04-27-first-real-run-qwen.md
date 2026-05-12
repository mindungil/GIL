# First Real Run — qwen3.6-27b via local vLLM endpoint

**Date:** 2026-04-27
**Endpoint:** user-provided OpenAI-compatible endpoint (vLLM-served)
**Model:** `qwen3.6-27b`
**Adapter:** `core/provider.OpenAI` (Phase 15 Track A — pure stdlib)
**Driver:** `tests/dogfood/qwen_smoke/main.go`

This is the first time gil has talked to a non-Anthropic model. The endpoint
is a vLLM deployment provided by the user; concrete URL and key live only in
`~/.config/gil/auth.json` (0600) and are deliberately omitted here.

## What was exercised

The smoke binary makes three calls in order, using only the OpenAI adapter
(no daemon round-trip, no agent loop):

1. **Plain text completion** — single user message, no tools, system prompt.
2. **Tool-use** — single tool definition (`calculate(expr)`), user asks to
   compute `17 * 23` using it.
3. **Tool-result follow-up** — feeds a synthetic `391` back to the model and
   asks it to produce a final answer, exercising the `role: "tool"` path.

A separate ad-hoc script also exercised:

4. **Constrained text** — five-word reply with `max_tokens=512`.
5. **Parallel tool calls** — two `calculate` calls in a single turn.

## Results

| Step | Outcome | Latency | Tokens (in / out) |
|------|---------|---------|-------------------|
| 1. text completion (max_tokens=64) | reached `max_tokens` (model emitted "thinking" preamble) | 1.39s | 24 / 64 |
| 2. tool-use round trip | one well-formed `calculate` call, `tool_use` stop | 1.65s | 312 / 86 |
| 3. tool-result follow-up | clean `"The result of 17 * 23 is 391."` | 1.04s | 363 / 54 |
| 4. constrained text (max_tokens=512) | exact five words after a leading newline | 4.03s | 34 / 224 |
| 5. parallel tool calls | two `calculate` calls in one turn, both well-formed | 3.69s | 292 / 218 |

### What worked

- **Authorization header** — bearer key plumbed through correctly; first call
  with a wrong endpoint pretty-printed the upstream error so the user could
  see the wire failure.
- **System prompt** — prepended as a leading `role: "system"` message; the
  model honoured it (see step 4).
- **Tool definitions** — `tools: [{type:"function", function:{...}}]` round-
  tripped fine; qwen produced **valid JSON arguments** wrapped as a string
  per OpenAI spec.
- **Tool result feedback** — `role: "tool"` with `tool_call_id` was accepted
  and the model produced a coherent follow-up.
- **Parallel tool calls** — qwen emitted two tool calls in one assistant
  turn, which means the agent loop's parallel-tool path will exercise.
- **Stop reason mapping** — `stop`/`tool_calls`/`length` all mapped to gil's
  `end_turn`/`tool_use`/`max_tokens` vocabulary as expected.
- **Token counts** — `usage.prompt_tokens` and `usage.completion_tokens` are
  populated and look reasonable.

### What's worth flagging

- **Verbose preambles at low max_tokens.** With `max_tokens=64` and a casual
  prompt, qwen emitted a numbered "thinking" outline rather than the answer,
  and finished by hitting the cap. The agent loop already plans for
  `max_tokens` as a normal stop reason, so this isn't a bug — but anyone
  benchmarking response quality against Anthropic should bump
  `max_tokens` to ≥256 to give qwen room to think aloud.
- **Leading whitespace.** Qwen tends to start its reply with `\n\n`. Not a
  bug, but downstream renderers that don't trim leading newlines will look
  off. Anthropic responses don't do this.
- **No `cached_read_per_m`.** The cost catalog entry is `$0/$0` (local
  model); cache-aware billing doesn't apply.
- **Latency.** ~1-4s per turn from this client. That's the round trip,
  not just generation, so it includes network. For comparison the Anthropic
  Haiku live smoke typically runs in ~700ms from the same machine.

### Bugs/issues found in gil

None. The adapter, factory wiring, errwrap mappings, and credstore lookup
all worked on the first try once `gil auth login vllm` was run. The
unit tests caught the OpenAI-vs-Anthropic content-string-vs-null contract
before this run, so no debugging was needed at runtime.

## How to repro

```bash
# 1. store the credential (not committed; lives in ~/.config/gil/auth.json)
gil auth login vllm --base-url <url> --api-key <key>

# 2. build & run the smoke
make build
go run ./tests/dogfood/qwen_smoke
```

Set `GIL_PROVIDER=openai` or `GIL_MODEL=<other>` to point the smoke at a
different stored credential or a different served model.

## What this unblocks

- Phase 15 Track B can lean on the OpenAI adapter for OpenRouter (any model
  that fronts an OpenAI-compatible endpoint).
- A real interview-and-run exercise against qwen is now a question of
  spinning up gild with `--provider vllm` and the existing flow — no
  further adapter work required.
- Cost estimation on local models becomes "free by definition" via the
  `qwen3.6-27b` catalog entry, so dogfood runs against this endpoint won't
  pad the user's reported usage.
