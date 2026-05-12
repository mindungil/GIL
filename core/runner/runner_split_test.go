// Tests for the architect/coder turn-routing split (Phase 19 Track C).
package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

// TestClassifyTurn covers the four classification rules in priority order.
func TestClassifyTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		iterIdx  int
		lastResp *provider.Response
		want     string
	}{
		{
			name:    "first_turn_always_planner",
			iterIdx: 0,
			// lastResp is irrelevant when iterIdx==0; passing one with
			// exec tools should NOT change the answer.
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{{Name: "bash"}}},
			want:     RolePlanner,
		},
		{
			name:     "nil_last_response_after_first_turn_falls_back_to_main",
			iterIdx:  3,
			lastResp: nil,
			want:     RoleMain,
		},
		{
			name:    "plan_tool_call_routes_to_planner",
			iterIdx: 5,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "plan"},
			}},
			want: RolePlanner,
		},
		{
			name:    "plan_with_other_tools_still_planner",
			iterIdx: 4,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "plan"},
				{Name: "bash"},
			}},
			want: RolePlanner,
		},
		{
			name:    "only_exec_tools_routes_to_editor",
			iterIdx: 2,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "bash"},
				{Name: "edit"},
			}},
			want: RoleEditor,
		},
		{
			name:    "single_exec_tool_routes_to_editor",
			iterIdx: 6,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "write_file"},
			}},
			want: RoleEditor,
		},
		{
			name:    "non_exec_tool_keeps_main",
			iterIdx: 7,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "subagent"},
			}},
			want: RoleMain,
		},
		{
			name:    "mixed_exec_and_non_exec_keeps_main",
			iterIdx: 8,
			lastResp: &provider.Response{ToolCalls: []provider.ToolCall{
				{Name: "bash"},
				{Name: "subagent"},
			}},
			want: RoleMain,
		},
		{
			name:     "no_tool_calls_keeps_main",
			iterIdx:  9,
			lastResp: &provider.Response{Text: "thinking..."},
			want:     RoleMain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyTurn(tt.iterIdx, tt.lastResp)
			require.Equal(t, tt.want, got)
		})
	}
}

// splitRecordingProvider tags every Complete with the model id passed in,
// so a test can verify which provider+model handled which iteration.
type splitRecordingProvider struct {
	mu    sync.Mutex
	name  string
	turns []provider.MockTurn
	idx   int
	calls []recordedCall // (model, system) per call
}

type recordedCall struct {
	Model  string
	System string
}

func (r *splitRecordingProvider) Name() string { return r.name }

func (r *splitRecordingProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{Model: req.Model, System: req.System})
	if r.idx >= len(r.turns) {
		// Sentinel: end the turn so the loop exits cleanly.
		return provider.Response{Text: "", StopReason: "end_turn", InputTokens: 1, OutputTokens: 1}, nil
	}
	t := r.turns[r.idx]
	r.idx++
	return provider.Response{
		Text:         t.Text,
		ToolCalls:    t.ToolCalls,
		StopReason:   t.StopReason,
		InputTokens:  10,
		OutputTokens: 10,
	}, nil
}

// noopTool returns ok for any input. Used so tool calls in scripted
// turns dispatch successfully without touching the filesystem.
type splitNoopTool struct{ name string }

