package service

import (
	"context"

	"github.com/mindungil/gil/core/compact"
	"github.com/mindungil/gil/core/provider"
)

// chat_compact.go owns chat-side conversation compaction (P35). The
// run-time agent loop in core/runner already runs the same Hermes
// pattern in-flight; this file applies it at chat-surface Prompt
// entry so long natural-language conversations don't exhaust the
// model's context window.
//
// Trigger: at Prompt entry, after loading history + appending the
// new user turn, BEFORE the first provider.Complete. When estimated
// tokens cross 95% of the model's context window, summarize the
// middle, keep head + tail. The chat agent never sees the compaction
// happen — it's a system-level safety net.

// compactChatThresholdFactor is the fraction of a model's context
// window above which the chat history gets compacted. Mirrors the
// runner.go in-loop threshold (P27 T5).
const compactChatThresholdFactor = 0.95

// fallbackContextWindow is the context-window estimate used when the
// provider.ContextTokensForProvider lookup returns 0. Matches runner.go's
// same belt-and-suspenders guard.
const fallbackContextWindow int64 = 200_000

// compactChatIfNeeded estimates the token cost of msgs and runs the
// shared core/compact.Compactor when the estimate crosses the
// per-model threshold. Returns the compacted slice (or the original
// untouched if the threshold wasn't met or the compactor declined).
// Soft-fail: provider errors surface as a non-nil err and msgs is
// returned unchanged; the caller continues with the original history.
func compactChatIfNeeded(
	ctx context.Context,
	providerID, model string,
	prov provider.Provider,
	msgs []provider.Message,
) (out []provider.Message, didCompact bool, err error) {
	if prov == nil || len(msgs) == 0 {
		return msgs, false, nil
	}
	ctxWindow := provider.ContextTokensForProvider(providerID, model)
	if ctxWindow == 0 {
		ctxWindow = fallbackContextWindow
	}
	threshold := int64(float64(ctxWindow) * compactChatThresholdFactor)
	if estimateChatMessagesTokens(providerID, msgs) < threshold {
		return msgs, false, nil
	}
	c := &compact.Compactor{
		Provider:   prov,
		ProviderID: providerID,
		Model:      model,
	}
	compacted, res, cerr := c.Compact(ctx, msgs)
	if cerr != nil {
		return msgs, false, cerr
	}
	if res.Skipped {
		// Middle too small for the compactor's MinMiddle (default 8) —
		// nothing to summarize even though we crossed the token threshold
		// (e.g. fewer than ~10 messages, each huge). The caller continues
		// with the original; the agent loop's own emergency truncation
		// (provider-side) will take over if the LLM call still overflows.
		return msgs, false, nil
	}
	return compacted, true, nil
}

// estimateChatMessagesTokens duplicates the small helper in
// core/runner/runner.go (estimateMessagesTokens, unexported) so we
// don't need to either import runner into service or export a new
// symbol. Same per-provider density heuristic as compact/compactor.go's
// estimateTokens — using provider.EstimateTokens, which already
// accounts for tokenizer differences across anthropic/openai/vllm/etc.
func estimateChatMessagesTokens(providerID string, msgs []provider.Message) int64 {
	total := int64(0)
	for _, m := range msgs {
		total += int64(provider.EstimateTokens(providerID, m.Content))
		for _, tc := range m.ToolCalls {
			total += int64(provider.EstimateTokens(providerID, string(tc.Input)))
		}
		for _, tr := range m.ToolResults {
			total += int64(provider.EstimateTokens(providerID, tr.Content))
		}
	}
	return total
}
