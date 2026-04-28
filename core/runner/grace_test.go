package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
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