func (n *splitNoopTool) Name() string                  { return n.name }
func (n *splitNoopTool) Description() string           { return "noop " + n.name }
func (n *splitNoopTool) Schema() json.RawMessage       { return json.RawMessage(`{"type":"object"}`) }
func (n *splitNoopTool) Run(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

// TestAgentLoop_Split_RoutesPlannerEditorMain wires a 3-provider split and
// verifies each iteration lands on the expected provider+model. Turn
// shapes:
//
//	iter 1 (idx 0): plan_tool_call           → planner
//	iter 2 (idx 1): bash + edit              → editor (after exec turn)
//	iter 3 (idx 2): mixed (bash + subagent)  → main
//	iter 4 (idx 3): no tools, end_turn       → main (verifier passes)
//
// We assert: each provider's first call carries the expected model, and a
// model_switched event is emitted on every transition.
func TestAgentLoop_Split_RoutesPlannerEditorMain(t *testing.T) {
	t.Parallel()

	plannerProv := &splitRecordingProvider{
		name: "planner-mock",
		turns: []provider.MockTurn{
			// iter 1: plan_tool_call → planner takes this turn AND the
			// next one (because the response calls plan, classifyTurn
			// for iter 2 would still pick planner). To force the editor
			// to pick up iter 2, we have iter 1's response NOT call plan
			// — it calls plan AND we want the editor for iter 2 only
			// when the previous turn's tools were "only_exec". Simpler:
			// iter 1 emits plan_tool_call; iter 2 from planner emits
			// only exec tools so iter 3 goes to editor.
			{
				Text:       "let me plan",
				ToolCalls:  []provider.ToolCall{{ID: "p1", Name: "plan", Input: json.RawMessage(`{}`)}},
				StopReason: "tool_use",
			},
			{
				Text: "executing edits",
				ToolCalls: []provider.ToolCall{
					{ID: "b1", Name: "bash", Input: json.RawMessage(`{}`)},
					{ID: "e1", Name: "edit", Input: json.RawMessage(`{}`)},
				},
				StopReason: "tool_use",
			},
		},
	}
	editorProv := &splitRecordingProvider{
		name: "editor-mock",
		turns: []provider.MockTurn{
			// iter 3: previous (iter 2) had only exec tools, so this is
			// editor. We emit a mixed call so iter 4 falls back to main.
			{
				Text: "running mixed",
				ToolCalls: []provider.ToolCall{
					{ID: "b2", Name: "bash", Input: json.RawMessage(`{}`)},
					{ID: "s1", Name: "subagent", Input: json.RawMessage(`{}`)},
				},
				StopReason: "tool_use",
			},
		},
	}
	mainProv := &splitRecordingProvider{
		name: "main-mock",
		turns: []provider.MockTurn{
			// iter 4: previous turn was mixed, so this is main. Emit no
			// tools to trigger the end_turn / verifier path and let the
			// loop exit cleanly.
			{Text: "all done", StopReason: "end_turn"},
		},
	}

	stream := event.NewStream()
	sub := stream.Subscribe(256)
	defer sub.Close()

	loop := &AgentLoop{
		Spec: &gilv1.FrozenSpec{
			Goal:         &gilv1.Goal{OneLiner: "split test"},
			Verification: &gilv1.Verification{}, // no checks → vacuous pass on no-tool turn
			Budget:       &gilv1.Budget{MaxIterations: 8},
		},
		// .Provider/.Model serve as the legacy fallback. The maps below
		// override per-role.
		Provider: mainProv,
		Model:    "main-fallback",
		Providers: map[string]provider.Provider{
			RolePlanner: plannerProv,
			RoleEditor:  editorProv,
			RoleMain:    mainProv,
		},
		Models: map[string]string{
			RolePlanner: "claude-opus",
			RoleEditor:  "qwen-27b",
			RoleMain:    "gpt-4o",
		},
		Tools: []tool.Tool{
			&splitNoopTool{name: "plan"},
			&splitNoopTool{name: "bash"},
			&splitNoopTool{name: "edit"},
			&splitNoopTool{name: "subagent"},
		},
		Verifier: verify.NewRunner(""),
		Events:   stream,
	}

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "done", res.Status, "expected loop to end cleanly")

	// Iter 1 → planner, iter 2 → planner (plan tool was called), iter 3 →
	// editor (iter 2 had only exec tools), iter 4 → main (iter 3 mixed).
	plannerProv.mu.Lock()
	require.GreaterOrEqual(t, len(plannerProv.calls), 2, "planner should have driven iter 1 + iter 2")
	require.Equal(t, "claude-opus", plannerProv.calls[0].Model)
	require.Equal(t, "claude-opus", plannerProv.calls[1].Model)
	plannerProv.mu.Unlock()

	editorProv.mu.Lock()
	require.GreaterOrEqual(t, len(editorProv.calls), 1, "editor should have driven iter 3")
	require.Equal(t, "qwen-27b", editorProv.calls[0].Model)
	editorProv.mu.Unlock()

	mainProv.mu.Lock()
	require.GreaterOrEqual(t, len(mainProv.calls), 1, "main should have driven iter 4")
	require.Equal(t, "gpt-4o", mainProv.calls[0].Model)
	mainProv.mu.Unlock()

	// Drain events and assert at least three model_switched events fired
	// (planner → editor at iter 3, editor → main at iter 4, plus the
	// initial empty→planner at iter 1). We close the subscription and
	// drain remaining buffered events; sub.Events() returns once the
	// subscription is removed.
	var switches []map[string]any
drain1:
	for {
		select {
		case e := <-sub.Events():
			if e.Type == "model_switched" {
				var d map[string]any
				require.NoError(t, json.Unmarshal(e.Data, &d))
				switches = append(switches, d)
			}
		default:
			break drain1
		}
	}
	require.GreaterOrEqual(t, len(switches), 3,
		"expected ≥3 model_switched events (initial+planner→editor+editor→main); got %d", len(switches))

	// First switch: from "" to planner.
	require.Equal(t, "", switches[0]["from"])
	require.Equal(t, "planner", switches[0]["to"])
	require.Equal(t, "first_turn", switches[0]["reason"])

	// Verify ByRole tracking.
	require.NotNil(t, res.ByRole)
	require.Greater(t, res.ByRole[RolePlanner].Calls, 0, "planner role usage should be tracked")
	require.Greater(t, res.ByRole[RoleEditor].Calls, 0, "editor role usage should be tracked")
	require.Greater(t, res.ByRole[RoleMain].Calls, 0, "main role usage should be tracked")
	// All three roles must have positive token counts.
	require.Greater(t, res.ByRole[RolePlanner].InputTokens+res.ByRole[RolePlanner].OutputTokens, int64(0))
	require.Greater(t, res.ByRole[RoleEditor].InputTokens+res.ByRole[RoleEditor].OutputTokens, int64(0))
	require.Greater(t, res.ByRole[RoleMain].InputTokens+res.ByRole[RoleMain].OutputTokens, int64(0))
}

