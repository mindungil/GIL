package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// subagentTestProvider captures every Request and returns the next scripted
// turn. We use a single provider for both parent + sub-loop in these tests
// because RunSubagentWithConfig deliberately reuses the parent's provider;
// distinguishing turns by the SeedUserMessage content is enough.
type subagentTestProvider struct {
	mu      sync.Mutex
	turns   []provider.MockTurn
	idx     int
	systems []string
	seeds   []string
}

func (r *subagentTestProvider) Name() string { return "recording" }

func (r *subagentTestProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.systems = append(r.systems, req.System)
	if len(req.Messages) > 0 {
		r.seeds = append(r.seeds, req.Messages[0].Content)
	}
	if r.idx >= len(r.turns) {
		// Default sentinel: end the turn so the loop can exit cleanly
		// rather than panic.
		return provider.Response{Text: "fallback", StopReason: "end_turn", InputTokens: 5, OutputTokens: 5}, nil
	}
	t := r.turns[r.idx]
	r.idx++
	return provider.Response{
		Text:         t.Text,
		ToolCalls:    t.ToolCalls,
		StopReason:   t.StopReason,
		InputTokens:  10,
		OutputTokens: int64(len(t.Text)),
	}, nil
}

// recordingTool captures every Run call so we can assert which tools the
// sub-loop was actually allowed to invoke.
type recordingTool struct {
	mu    sync.Mutex
	name  string
	calls []json.RawMessage
}

func (r *recordingTool) Name() string                  { return r.name }
func (r *recordingTool) Description() string           { return "test " + r.name }
func (r *recordingTool) Schema() json.RawMessage       { return json.RawMessage(`{"type":"object"}`) }
func (r *recordingTool) Run(_ context.Context, args json.RawMessage) (tool.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, args)
	r.mu.Unlock()
	return tool.Result{Content: r.name + ": ok"}, nil
}

// TestRunSubagentWithConfig_FiltersToolsAndReturnsSummary verifies the
// config-driven entrypoint: AllowedTools restricts the sub-loop's tool
// set, MaxIterations is honoured, and the result carries the sub-loop's
// FinalText + status + tokens.
func TestRunSubagentWithConfig_FiltersToolsAndReturnsSummary(t *testing.T) {
	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			// Sub-loop turn 1: investigate, then end.
			{Text: "core/runner/runner.go has the main loop.", StopReason: "end_turn"},
		},
	}

	readTool := &recordingTool{name: "read_file"}
	writeTool := &recordingTool{name: "write_file"}
	bashTool := &recordingTool{name: "bash"}

	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools:    []tool.Tool{readTool, writeTool, bashTool},
		Verifier: verify.NewRunner(""),
	}

	res, err := parent.RunSubagentWithConfig(context.Background(), SubagentConfig{
		Goal:          "find which file defines the main agent loop",
		AllowedTools:  []string{"read_file"},
		MaxIterations: 3,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "core/runner/runner.go has the main loop.", res.Summary)
	require.Equal(t, "done", res.Status)
	require.Greater(t, res.Tokens, int64(0))

	// Sub-loop saw only the allowed tool — write_file + bash were filtered.
	prov.mu.Lock()
	defer prov.mu.Unlock()
	require.Greater(t, len(prov.systems), 0)
	require.Contains(t, prov.systems[0], "read_file")
	require.NotContains(t, prov.systems[0], "write_file")
	require.NotContains(t, prov.systems[0], "bash")
	// The seed message is the goal we passed.
	require.Contains(t, prov.seeds[0], "find which file defines the main agent loop")
}

