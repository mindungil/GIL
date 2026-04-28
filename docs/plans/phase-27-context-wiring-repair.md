# Phase 27 — Context Wiring Repair

**Status**: design approved (2026-04-28), implementation pending
**Predecessor**: Phase 25 (UX elevation)
**Successor**: Phase 26 (chat-only surface) — depends on P27 outputs

## 1. Goal

Wire up gil's context-management infrastructure that exists in code but is not invoked from the production run loop. The audit (2026-04-28) found that `core/compact/Compactor`, `core/compact/MarkCacheBreakpoints`, and per-model context capacity are all implemented but never called from `server/internal/service/run.go` or `core/runner/runner.go` mainline. Without this phase, gil's `[run · iter X/100 · $Y]` strip (P26) would surface a status line about a context loop that is silently bloating, miscaching, and crashing on long missions — repeating the P25 mistake of polishing surface over un-wired internals.

The success criterion is a 100-iteration mission running to completion without provider 4xxx context-overflow errors, with at least one compaction event observed in the event log and an Anthropic cache hit rate > 0 on the last-3-messages window.

## 2. Non-Goals

- **No new providers, models, or sandboxes**. Pure plumbing repair.
- **No tokenizer V2** (Anthropic / Google `count_tokens` API integration). Deferred to Phase 28.
- **No memory-bank GC**. Bank growth is a separate concern, addressed in a future phase.
- **No multi-user, cloud VM, or remote workspace work**. Phase 9+.
- **No chat surface, REPL, slash commands**. P26 territory.
- **No compaction policy changes**. Existing Head/Middle/Tail with default head=2 / tail=6 / 95% threshold stays.

## 3. Architecture

Six surgical fixes, all server-side / runner-side. No proto changes. No new dependencies in V1.

### 3.1 Fix 1 — Compactor instantiation in production

`core/compact/compactor.go` exports `NewCompactor(provider, model, opts)` (verify exact signature; rename in plan if different). Currently the field `AgentLoop.Compactor` is type-checked but never assigned outside unit tests. Add a factory in `core/runner/factory.go` (new file) that takes a resolved spec + provider registry and returns an initialized `*compact.Compactor`. Wire the factory into `server/internal/service/run.go` at the `NewAgentLoop(...)` call site so every production run gets a live Compactor.

The summarization model is a per-spec choice — if `spec.Models.Weak` is set, use it (cost-efficient); otherwise fall back to `spec.Models.Main`. This reuses the architect/coder split conventions from Phase 19.

### 3.2 Fix 2 — Cache breakpoint marker mainline call

`core/compact/cache.go` exports `MarkCacheBreakpoints(messages, opts)` which annotates the system block + the last 3 messages with `cache_control: ephemeral` per Anthropic's documented strategy. The Anthropic provider adapter (`core/provider/anthropic.go:106-107`) honors the marker but only sees what the runner sends. The runner currently sends messages without the marker.

Insert one call site in `core/runner/runner.go` immediately before `provider.Complete(ctx, req)`:

```go
if req.Provider == "anthropic" {
    req.Messages = compact.MarkCacheBreakpoints(req.Messages, compact.CacheOpts{Recent: 3})
}
```

This is provider-conditional because OpenAI/Google don't honor the markers (they're no-ops there but unnecessary). The exact insertion point is the place where `req` is constructed; pull that into a helper if it isn't one already.

### 3.3 Fix 3 — Per-model context window registry

Today `AgentLoop.MaxContextTokens` is a single integer (default 200_000). Different models have different capacities and gil already has a `Models` map per role. Add `core/provider/capacity.go` with a static lookup table:

```go
var modelContextTokens = map[string]int64{
    "claude-opus-4-7":           1_000_000,
    "claude-opus-4-7[1m]":       1_000_000,
    "claude-sonnet-4-6":           200_000,
    "claude-haiku-4-5-20251001":   200_000,
    "gpt-4o":                      128_000,
    "gpt-4o-mini":                 128_000,
    "gpt-5":                       400_000,
    "gemini-2-pro":              1_000_000,
    "gemini-1.5-flash":          1_000_000,
    "ollama:llama3:8b":              8_192,
    "ollama:qwen3-coder:32b":      32_768,
}

func ContextTokens(model string) int64 {
    if v, ok := modelContextTokens[model]; ok {
        return v
    }
    return 200_000 // conservative default
}
```

