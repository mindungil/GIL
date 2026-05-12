# Phase 27.5 — OpenAI tiktoken-go Integration

**Status**: planned, not started
**Predecessor**: Phase 27 (V1 char-multiplier heuristic)
**Successor**: Phase 28 (Anthropic + Google count_tokens API)

## Goal

Replace the provider-aware char-multiplier in `core/provider/tokenest.go`
for OpenAI requests with `pkoukk/tiktoken-go` so OpenAI token counts
are exact (matches the official tiktoken encoder used by the model).

After 27.5: OpenAI-routed turns use exact token counts; Anthropic /
Google / Ollama keep the heuristic until Phase 28.

## Scope

- Add `github.com/pkoukk/tiktoken-go` to `go.mod` (MIT-licensed,
  small dep, offline).
- In `core/provider/tokenest.go`, branch on provider:
  - OpenAI → call tiktoken with the model's encoder
  - Anthropic / Google / Ollama → keep current char-multiplier
- Cache the tiktoken encoder per-model (encoder instantiation has
  one-time cost; encoders are reusable across calls).
- Add tests asserting exact-token counts against known fixtures
  (OpenAI publishes reference encodings — use a few canonical strings).
- Verify the runner's compaction trigger fires at exactly 95% of the
  per-model context window for OpenAI runs (no more "off by 20%"
  surprise).

## Files

- Modify: `core/provider/tokenest.go`
- Modify: `core/provider/tokenest_test.go`
- Modify: `go.mod`, `go.sum`

## Estimate

~1-2 days.

## Out of scope

- Anthropic count_tokens API (Phase 28)
- Google count_tokens API (Phase 28)
- Local Ollama per-model tokenizers (Phase 28+)

## Acceptance

- An OpenAI mission's `cost --by-role` numbers match Anthropic-side or
  OpenAI-side billing reports within ±2% (current heuristic drifts ±15%).
- All existing tests pass with no behavior change for non-OpenAI providers.
