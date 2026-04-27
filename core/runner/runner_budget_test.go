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
// with a 100-token cap, a 1-token reserve (so the test stays deterministic
// regardless of the runner's default reserve), and a provider that bills 40
// in + 10 out per call (50/iter), the loop must stop on iteration 2 with a
// budget_exhausted_* status (verify here is `false`, so it lands on
// budget_exhausted_verify_failed) and BudgetReason="tokens".
// MaxIterations is set high so the iteration cap is not what stops the
// run.
func TestAgentLoop_BudgetTokens_StopsAtCap(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 40, out: 10}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
		// ReserveTokens=1 keeps the effective cap = 99 so iter1 (50 tokens)
		// stays under and iter2 (100 tokens) trips the guard. Without a
		// pin here, the runner's 8000-token default would dominate this
		// 100-token test cap and trip the guard immediately on iter1.
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 1},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	// `false` verifier check fails on the post-loop run, so the new
	// status mapping picks budget_exhausted_verify_failed. Pre-Phase-19
	// callers that match the legacy budget_exhausted prefix keep working.
	require.Equal(t, "budget_exhausted_verify_failed", res.Status)
	require.Equal(t, "tokens", res.BudgetReason)
	require.Equal(t, 2, res.Iterations, "iter1=50 tokens, iter2=100 hits the cap")
	require.Equal(t, int64(100), res.Tokens)
	require.NotEmpty(t, res.VerifyAll, "post-loop verify must populate VerifyAll even on budget exhaust")
}

// TestAgentLoop_BudgetTokens_EmitsWarningAt75 streams events and asserts
// that crossing 75% emits a budget_warning before the eventual
// budget_exceeded. With cap=100, reserve=1 (effective cap=99), and 30/iter,
// the warning fires on iter 3 (90 tokens, frac=0.90 >= 0.75) and the cap
// trips on iter 4 (120 >= 99). Reserve is pinned so the runner's
// 8000-token default doesn't dominate this 100-token contrived cap.
func TestAgentLoop_BudgetTokens_EmitsWarningAt75(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 25, out: 5} // 30/iter

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}}},
		Budget:       &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 1},
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
	// New status taxonomy: post-loop verify ran the `false` check and it
	// failed, so we land on budget_exhausted_verify_failed. The legacy
	// budget_exhausted prefix is preserved on BudgetReason so older
	// callers that match the prefix keep working.
	require.Equal(t, "budget_exhausted_verify_failed", res.Status)
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
	// Cost cap fires same as before, but with the new always-final-verify
	// path the `false` check fails on the post-loop run → status becomes
	// budget_exhausted_verify_failed. BudgetReason still says "cost" so
	// downstream meters/dashboards keep their wiring intact.
	require.Equal(t, "budget_exhausted_verify_failed", res.Status)
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

// budgetThenEndProvider yields a fixed number of tool-call turns before
// finally emitting end_turn. Used to simulate "agent does work, then
// declares done" while letting the test pin total token usage.
type budgetThenEndProvider struct {
	in, out         int64
	toolTurns       int
	calls           int
	endText         string
	endInputTokens  int64
	endOutputTokens int64
}

func (p *budgetThenEndProvider) Name() string { return "budget-end-mock" }

func (p *budgetThenEndProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	p.calls++
	if p.calls > p.toolTurns {
		// Final turn — no tool calls so the inline verify path fires.
		return provider.Response{
			Text:         p.endText,
			StopReason:   "end_turn",
			InputTokens:  p.endInputTokens,
			OutputTokens: p.endOutputTokens,
		}, nil
	}
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

// TestAgentLoop_BudgetReserve_VerifyPasses_StatusDone is Phase 19 Track A,
// case 1: with cap=100, reserve=8 (effective=92), agent uses 50/iter for
// 1 tool turn then ends. After the tool turn totalTokens=50 (under 92);
// the end-turn iteration adds 4 more tokens (54 < 92) and the inline
// verify fires + passes → status="done". The test pins ReserveTokens
// explicitly so the runner's default scaling doesn't interfere.
func TestAgentLoop_BudgetReserve_VerifyPasses_StatusDone(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetThenEndProvider{
		in:              40,
		out:             10,
		toolTurns:       1,
		endText:         "all done",
		endInputTokens:  3,
		endOutputTokens: 1,
	}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			// `true` always passes.
			Checks: []*gilv1.Check{{Name: "ok", Kind: gilv1.CheckKind_SHELL, Command: "true"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 8},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status, "agent ended within cap, verify passed → done")
	require.NotEmpty(t, res.VerifyAll)
	require.True(t, res.VerifyAll[0].Passed)
	require.Equal(t, "all done", res.FinalText)
}

// TestAgentLoop_BudgetExhausted_VerifyFails_NewStatus is Phase 19 Track A,
// case 2: cap=100, reserve=8, agent burns 95/iter so iter1 totals 95 ≥ 92
// (effective cap) → reserve guard trips. Post-loop verify runs the
// `false` check → fail → status=budget_exhausted_verify_failed, with
// VerifyAll populated so the user sees what state the workspace ended
// in. Before this fix the verifier never ran when budget tripped.
func TestAgentLoop_BudgetExhausted_VerifyFails_NewStatus(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 80, out: 15} // 95/iter
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 8},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted_verify_failed", res.Status)
	require.Equal(t, "tokens", res.BudgetReason)
	require.NotEmpty(t, res.VerifyAll, "VerifyAll must be populated on budget exhaust (Phase 19 fix)")
	require.False(t, res.VerifyAll[0].Passed)
}

