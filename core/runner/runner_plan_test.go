package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/plan"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

// captureProvider is a test provider that records the System prompt of
// every Complete call. The scripted turns drive what tools the agent
// invokes.
type captureProvider struct {
	systems []string
	turns   []provider.MockTurn
	idx     int
}

func (c *captureProvider) Name() string { return "capture" }
func (c *captureProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	c.systems = append(c.systems, req.System)
	t := c.turns[c.idx]
	if c.idx < len(c.turns)-1 {
		c.idx++
	}
	return provider.Response{
		Text:         t.Text,
		ToolCalls:    t.ToolCalls,
		StopReason:   t.StopReason,
		InputTokens:  5,
		OutputTokens: 10,
	}, nil
}

// TestAgentLoop_PlanPrependedToSystemPrompt confirms that once the
// agent writes a plan via the `plan` tool, subsequent iterations see a
// "=== PLAN" block injected at the top of their system prompt.
func TestAgentLoop_PlanPrependedToSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	store := plan.NewStore(dir)
	planTool := &tool.Plan{Store: store, SessionID: "s1"}

	prov := &captureProvider{turns: []provider.MockTurn{
		// Turn 1: agent writes a 3-item plan.
		{
			Text: "Writing plan.",
			ToolCalls: []provider.ToolCall{{
				ID: "p1", Name: "plan",
				Input: json.RawMessage(`{
                    "operation":"set",
                    "items":[
                        {"text":"analyze repomap","status":"completed"},
                        {"text":"refactor theme","status":"in_progress"},
                        {"text":"add toggle","status":"pending"}
                    ]
                }`),
			}},
			StopReason: "tool_use",
		},
		// Turn 2: stop, let verifier run.
		{Text: "Done.", StopReason: "end_turn"},
	}}

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test plan prepend"},
		Verification: &gilv1.Verification{}, // no checks → done immediately
		Budget:       &gilv1.Budget{MaxIterations: 4},
	}

	loop := NewAgentLoop(spec, prov, "test-model", []tool.Tool{planTool}, verify.NewRunner(dir))
	loop.Plan = store
	loop.SessionID = "s1"

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	// Iteration 1's system prompt should NOT contain a plan block (the
	// plan didn't exist yet when the prompt was built).
	require.False(t, strings.Contains(prov.systems[0], "=== PLAN"),
		"iter 1 should not have a plan prepend; got:\n%s", prov.systems[0])

	// Iteration 2's system prompt SHOULD contain the plan block, with
	// all three statuses rendered.
	require.GreaterOrEqual(t, len(prov.systems), 2, "expected >=2 provider calls")
	iter2 := prov.systems[1]
	require.Contains(t, iter2, "=== PLAN")
	require.Contains(t, iter2, "analyze repomap")
	require.Contains(t, iter2, "refactor theme")
	require.Contains(t, iter2, "add toggle")
	// Status glyphs should be present per spec.
	require.Contains(t, iter2, "✓") // completed
	require.Contains(t, iter2, "●") // in progress
	require.Contains(t, iter2, "○") // pending
}

// TestAgentLoop_EmptyPlan_NoPrepend asserts the prompt is unchanged when
// no plan exists — important for cache-prefix stability.
func TestAgentLoop_EmptyPlan_NoPrepend(t *testing.T) {
	dir := t.TempDir()
	store := plan.NewStore(dir)
	prov := &captureProvider{turns: []provider.MockTurn{
		{Text: "nothing to do", StopReason: "end_turn"},
	}}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "noop"},
		Verification: &gilv1.Verification{},
		Budget:       &gilv1.Budget{MaxIterations: 2},
	}
	loop := NewAgentLoop(spec, prov, "m", nil, verify.NewRunner(dir))
	loop.Plan = store
	loop.SessionID = "s2"
	_, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(prov.systems), 1)
	require.False(t, strings.Contains(prov.systems[0], "=== PLAN"),
		"empty plan should not prepend anything")
}

// TestAgentLoop_PlanUpdatedEvent confirms emitter wires through stream.
func TestAgentLoop_PlanUpdatedEvent(t *testing.T) {
	dir := t.TempDir()
	store := plan.NewStore(dir)

	stream := event.NewStream()
	sub := stream.Subscribe(64)
	defer sub.Close()

	planTool := &tool.Plan{
		Store:     store,
		SessionID: "s3",
		Emit: func(ctx context.Context, p *plan.Plan, op string) {
			b, _ := json.Marshal(map[string]any{
				"op":      op,
				"version": p.Version,
				"items":   len(p.Items),
			})
			_, _ = stream.Append(event.Event{
				Source: event.SourceAgent,
				Kind:   event.KindObservation,
				Type:   "plan_updated",
				Data:   b,
			})
		},
	}
	prov := &captureProvider{turns: []provider.MockTurn{
		{
			Text: "set",
			ToolCalls: []provider.ToolCall{{
				ID: "p", Name: "plan",
				Input: json.RawMessage(`{"operation":"set","items":[{"text":"x"}]}`),
			}},
			StopReason: "tool_use",
		},
		{Text: "done", StopReason: "end_turn"},
	}}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "emit"},
		Verification: &gilv1.Verification{},
		Budget:       &gilv1.Budget{MaxIterations: 3},
	}
	loop := NewAgentLoop(spec, prov, "m", []tool.Tool{planTool}, verify.NewRunner(dir))
	loop.Plan = store
	loop.SessionID = "s3"
	loop.Events = stream
	_, err := loop.Run(context.Background())
	require.NoError(t, err)

	// Drain the subscription channel and look for plan_updated.
	found := false
	timeout := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				break drain
			}
			if e.Type == "plan_updated" {
				found = true
			}
		case <-timeout:
			break drain
		default:
			break drain
		}
	}
	require.True(t, found, "expected at least one plan_updated event")
}