Compaction trigger uses `ContextTokens(model)` instead of a hardcoded constant. When the architect/coder split routes a turn to a smaller model, the threshold adapts.

### 3.4 Fix 4 — Per-role context budget

Phase 19 introduced architect/coder/main role routing via `Providers` and `Models` maps in `AgentLoop`. Compaction trigger today is global. Make it role-aware: when the next turn is routed to model X, check usage against `ContextTokens(X)`, not against a global value. Implementation: at the point where `classifyTurn(...)` selects a role, record the chosen model into the request context, then use that model when computing the threshold.

This prevents the case where a 200k-context Sonnet check passes but the next-turn 8k Ollama call would 4xxx.

### 3.5 Fix 5 — Grace call on budget exhaustion (Hermes pattern)

Today `runner.go:622` sets status `"budget_exhausted"` and stops cold the moment usage exceeds budget. The Hermes pattern (used in long-horizon reasoning research) instead allows **one final turn** with a shrunken instruction asking the model to summarize what's done, what's pending, and what the next agent should pick up. This makes a partially-complete mission resumable.

Implementation: when `totalTokens + reserve > budgetMaxTokens` for the first time, insert a synthetic user message:

```
[BUDGET WRAP-UP] You are about to hit the budget cap. This is your final turn.
Stop work. Output: (1) what got done, (2) what's pending, (3) which file/state
the next iteration should resume from. Do not call tools.
```

Then call provider once more, append the response, set status `"budget_exhausted_with_handoff"`, and stop. The grace call is gated by a `--no-grace` spec flag in case the user prefers hard cutoff.

### 3.6 Fix 6 — Provider-aware char-multiplier (V1 token estimate refinement)

Current `estimateMessagesTokens` uses 4 characters per token uniformly. For V1 keep the heuristic but make the multiplier per-provider:

```go
var providerCharsPerToken = map[string]float64{
    "anthropic": 3.5,  // code-heavy missions, dense tokens
    "openai":    4.0,
    "google":    3.8,
    "ollama":    4.5,  // wider variance, conservative
}
```

This is a one-line change in two files (runner.go + compactor.go). It's not a real tokenizer (V1.5/V2 work), but moves accuracy from ~70% to ~85% with no new deps.

## 4. Roadmap (V1 / V1.5 / V2)

**V1 (this phase, ~3-5 days)**: Fixes 1-6 above. Pure plumbing, no new deps.

**V1.5 (Phase 27.5, ~1-2 days, separate)**: OpenAI tiktoken integration via `pkoukk/tiktoken-go` (offline, fast, MIT-licensed). Replaces the char-multiplier path for OpenAI requests. Anthropic / Google / Ollama keep multipliers.

**V2 (Phase 28, separate)**: Anthropic `count_tokens` API integration with response caching (per session, per message-prefix hash). Same for Google. Network-bound, complexity warrants its own design phase.

These three phases are committed as a sequence, not "TBD" — each ships independently, accuracy increases monotonically.

## 5. Migration Plan

### 5.1 Files affected

**New**:
- `core/runner/factory.go` — Compactor factory + helper assembly
- `core/provider/capacity.go` — per-model context table + lookup
- `core/runner/factory_test.go`
- `core/provider/capacity_test.go`

**Modified**:
- `core/runner/runner.go` — cache marker call site, per-role/per-model context check, grace call on budget exhaust, char-multiplier indirection (use new helper)
- `server/internal/service/run.go` — call factory, assign `loop.Compactor`
- `core/compact/compactor.go` — char-multiplier indirection (use new helper)

**Untouched**:
- `proto/`, `sdk/` — no proto changes
- `cli/`, `tui/` — no client changes
- `core/compact/cache.go`, `core/compact/compactor.go` (logic) — already correct, just need to be called
- `core/memory/` — bank GC is a separate phase