// TestAgentLoop_BudgetExhausted_VerifyPasses_NewStatus is Phase 19 Track A,
// case 3: same shape as case 2 but the check is `true`. The agent's prior
// edits already satisfied verification, the reserve guard trips, and the
// post-loop verify reports green → status=budget_exhausted_verify_passed.
// This is the case the dogfood report described — qwen actually succeeded
// before budget hit, but the user couldn't tell.
func TestAgentLoop_BudgetExhausted_VerifyPasses_NewStatus(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 80, out: 15} // 95/iter
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "ok", Kind: gilv1.CheckKind_SHELL, Command: "true"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100, ReserveTokens: 8},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted_verify_passed", res.Status,
		"verify passed despite budget exhaust → report best-effort success")
	require.Equal(t, "tokens", res.BudgetReason)
	require.NotEmpty(t, res.VerifyAll)
	require.True(t, res.VerifyAll[0].Passed)
}

// TestAgentLoop_NoBudget_NoReserveBehavior verifies the backwards-compat
// guarantee from Phase 19 Track A: when the spec has no max_total_tokens,
// the reserve mechanism is silent — no budget events fire and the loop
// runs to MaxIterations exactly as it did pre-fix. Distinguishes from
// TestAgentLoop_NoBudgetCaps_BackwardsCompat by checking the post-loop
// verify still runs (so VerifyAll is populated) — that's the new
// behavior, additive on top of the legacy contract.
func TestAgentLoop_NoBudget_NoReserveBehavior(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 100, out: 100}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
		Budget: &gilv1.Budget{MaxIterations: 2}, // no token cap
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "max_iterations", res.Status)
	require.NotEmpty(t, res.VerifyAll, "post-loop verify still runs on max-iter exit")
	require.False(t, res.VerifyAll[0].Passed)
	require.Empty(t, res.BudgetReason, "no budget set → no BudgetReason")
}

// TestAgentLoop_BudgetReserve_DefaultScaling verifies the runner's
// default-policy heuristic: when ReserveTokens is unset the runner picks
// min(8000, max/10). Pin a tiny cap (100) so we observe the scaling
// kicks in: effective cap = 100 - 10 = 90, NOT 100 - 8000 (which would
// trip immediately). The test scripts a provider that uses 30/iter so
// the warning + cap fire on predictable iters.
func TestAgentLoop_BudgetReserve_DefaultScaling(t *testing.T) {
	dir := t.TempDir()
	prov := &budgetTokenProvider{in: 25, out: 5} // 30/iter
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "x"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
		// ReserveTokens unset on purpose — exercise the default policy.
		Budget: &gilv1.Budget{MaxIterations: 50, MaxTotalTokens: 100},
	}
	tools := []tool.Tool{&noopTool{}}
	loop := NewAgentLoop(spec, prov, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	// effective cap = 100 - 10 = 90. iter1=30, iter2=60, iter3=90 ≥ 90 → trip.
	require.Equal(t, 3, res.Iterations, "default reserve scales to max/10 = 10")
	require.Equal(t, "budget_exhausted_verify_failed", res.Status)
}

// Compile-time check that the test reaches into cost.Calculator's API
// in case future changes rename the public surface.
var _ = (&cost.Calculator{}).Estimate