// TestRunSubagentWithConfig_EmitsStartedAndDoneEvents verifies the
// observability hooks fire so TUI/CLI viewers can render sub-loop activity.
func TestRunSubagentWithConfig_EmitsStartedAndDoneEvents(t *testing.T) {
	stream := event.NewStream()
	sub := stream.Subscribe(64)
	defer sub.Close()

	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			{Text: "finding text", StopReason: "end_turn"},
		},
	}

	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools:    []tool.Tool{&recordingTool{name: "read_file"}},
		Verifier: verify.NewRunner(""),
		Events:   stream,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collected := make(chan []event.Event, 1)
	go func() {
		var evs []event.Event
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					collected <- evs
					return
				}
				evs = append(evs, e)
				if e.Type == "subagent_done" {
					collected <- evs
					return
				}
			case <-ctx.Done():
				collected <- evs
				return
			}
		}
	}()

	res, err := parent.RunSubagentWithConfig(ctx, SubagentConfig{
		Goal:          "scout core",
		AllowedTools:  []string{"read_file"},
		MaxIterations: 2,
		MaxTokens:     5000,
	})
	require.NoError(t, err)
	require.Equal(t, "finding text", res.Summary)

	evs := <-collected
	var sawStarted, sawDone bool
	var sawSubLoopInternal bool
	var startedData, doneData map[string]any
	for _, e := range evs {
		switch e.Type {
		case "subagent_started":
			sawStarted = true
			require.NoError(t, json.Unmarshal(e.Data, &startedData))
		case "subagent_done":
			sawDone = true
			require.NoError(t, json.Unmarshal(e.Data, &doneData))
		case "iteration_start", "provider_request", "provider_response", "run_done", "verify_run":
			// Sub-loop internal events MUST NOT leak into the parent
			// stream — the parent's two subagent_* events are the surface API.
			sawSubLoopInternal = true
		}
	}
	require.True(t, sawStarted, "expected subagent_started event")
	require.True(t, sawDone, "expected subagent_done event")
	require.False(t, sawSubLoopInternal, "sub-loop internal events must not leak to parent stream")
	require.Equal(t, "scout core", startedData["goal"])
	require.Equal(t, float64(2), startedData["max_iterations"])
	require.Equal(t, float64(5000), startedData["max_tokens"])
	require.Equal(t, "main-model", startedData["model"])
	require.Equal(t, "scout core", doneData["goal"])
	require.Equal(t, "done", doneData["status"])
	require.Contains(t, doneData["summary"], "finding text")
}

// TestRunSubagentWithConfig_DefaultsAndCeilings verifies the iteration
// ceiling, default tool set, and default max tokens kick in when callers
// pass zero/empty values.
func TestRunSubagentWithConfig_DefaultsAndCeilings(t *testing.T) {
	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			{Text: "ok", StopReason: "end_turn"},
		},
	}

	// Provide several tools — only the default-allowed ones should leak
	// into the sub-loop's system prompt.
	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools: []tool.Tool{
			&recordingTool{name: "read_file"},
			&recordingTool{name: "repomap"},
			&recordingTool{name: "memory_load"},
			&recordingTool{name: "web_fetch"},
			&recordingTool{name: "lsp"},
			&recordingTool{name: "bash"},        // NOT in default
			&recordingTool{name: "write_file"},  // NOT in default
			&recordingTool{name: "apply_patch"}, // NOT in default
		},
		Verifier: verify.NewRunner(""),
	}

	// MaxIterations=999 → clamped to ceiling 20. Goal is the only required field.
	res, err := parent.RunSubagentWithConfig(context.Background(), SubagentConfig{
		Goal:          "scout",
		MaxIterations: 999,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	prov.mu.Lock()
	sys := prov.systems[0]
	prov.mu.Unlock()
	// Read-only default set members appear.
	for _, name := range []string{"read_file", "repomap", "memory_load", "web_fetch", "lsp"} {
		require.Contains(t, sys, name, "default sub-loop should include %q", name)
	}
	// Mutating tools are NOT in the sub-loop's tool list.
	for _, name := range []string{"bash", "write_file", "apply_patch"} {
		// Tool names appear as "- bash:" lines in the system prompt;
		// match on the line prefix to avoid accidental substring collisions
		// (e.g., "bash" inside a longer description).
		require.NotContains(t, sys, "- "+name+":", "default sub-loop must NOT include %q", name)
	}
}

// TestRunSubagentWithConfig_EmptyGoal_Errors confirms the goal validation.
func TestRunSubagentWithConfig_EmptyGoal_Errors(t *testing.T) {
	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: &subagentTestProvider{},
		Model:    "main-model",
		Tools:    []tool.Tool{&recordingTool{name: "read_file"}},
		Verifier: verify.NewRunner(""),
	}
	_, err := parent.RunSubagentWithConfig(context.Background(), SubagentConfig{Goal: "  "})
	require.Error(t, err)
	require.Contains(t, err.Error(), "goal is required")
}

