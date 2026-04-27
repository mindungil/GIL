package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mindungil/gil/core/cost"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

// budgetTokenProvider returns a constant token-counted turn each call so
// tests can predict when a token budget will be crossed. Each call adds
// in+out tokens to the running total; after K calls the loop should hit
// the cap.
type budgetTokenProvider struct {
	in, out int64
	calls   int
}

func (p *budgetTokenProvider) Name() string { return "budget-mock" }

func (p *budgetTokenProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	p.calls++
	// Always emit a tool call so the loop never enters the verify branch
	// before the budget cap fires. The tool is a noop (declared in
	// runner_test.go) registered by the test.
	return provider.Response{
		Text: "step",
		ToolCalls: []provider.ToolCall{
			{ID: "x", Name: "noop", Input: json.RawMessage(`{}`)},
		},
		StopReason:   "tool_use",
		InputTokens:  p.in,
		OutputTokens: p.out,
	}, nil
}

// TestAgentLoop_BudgetTokens_StopsAtCap verifies token-budget enforcement:
// with a 100-token cap and a provider that bills 40 in + 10 out per call
// (50/iter), the loop must stop on iteration 2 with status="budget_exhausted"
// and BudgetReason="tokens". MaxIterations is set high so the iteration cap
// is not what stops the run.
func TestAgentLoop_BudgetTokens_StopsAtCap(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 40, out: 10}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", res.Status)
	require.Equal(t, "tokens", res.BudgetReason)
	require.Equal(t, 2, res.Iterations, "iter1=50 tokens, iter2=100 hits the cap")
	require.Equal(t, int64(100), res.Tokens)
}

// TestAgentLoop_BudgetTokens_EmitsWarningAt75 streams events and asserts
// that crossing 75% emits a budget_warning before the eventual
// budget_exceeded. With cap=100 and 30/iter, the warning fires on iter 3
// (90 tokens, frac=0.90 >= 0.75) and the cap fires on iter 4 (120 >= 100).
func TestAgentLoop_BudgetTokens_EmitsWarningAt75(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 25, out: 5} // 30/iter

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}}},
		Budget:       &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100},
	}
	tools := []tool.Tool{&noopTool{}}
	stream := event.NewStream()
	sub := stream.Subscribe(128)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "x",
		Tools:    tools,
		Verifier: verify.NewRunner(dir),
		Events:   stream,
	}
	resCh := make(chan *Result, 1)
	go func() {
		r, _ := loop.Run(context.Background())
		resCh <- r
	}()

	var warnings, exceeded int
	timeout := time.After(2 * time.Second)
collect:
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				break collect
			}
			switch e.Type {
			case "budget_warning":
				warnings++
				// Warning payload should carry reason+used+limit so the
				// TUI can render the meter without needing extra RPCs.
				var d map[string]any
				require.NoError(t, json.Unmarshal(e.Data, &d))
				require.Equal(t, "tokens", d["reason"])
			case "budget_exceeded":
				exceeded++
				break collect
			}
		case <-timeout:
			t.Fatal("timed out waiting for budget events")
		}
	}
	res := <-resCh
	require.Equal(t, "budget_exhausted", res.Status)
	require.Equal(t, 1, warnings, "exactly one warning per dimension crossing")
	require.Equal(t, 1, exceeded)
}

// TestAgentLoop_BudgetCost_StopsAtCap verifies USD-budget enforcement.
// Use a model in the embedded catalog (claude-haiku-4-5: $1/M in,
// $5/M out). With 200_000 in + 200_000 out per iter the cost per iter
// is $0.20 + $1.00 = $1.20; cap=$2.50 → stop on iter 3 (3*1.20=$3.60 >=
// $2.50; iter 2 = $2.40 < cap).
func TestAgentLoop_BudgetCost_StopsAtCap(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 200_000, out: 200_000}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}}},
		Budget:       &gilv1.Budget{MaxIterations: 50, MaxTotalCostUsd: 2.50},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "claude-haiku-4-5", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", res.Status)
	require.Equal(t, "cost", res.BudgetReason)
	require.Equal(t, 3, res.Iterations)
	require.InDelta(t, 3.60, res.CostUSD, 0.01)
}

// TestAgentLoop_BudgetCost_UnknownModelDoesNotEnforce: the calculator
// returns found=false for an unknown model, so cost stays $0 and the
// loop runs to MaxIterations without a budget_exceeded firing — token /
// iteration caps are still authoritative. Catches a regression where
// callers might unwittingly stop a no-cost vLLM run because the catalog
// lookup mis-reported a price.
func TestAgentLoop_BudgetCost_UnknownModelDoesNotEnforce(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 1_000_000, out: 1_000_000}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}}},
		Budget:       &gilv1.Budget{MaxIterations: 2, MaxTotalCostUsd: 0.01},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "no-such-model-xyz", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "max_iterations", res.Status, "cost cap should not fire on unknown model")
	require.Equal(t, 2, res.Iterations)
	require.Equal(t, 0.0, res.CostUSD)
}

// TestAgentLoop_NoBudgetCaps_BackwardsCompat: a spec with no token / cost
// caps must behave exactly like the pre-budget code path — iteration cap
// alone, no budget_warning / budget_exceeded events, no Calculator
// auto-construction.
func TestAgentLoop_NoBudgetCaps_BackwardsCompat(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 100, out: 100}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}}},
		Budget:       &gilv1.Budget{MaxIterations: 2},
	}
	tools := []tool.Tool{&noopTool{}}
	stream := event.NewStream()
	sub := stream.Subscribe(64)
	defer sub.Close()

	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "x", Tools: tools,
		Verifier: verify.NewRunner(dir), Events: stream,
	}
	resCh := make(chan *Result, 1)
	go func() {
		r, _ := loop.Run(context.Background())
		resCh <- r
	}()
	var sawBudgetEvt bool
	timeout := time.After(2 * time.Second)
loop2:
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				break loop2
			}
			if e.Type == "budget_warning" || e.Type == "budget_exceeded" {
				sawBudgetEvt = true
			}
			if e.Type == "run_max_iterations" {
				break loop2
			}
		case <-timeout:
			break loop2
		}
	}
	res := <-resCh
	require.Equal(t, "max_iterations", res.Status)
	require.False(t, sawBudgetEvt, "no budget events when caps are zero")
	require.Nil(t, loop.CostCalculator, "calculator should not be lazily built when no cost cap")
}

// Compile-time check that the test reaches into cost.Calculator's API
// in case future changes rename the public surface.
var _ = (&cost.Calculator{}).Estimate
