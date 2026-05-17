# Phase 33 — separated reasoning surfaced in chat

> Severity 2 line item from `roadmap-post-v0.2.0.md`:
> "Reasoning split. Models that emit `<thinking>` / extended thinking
> get mashed into the chat transcript as plain text. Need typed
> `AssistantOutput` proto with reasoning vs. visible-response
> branches. Cross-cuts proto + provider adapter + renderer; bigger
> than it looks because the change touches every consumer of
> `provider.Response.Text`."

## Why now

`provider.Response.Reasoning` has existed since the provider rewrite,
populated by the vLLM adapter (qwen3.6-27b's reasoning field, DeepSeek
`reasoning_content`, Anthropic extended-thinking blocks). But no
consumer in `server/` or `cli/` reads the field — `session_prompt.go`
only emits `resp.Text`, so every reasoning trace from the eval-loop
provider was silently discarded. The user could not see what the model
was thinking; the agent's own monologue was invisible context for
debugging long-running runs.

The cross-cutting fear in the roadmap was about `provider.Response.Text`
consumers needing audit. After the audit, only two callers actually
needed plumbing changes: the chat-mode `session_prompt.go` dispatcher
and the CLI repl `grpc_client.go` translator. Run-mode (`core/runner`)
hands `Text` to the agent loop's plan-store and verify-loop — its
contract is "what the model said as its answer", which is exactly what
`Text` already means. Reasoning is for the surface, not the loop.

So scope is narrower than the roadmap implied: add one proto variant,
two consumer hooks, one render method. No proto field rename, no
existing-caller refactor.

## Design

1. **proto** (`gil/v1/session.proto`):
   - New `ReasoningDelta { string content = 1; }`.
   - New `Part.body` oneof variant `ReasoningDelta reasoning = 7;`.
   - Comment on `Part` explains the new variant covers vLLM
     `reasoning`, DeepSeek `reasoning_content`, Anthropic extended-
     thinking blocks.

2. **server/internal/service/session_prompt.go**:
   - When `resp.Reasoning != ""`, emit a `Part_Reasoning` BEFORE
     `Part_Text` so the stream order matches the user's expected
     read order (think → answer).
   - Do NOT append reasoning to `chatHistory` — the model regenerates
     fresh reasoning each turn, so replaying old reasoning wastes
     tokens and confuses next-turn context. Reasoning is a one-way
     stream from model → user.

3. **cli/internal/chat/repl/grpc_client.go**:
   - New `case *gilv1.Part_Reasoning` in the part-dispatch switch.
   - Emits `Message{Kind: "reasoning", Text: ...}` on the unified
     `msgCh`. Wire order preserved by the iter14a-era ordered queue.

4. **cli/internal/chat/repl/loop.go**:
   - New `case "reasoning"` in the message dispatch switch.
   - Routes through the same `sanitizeAssistantChunk` filter as text
     (a hostile file echoed into the model's reasoning could still
     ship ESC sequences — same threat as iter91a/iter118a).
   - Calls `Renderer.AssistantReasoning(chunk)`.

5. **cli/internal/chat/render/renderer.go**:
   - New `AssistantReasoning(chunk string)` method on the `Renderer`
     interface. Doc reads: "implementations may dim, indent, or hide
     reasoning behind a toggle; the contract is 'do not let it be
     mistaken for the actual answer'."

6. **cli/internal/chat/render/stdout.go**:
   - Default impl prefixes every non-empty line with a dimmed
     `[think]` marker so a scan of the transcript visually separates
     the model's monologue from its reply.

7. **cli/internal/chat/render/mock.go** + `renderer_test.go`:
   - Mock records `AssistantReasoning` calls so loop tests can assert
     dispatch ordering.
   - `nopRenderer` stub adopts the new method so the compile-time
     interface check still passes.

## Tests

- `TestStdout_AssistantReasoning_PrefixesEveryLine` — multi-line input
  gets `[think]` on each line, not just the first.
- `TestStdout_AssistantReasoning_EmptyIsNoop` — empty chunk doesn't
  flush anything to the buffer.
- `TestLoop_Reasoning_DispatchesToAssistantReasoning` — a
  `Kind: "reasoning"` message ends up on `AssistantReasoning`, AND it
  renders before a subsequent `text` message in the same turn.
- `TestLoop_Reasoning_StripsEscSequences` — ESC / BEL in a reasoning
  chunk get stripped before reaching the renderer (same defense as
  text path).

## Non-goals

- **Reasoning persistence.** The model regenerates fresh reasoning;
  history doesn't need it.
- **Run-mode reasoning surfacing.** Run-mode uses agent reasoning
  internally (plan-store, verify-loop) but doesn't surface it to a
  user. A run-mode toggle could come later.
- **Toggle to hide reasoning.** Default-on; a `--hide-reasoning` CLI
  flag could land as a tiny followup if users find the volume noisy
  against qwen3.6-27b.
- **Anthropic extended-thinking native API support.** The Anthropic
  adapter is what would need work; vLLM + DeepSeek already populate
  `Response.Reasoning`. Anthropic follow-up.

## Followups

- Toggle (`--hide-reasoning`, `/no-think` keybind).
- Anthropic extended-thinking → `Response.Reasoning` plumbing.
- Persistent token counters split by reasoning vs answer (useful for
  cost attribution on long runs).
- Reasoning surfaced inside `export_session` transcripts.
