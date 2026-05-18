# Phase 43 — Chat provider retry resilience

## Why

RunService wraps its provider with `provider.NewRetry` (run.go:681)
so the run-time agent loop transparently survives 429 / 5xx / network
blips. SessionService.Prompt — the chat surface entrypoint — did
NOT. A user mid-conversation who hits an Anthropic 429 sees an
immediate "rpc error: ..." response with no retry, no graceful
backoff, no preservation of the next attempt.

For the autonomous coding harness goal, this asymmetry matters:
chat sessions can run for hours (especially with P34/P35 making
that practical). Real LLM upstream errors are normal. Failing
hard on the first transient blip breaks the autonomy promise.

## Goal

Wrap the chat-side provider with `NewRetry` exactly the way the
run-time loop already does, so transient errors transparently
recover with exponential backoff (default: 4 attempts, 500ms
base). Permanent errors (auth, schema violation) still surface
immediately.

## Design

One-line code change in `session_prompt.go` after `providerFactory`
resolves the provider:

```go
prov, factoryModel, ferr := s.providerFactory(provName)
// ...
prov = provider.NewRetry(prov)
```

Mirrors run.go:681 exactly. The wrapper:
- 4 attempts default
- 500ms base exponential backoff
- isRetryable() classifier (5xx, 429, network timeouts, broken
  connection — see core/provider/retry.go)
- Honors Retry-After hints from typed ProviderRateLimit /
  ProviderTransient errors
- Cancellable via ctx (no zombie retries on stream cancel)

## Acceptance criteria

1. Transient error (substring matching isRetryable's table — e.g.
   "rate_limit", "503", "timeout") on first N Complete calls →
   chat agent recovers, returns success on attempt N+1.
2. Permanent error (e.g. "401 unauthorized") → fails immediately,
   no retry.
3. User message persisted exactly ONCE in chat history even when
   provider retries fire (Prompt.append happens before the
   Complete call, retries are internal to Complete).
4. No regression in existing service tests (100+ tests pass).

## Result (2026-05-17)

**Shipped.** 4 unit tests pass:
- `TestChatRetry_TransientErrors_RecoversViaRetryWrap` — 2
  transient failures then success; 1.6s wall (driven by 500ms
  base backoff × 2 retries).
- `TestChatRetry_PermanentError_SurfacesImmediately` — 401 surfaces
  in <0.1s, no retry.
- `TestChatRetry_RetryDoesNotDuplicateUserTurnInHistory` — exactly
  1 user row in history even after retries.
- `TestChatRetry_PostRetry_SessionRemainsValid` — session status
  unchanged (chat Prompt never changes status; only freeze_spec /
  start_run do).

**Files touched:** 3
- `server/internal/service/session_prompt.go` — `prov = provider.NewRetry(prov)`
- `server/internal/service/chat_retry_test.go` (new) — 4 tests
  with transientThenSuccessProvider + permanentFailureProvider
  stubs.
- `docs/plans/phase-43-chat-retry-resilience.md` — this doc.

No proto / SDK / CLI / TUI changes. Pure resilience hardening on
the daemon side.

## Followups (not in this phase)

- Surface retry attempts in the chat surface (the OnRetry callback
  on Retry already supports a hook; could wire it to `emitChatEvent`
  so the user sees "retrying 2/4 · 1.0s" inline rather than a silent
  pause).
- Tune defaults per provider (Anthropic's 429 sometimes wants 30s+
  while vllm/local typically recovers in 1s; current 500ms × 2^N
  may over- or under-back-off depending).
