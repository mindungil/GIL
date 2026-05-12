package runner

import (
	"context"

	"github.com/mindungil/gil/core/provider"
)

// graceWrapUpPrompt is the synthetic user instruction inserted on the
// final turn before budget exhaustion. The agent is asked to stop work
// and emit a hand-off summary so a future resume has a clean starting
// point.
const graceWrapUpPrompt = `[BUDGET WRAP-UP] You are about to hit the budget cap. This is your final turn.
Stop work. Output: (1) what got done, (2) what's pending, (3) which file/state
the next iteration should resume from. Do not call tools.`

// checkBudgetAndMaybeGrace inspects the synced budget usage against the
// caps. When usage has crossed the budget cap, it fires one final
// "wrap-up" call to the provider asking for a hand-off summary, then
// sets a.graceStatus to "budget_exhausted_with_handoff". When NoGrace
// is true the wrap-up is skipped and graceStatus is left empty (the
// caller's verify-based status classification takes effect instead).
//
// The method is idempotent: a second call after graceFired is set is a
// no-op (status preserved, no duplicate provider call).
func (a *AgentLoop) checkBudgetAndMaybeGrace(ctx context.Context) error {
	overTokens := a.graceBudgetMaxTokens > 0 && a.graceTotalTokens >= a.graceBudgetMaxTokens
	overCost := a.graceBudgetMaxCostUSD > 0 && a.graceTotalCostUSD >= a.graceBudgetMaxCostUSD
	if !overTokens && !overCost {
		return nil
	}
	if a.NoGrace {
		// Hard-cutoff mode: leave graceStatus empty so Run()'s
		// verify-based classification applies unchanged.
		return nil
	}
	if a.graceFired {
		// Already fired once — preserve the handoff status without
		// sending another provider call.
		a.graceStatus = "budget_exhausted_with_handoff"
		return nil
	}
	a.graceFired = true

	// Build a one-shot wrap-up request: existing messages + a single
	// user message asking for the hand-off summary.
	req := a.buildWrapUpRequest()
	p := a.Provider
	if p == nil {
		a.graceStatus = "budget_exhausted_with_handoff"
		return nil
	}
	resp, err := p.Complete(ctx, req)
	if err != nil {
		// Soft-fail: the budget cap is still enforced; we just don't
		// have the wrap-up summary. Status is still "with_handoff" so
		// callers know a grace attempt was made.
		a.graceStatus = "budget_exhausted_with_handoff"
		return nil
	}
	a.graceMessages = append(a.graceMessages, provider.Message{
		Role:    provider.RoleAssistant,
		Content: resp.Text,
	})
	a.graceStatus = "budget_exhausted_with_handoff"
	return nil
}

// buildWrapUpRequest assembles the final wrap-up request: the message
// history synced at the budget-break site plus a single synthetic user
// message inviting the summary. Tools are intentionally omitted — the
// wrap-up turn MUST NOT call tools.
func (a *AgentLoop) buildWrapUpRequest() provider.Request {
	msgs := make([]provider.Message, len(a.graceMessages))
	copy(msgs, a.graceMessages)
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleUser,
		Content: graceWrapUpPrompt,
	})
	return provider.Request{
		Model:    a.pickModel(RoleMain),
		Messages: msgs,
		// Tools intentionally omitted — wrap-up should not call tools.
	}
}

// syncGraceState copies the local-variable snapshot from Run() into the
// struct fields that checkBudgetAndMaybeGrace reads. Called at each
// budget-break site so the method always sees current counters.
func (a *AgentLoop) syncGraceState(
	totalTokens int64,
	totalCostUSD float64,
	budgetMaxTokens int64,
	budgetMaxCostUSD float64,
	messages []provider.Message,
) {
	a.graceTotalTokens = totalTokens
	a.graceTotalCostUSD = totalCostUSD
	a.graceBudgetMaxTokens = budgetMaxTokens
	a.graceBudgetMaxCostUSD = budgetMaxCostUSD
	// Copy slice so grace has a stable snapshot (Run() may keep appending
	// to the original after the sync).
	snap := make([]provider.Message, len(messages))
	copy(snap, messages)
	a.graceMessages = snap
}