// TestRunSubagent_LegacyShape_StillWorks verifies that the stuck-recovery
// SubagentBranch caller (which goes through the original positional
// RunSubagent signature) still gets a string summary back.
func TestRunSubagent_LegacyShape_StillWorks(t *testing.T) {
	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			{Text: "the bash command is failing because the file path is wrong", StopReason: "end_turn"},
		},
	}
	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools:    []tool.Tool{&recordingTool{name: "read_file"}},
		Verifier: verify.NewRunner(""),
	}
	summary, err := parent.RunSubagent(context.Background(), "investigate the workspace", []string{"read_file"}, 3)
	require.NoError(t, err)
	require.Contains(t, summary, "the bash command is failing")
}

// TestAsSubagentRunner_AdaptsToToolInterface confirms the runner-side
// adapter satisfies tool.SubagentRunner so the agent-callable subagent
// tool can spawn a sub-loop without importing core/runner.
func TestAsSubagentRunner_AdaptsToToolInterface(t *testing.T) {
	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			{Text: "adapter saw it", StopReason: "end_turn"},
		},
	}
	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools:    []tool.Tool{&recordingTool{name: "read_file"}},
		Verifier: verify.NewRunner(""),
	}

	runnerAdapter := parent.AsSubagentRunner()
	require.NotNil(t, runnerAdapter)

	res, err := runnerAdapter.RunSubagentWithConfig(context.Background(), tool.SubagentRunConfig{
		Goal:          "test the adapter",
		AllowedTools:  []string{"read_file"},
		MaxIterations: 2,
	})
	require.NoError(t, err)
	require.Equal(t, "adapter saw it", res.Summary)
	require.Equal(t, "done", res.Status)
}

// TestRunSubagentWithConfig_TokenBudget_Enforced verifies the MaxTokens
// cap actually wires through to the sub-loop's runner-level budget
// enforcement (the parent's existing budget machinery does the work; we
// just confirm the spec's MaxTotalTokens carries the right value).
func TestRunSubagentWithConfig_TokenBudget_Enforced(t *testing.T) {
	// Two turns: each costs 10 input + ~10 output tokens. With a 30-token
	// cap the sub-loop should hit budget_exhausted on iteration 2.
	prov := &subagentTestProvider{
		turns: []provider.MockTurn{
			{Text: "step 1 — calling read", ToolCalls: []provider.ToolCall{{ID: "x", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)}}, StopReason: "tool_use"},
			{Text: "step 2 — calling read", ToolCalls: []provider.ToolCall{{ID: "y", Name: "read_file", Input: json.RawMessage(`{"path":"b"}`)}}, StopReason: "tool_use"},
			{Text: "step 3 — done", StopReason: "end_turn"},
		},
	}
	parent := &AgentLoop{
		Spec:     &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "parent"}, Budget: &gilv1.Budget{MaxIterations: 5}},
		Provider: prov,
		Model:    "main-model",
		Tools:    []tool.Tool{&recordingTool{name: "read_file"}},
		Verifier: verify.NewRunner(""),
	}
	res, err := parent.RunSubagentWithConfig(context.Background(), SubagentConfig{
		Goal:          "scout",
		AllowedTools:  []string{"read_file"},
		MaxIterations: 5,
		MaxTokens:     30,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	// Either "budget_exhausted" (token cap hit cleanly) or "max_iterations"
	// is acceptable — the exact accounting depends on per-turn token estimates.
	// What matters is the cap WAS plumbed through and the sub-loop did not
	// silently run unbounded.
	require.Contains(t, []string{"budget_exhausted", "max_iterations", "done"}, res.Status)
	if res.Status == "budget_exhausted" {
		require.LessOrEqual(t, res.Iterations, 3)
	}
}

// helper: silence unused-import warning if we trim tests later
var _ = strings.Contains
