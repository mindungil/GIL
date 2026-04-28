package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// graceProvider records every Complete call so tests can verify the
// grace-call wrap-up message + count.
type graceProvider struct {
	name      string
	callCount int
	lastReq   provider.Request
	response  string
}

func (g *graceProvider) Name() string { return g.name }
func (g *graceProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	g.callCount++
	g.lastReq = req
	return provider.Response{Text: g.response}, nil
}

// newTestLoopForGrace builds a minimal AgentLoop wired for grace-call
// unit tests. It sets Provider + Providers["anthropic"] to the supplied
// graceProvider and seeds graceMessages with one prior assistant turn so
// buildWrapUpRequest has something to append to.
func newTestLoopForGrace(t *testing.T, p *graceProvider) *AgentLoop {
	t.Helper()
	loop := &AgentLoop{
		Provider: p,
		Providers: map[string]provider.Provider{
			"anthropic": p,
		},
		Model: "claude-sonnet-4-6",
		// Seed a minimal prior conversation so the wrap-up request has context.
		graceMessages: []provider.Message{
			{Role: provider.RoleUser, Content: "begin"},
			{Role: provider.RoleAssistant, Content: "working on it"},
		},
	}
	return loop
}

func TestGraceCall_FiresOnceOnBudgetExhaust(t *testing.T) {
	captured := &graceProvider{name: "anthropic", response: "done: A. pending: B. resume: file C."}
	loop := newTestLoopForGrace(t, captured)
	loop.graceBudgetMaxTokens = 100
	loop.graceTotalTokens = 110

	err := loop.checkBudgetAndMaybeGrace(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, captured.callCount, "grace must fire exactly once")

	last := captured.lastReq.Messages[len(captured.lastReq.Messages)-1]
	require.Contains(t, strings.ToUpper(last.Content), "WRAP")
	require.Contains(t, strings.ToLower(last.Content), "pending")
	require.Contains(t, strings.ToLower(last.Content), "resume")
}

func TestGraceCall_RespectsNoGraceFlag(t *testing.T) {
	captured := &graceProvider{name: "anthropic"}
	loop := newTestLoopForGrace(t, captured)
	loop.graceBudgetMaxTokens = 100
	loop.graceTotalTokens = 110
	loop.NoGrace = true

	require.NoError(t, loop.checkBudgetAndMaybeGrace(context.Background()))
	require.Equal(t, 0, captured.callCount, "no-grace flag must skip the wrap-up call")
}

func TestGraceCall_EndStatusIncludesHandoff(t *testing.T) {
	captured := &graceProvider{name: "anthropic", response: "wrap-up"}
	loop := newTestLoopForGrace(t, captured)
	loop.graceBudgetMaxTokens = 100
	loop.graceTotalTokens = 110

	require.NoError(t, loop.checkBudgetAndMaybeGrace(context.Background()))
	require.Equal(t, "budget_exhausted_with_handoff", loop.graceStatus)
}

func TestGraceCall_FiresOnlyOnceOnRepeatedCheck(t *testing.T) {
	captured := &graceProvider{name: "anthropic"}
	loop := newTestLoopForGrace(t, captured)
	loop.graceBudgetMaxTokens = 100
	loop.graceTotalTokens = 110

	_ = loop.checkBudgetAndMaybeGrace(context.Background())
	_ = loop.checkBudgetAndMaybeGrace(context.Background())
	require.Equal(t, 1, captured.callCount, "second call must not re-fire grace")
}

// TestRunner_BudgetExhaustGrace_ResultStatusIsHandoff drives a full Run() that
// exhausts the token budget and asserts that Result.Status (the PUBLIC API) is
// "budget_exhausted_with_handoff", not the verify-based variant. This is the
// contract regression that was broken before the graceStatus override was added
// to the post-loop finalStatus block.
func TestRunner_BudgetExhaustGrace_ResultStatusIsHandoff(t *testing.T) {
	dir := t.TempDir()
	// budgetTokenProvider emits 50 tokens/iter (in=40, out=10) and keeps
	// looping via tool_use. Reserve=1 → effective cap=99, so iter2 (100 tokens)
	// trips the guard. NoGrace is NOT set — grace MUST fire.
	prov := &budgetTokenProvider{in: 40, out: 10}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		// No Verification checks → verifyResults will be empty, which without
		// the fix would produce the legacy "budget_exhausted" status. With the
		// fix graceStatus overrides it to "budget_exhausted_with_handoff".
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 1},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	// NoGrace intentionally left false so the wrap-up call fires.

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted_with_handoff", res.Status,
		"graceStatus must propagate into Result.Status (public API), not stay hidden on the struct")
	require.Equal(t, "tokens", res.BudgetReason)
}

// TestRunner_BudgetExhaustNoGrace_ResultStatusIsExhausted mirrors the above but
// with NoGrace=true. The wrap-up call is skipped, so the legacy
// "budget_exhausted" status (no verify checks) must be preserved.
func TestRunner_BudgetExhaustNoGrace_ResultStatusIsExhausted(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 40, out: 10}
	spec := &gilv1.FrozenSpec{
		Goal:   &gilv1.Goal{OneLiner: "x"},
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 1},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	loop.NoGrace = true // disable grace → should get legacy status

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", res.Status,
		"with NoGrace=true no handoff fires; legacy status must be preserved")
	require.Equal(t, "tokens", res.BudgetReason)
}

func TestGraceCall_ProviderNilSetsHandoffStatus(t *testing.T) {
	loop := newTestLoopForGrace(t, &graceProvider{name: "anthropic"})
	loop.Provider = nil // simulate misconfigured loop
	loop.graceBudgetMaxTokens = 100
	loop.graceTotalTokens = 110

	require.NoError(t, loop.checkBudgetAndMaybeGrace(context.Background()))
	require.Equal(t, "budget_exhausted_with_handoff", loop.graceStatus,
		"graceStatus must still signal handoff even when no provider call is possible")
}