// TestAgentLoop_Split_LegacyFallbackSingleProvider verifies that a loop
// constructed without Providers/Models maps still works exactly as before
// — every Complete call lands on a.Provider with a.Model. This is the
// backwards-compat guarantee that protects the existing single-provider
// CLI / e2e flow.
func TestAgentLoop_Split_LegacyFallbackSingleProvider(t *testing.T) {
	t.Parallel()

	prov := &splitRecordingProvider{
		name: "single-mock",
		turns: []provider.MockTurn{
			{Text: "done", StopReason: "end_turn"},
		},
	}

	loop := &AgentLoop{
		Spec: &gilv1.FrozenSpec{
			Verification: &gilv1.Verification{},
			Budget:       &gilv1.Budget{MaxIterations: 3},
		},
		Provider: prov,
		Model:    "only-model",
		// Deliberately NO Providers/Models maps — exercise the fallback.
		Tools:    []tool.Tool{},
		Verifier: verify.NewRunner(""),
	}

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	prov.mu.Lock()
	defer prov.mu.Unlock()
	require.Greater(t, len(prov.calls), 0, "single provider should still be called")
	for i, c := range prov.calls {
		require.Equal(t, "only-model", c.Model, "call #%d expected fallback model", i)
	}

	// ByRole still tracks the single role used (planner for iter 1).
	require.NotNil(t, res.ByRole)
	totalCalls := 0
	for _, u := range res.ByRole {
		totalCalls += u.Calls
	}
	require.Greater(t, totalCalls, 0, "ByRole should aggregate at least one call")
}