### 5.2 Estimated diff size

~150 lines new, ~80 lines modified. Mostly factory glue + table data + one-liner call insertions. Largest single block is Fix 5 (grace call) at ~40 lines.

## 6. Testing Strategy

### 6.1 Unit tests per fix

- **Fix 1**: factory test — given a spec with weak model + main model, assert factory returns a Compactor configured with the weak model.
- **Fix 2**: cache marker test — capture mock Anthropic provider, assert the request received by the provider has `cache_control` markers on system + last 3 messages.
- **Fix 3**: capacity table test — known model strings return expected capacities; unknown returns 200k default.
- **Fix 4**: per-role test — set up spec with main=opus + editor=ollama:llama3:8b. Drive a turn classified as editor. Assert compaction threshold uses 8k, not 200k.
- **Fix 5**: grace call test — force budget exhaust on iter 5, assert one extra provider call with the wrap-up prompt, assert status `"budget_exhausted_with_handoff"`.
- **Fix 6**: char-multiplier test — same input message string, four providers. Assert estimated tokens differ per provider.

### 6.2 Integration test

`tests/integration/p27_context_wiring_test.go`:
- Spin up daemon with mock provider that records every request.
- Send a 50-iter mission with synthetically large tool outputs (push usage past 95% threshold).
- Assert: at least one Compactor invocation in the event log; messages after compaction are shorter than before; cache markers present in Anthropic-flagged requests.

### 6.3 Dogfood E2E

Run a real Anthropic-backed mission with `--max-iter 100`. Verify:
- Mission completes (no 4xxx provider errors).
- At least one compaction event in the run log.
- Anthropic API response headers / billing report shows cache hit rate > 0 on the run.

## 7. Risks & Mitigations

- **Compactor summarization changes context semantics**. The summary replaces real messages with a paragraph; the agent's next turn sees a different shape. Mitigation: head/tail preservation already protects the most recent context (default 6 messages). Test with synthetic missions to confirm no regression.
- **Cache markers may cause Anthropic to reject requests if mis-applied**. The cache_control field has format constraints. Mitigation: `MarkCacheBreakpoints` already passes existing unit tests; integration test will catch any 400 from Anthropic immediately.
- **Per-model registry will be incomplete**. New models ship faster than gil updates. Mitigation: 200k conservative default + structured logging when unknown model encountered, so gaps surface in dogfood.
- **Grace call burns extra cost**. One extra call near budget cap ≈ 1% of total mission cost typically. Mitigation: `--no-grace` flag for users who prefer hard cutoff.
- **Char-multiplier table is still inaccurate**. Yes — V1.5/V2 fix this. V1 is "less wrong than 4-char uniform," not "correct."

## 8. Open Decisions Deferred to Implementation

- Exact signature of Compactor factory — depends on spec resolver shape; lift the pattern from existing factory functions in `server/internal/service/`.
- Whether the grace-call wrap-up goes into the rollout log as a normal assistant message or a special event type. Lean: normal message + `synthetic=true` tag in metadata.
- Char-multiplier values are seed estimates; tighten after first dogfood run with cost telemetry.

## 9. Out of Scope

- Tokenizer V1.5/V2 (Phase 27.5, Phase 28)
- Memory-bank GC / auto-summarization
- Multi-user, cloud VM
- New providers, sandbox modes
- Chat surface (Phase 26)

## 10. Acceptance

Phase 27 V1 ships when:

1. A 100-iter mission against Anthropic completes without provider context-overflow errors.
2. Run event log contains ≥1 `compaction.applied` event for that mission.
3. Anthropic cache-hit rate on the run is observable (> 0).
4. Switching the mission to a smaller model (e.g., Haiku) at the architect/coder boundary triggers earlier compaction (provable via event timestamps).
5. Forcing budget exhaust on a test mission produces a single extra wrap-up message and exits with `"budget_exhausted_with_handoff"` status.
6. All unit + integration tests pass; `go test ./...` clean.
7. P26 implementation can read `[run · iter X/100 · $Y]` from the event stream and trust the numbers.
