# Phase 28 — Anthropic + Google count_tokens API Integration

**Status**: planned, not started
**Predecessor**: Phase 27.5 (OpenAI tiktoken)

## Goal

Replace the char-multiplier heuristic in `core/provider/tokenest.go`
for Anthropic and Google with their respective `count_tokens` API
endpoints. After Phase 28, all major providers' token counts are
exact (or near-exact for cached responses).

Local Ollama models will continue to use the heuristic, since per-model
tokenizers are model-specific files and per-call API trips don't apply.

## Scope

- Anthropic: call `POST /v1/messages/count_tokens` with the message
  array; cache the response per-(model, message-prefix-hash).
- Google: call the equivalent count_tokens method on the Gemini SDK;
  cache the response.
- Both API calls cost network round-trip; cache aggressively per session
  to keep compaction-trigger checks cheap.
- Add a `TokenCountCache` abstraction in `core/provider/tokencache.go`
  with TTL eviction and per-session scoping.
- Wire the new exact counts into `core/provider/tokenest.go`'s
  `EstimateTokens` function so callers don't need to change.

## Files

- Create: `core/provider/tokencache.go`
- Create: `core/provider/tokencache_test.go`
- Modify: `core/provider/anthropic.go` (add count_tokens caller)
- Modify: `core/provider/google.go` (add count_tokens caller)
- Modify: `core/provider/tokenest.go` (route Anthropic + Google to
  exact path; keep Ollama on heuristic; OpenAI still on tiktoken
  from Phase 27.5)
- Modify: `core/provider/tokenest_test.go`

## Estimate

~3-4 days.

## Risks

- **Network failures**: count_tokens API calls can fail or be slow.
  Mitigation: fall back to char-multiplier when the cache misses AND
  the API returns an error.
- **Cost**: count_tokens API has its own pricing. Mitigation: cache
  per session; budget guard sets a max-count_tokens-calls-per-session
  ceiling.
- **Cache invalidation**: caching is per (model, prefix-hash); when
  the message prefix mutates (compaction!), recompute. Mitigation:
  invalidate cache entries on `compact_applied` event.

## Out of scope

- Tokenizers for local-model providers other than Ollama
- Real-time token streaming counts (out of scope until streaming is
  re-architected)

## Acceptance

- Anthropic mission cost reports match Console billing within ±1%.
- Compaction triggers fire at exactly 95% of context window (no
  guesswork).
- count_tokens API call rate observable in event log; not exceeding
  configured ceiling.