// TestAgentLoop_Split_PartialMapsFallToMain covers the case where the
// user wires a planner override but leaves editor unset — editor turns
// should fall through to a.Provider/a.Model rather than panic.
func TestAgentLoop_Split_PartialMapsFallToMain(t *testing.T) {
	t.Parallel()

	plannerProv := &splitRecordingProvider{
		name: "planner-mock",
		turns: []provider.MockTurn{
			{
				Text: "edits incoming",
				ToolCalls: []provider.ToolCall{
					{ID: "b1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				StopReason: "tool_use",
			},
		},
	}
	mainProv := &splitRecordingProvider{
		name: "main-mock",
		turns: []provider.MockTurn{
			{Text: "fallback worked", StopReason: "end_turn"},
		},
	}

	loop := &AgentLoop{
		Spec: &gilv1.FrozenSpec{
			Verification: &gilv1.Verification{},
			Budget:       &gilv1.Budget{MaxIterations: 5},
		},
		Provider: mainProv,
		Model:    "main-default",
		Providers: map[string]provider.Provider{
			RolePlanner: plannerProv,
			// editor + main NOT set → falls back to a.Provider
		},
		Models: map[string]string{
			RolePlanner: "opus",
			// editor + main NOT set → falls back to a.Model
		},
		Tools:    []tool.Tool{&splitNoopTool{name: "bash"}},
		Verifier: verify.NewRunner(""),
	}

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	// Planner drove iter 1 (claude-opus model).
	plannerProv.mu.Lock()
	require.GreaterOrEqual(t, len(plannerProv.calls), 1)
	require.Equal(t, "opus", plannerProv.calls[0].Model)
	plannerProv.mu.Unlock()

	// Iter 2's role would be editor (only-exec last turn) but editor is
	// not in the map → falls back to a.Provider (mainProv) with a.Model
	// ("main-default").
	mainProv.mu.Lock()
	require.GreaterOrEqual(t, len(mainProv.calls), 1)
	require.Equal(t, "main-default", mainProv.calls[0].Model,
		"editor fell back to a.Model when not wired in Models map")
	mainProv.mu.Unlock()
}

// TestModelSwitchReason exercises every branch of the reason classifier
// so the human-readable explanation in model_switched events stays stable.
func TestModelSwitchReason(t *testing.T) {
	t.Parallel()

	require.Equal(t, "first_turn", modelSwitchReason(0, nil, RolePlanner))
	require.Equal(t, "first_turn", modelSwitchReason(0, &provider.Response{}, RolePlanner))

	planResp := &provider.Response{ToolCalls: []provider.ToolCall{{Name: "plan"}}}
	require.Equal(t, "plan_tool_call", modelSwitchReason(2, planResp, RolePlanner))

	execResp := &provider.Response{ToolCalls: []provider.ToolCall{{Name: "bash"}}}
	require.Equal(t, "tool_heavy", modelSwitchReason(3, execResp, RoleEditor))

	mixedResp := &provider.Response{ToolCalls: []provider.ToolCall{
		{Name: "bash"}, {Name: "subagent"},
	}}
	require.Equal(t, "ambiguous_turn", modelSwitchReason(4, mixedResp, RoleMain))

	// When neither signal matches and role is planner/editor (e.g., a
	// future caller wires in extra rules), the reason falls through to
	// the role-specific default.
	noToolResp := &provider.Response{Text: "just thinking"}
	require.Equal(t, "planner_default", modelSwitchReason(5, noToolResp, RolePlanner))
	require.Equal(t, "editor_default", modelSwitchReason(6, noToolResp, RoleEditor))
}

// TestExecToolNamesContainsCriticalEditTools is a guard so a future
// refactor doesn't accidentally drop bash/edit/write_file from the
// classifier set — that would silently route most production runs to
// the main role and defeat the architect/coder split.
func TestExecToolNamesContainsCriticalEditTools(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"bash", "edit", "write_file", "apply_patch"} {
		require.True(t, execToolNames[name], "execToolNames missing %q — split is broken", name)
	}
	// Negative guard: tools that explicitly should NOT count as exec.
	for _, name := range []string{"plan", "subagent", "repomap", "lsp", "web_search", "web_fetch"} {
		require.False(t, execToolNames[name], "execToolNames should NOT include %q", name)
	}
}

// Sanity: the package's exported role constants are wired correctly so
// surface code (server, TUI) can refer to them by name.
func TestRoleConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, "planner", RolePlanner)
	require.Equal(t, "editor", RoleEditor)
	require.Equal(t, "main", RoleMain)
}

// TestAgentLoop_Split_ModelSwitchedEventCarriesModel verifies the
// model_switched payload includes the model id (not just the role) so
// log greppers can correlate spend by absolute model.
func TestAgentLoop_Split_ModelSwitchedEventCarriesModel(t *testing.T) {
	t.Parallel()

	prov := &splitRecordingProvider{
		name: "mock",
		turns: []provider.MockTurn{
			{Text: "ok", StopReason: "end_turn"},
		},
	}

	stream := event.NewStream()
	sub := stream.Subscribe(64)
	defer sub.Close()

	loop := &AgentLoop{
		Spec: &gilv1.FrozenSpec{
			Verification: &gilv1.Verification{},
			Budget:       &gilv1.Budget{MaxIterations: 2},
		},
		Provider: prov,
		Model:    "fallback-model",
		Models: map[string]string{
			RolePlanner: "the-planner-model",
		},
		Verifier: verify.NewRunner(""),
		Events:   stream,
	}

	_, err := loop.Run(context.Background())
	require.NoError(t, err)

	var sawSwitch bool
drain2:
	for {
		select {
		case e := <-sub.Events():
			if e.Type != "model_switched" {
				continue
			}
			var d map[string]any
			require.NoError(t, json.Unmarshal(e.Data, &d))
			// Iter 1 picks planner — model id should be the wired override.
			if to, ok := d["to"].(string); ok && strings.Contains(to, "planner") {
				require.Equal(t, "the-planner-model", d["model"])
				sawSwitch = true
			}
		default:
			break drain2
		}
	}
	require.True(t, sawSwitch, "expected at least one model_switched event with role=planner + the wired model id")
}
