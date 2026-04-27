package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/compact"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/instructions"
	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/permission"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/stuck"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

// loopProvider is a test provider that forever returns the same scripted turn.
type loopProvider struct {
	turn provider.MockTurn
}

func (l *loopProvider) Name() string { return "loop-mock" }
func (l *loopProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{
		Text:         l.turn.Text,
		ToolCalls:    l.turn.ToolCalls,
		StopReason:   l.turn.StopReason,
		InputTokens:  10,
		OutputTokens: int64(len(l.turn.Text)),
	}, nil
}

func TestAgentLoop_HelloWorld_Done(t *testing.T) {
	dir := t.TempDir()

	mock := provider.NewMockToolProvider([]provider.MockTurn{
		// Turn 1: write_file
		{
			Text: "Creating hello.go",
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_1",
					Name: "write_file",
					Input: json.RawMessage(`{"path":"hello.go","content":"package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"hello, world\")}"}`),
				},
			},
			StopReason: "tool_use",
		},
		// Turn 2: run go run
		{
			Text: "Verifying",
			ToolCalls: []provider.ToolCall{
				{
					ID:    "call_2",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"go run hello.go"}`),
				},
			},
			StopReason: "tool_use",
		},
		// Turn 3: stop, let verifier run
		{Text: "Done.", StopReason: "end_turn"},
	})

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "create hello.go that prints hello, world"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello.go", ExpectedExitCode: 0},
				{Name: "runs", Kind: gilv1.CheckKind_SHELL, Command: "go run hello.go | grep -q 'hello, world'", ExpectedExitCode: 0},
			},
		},
		Budget: &gilv1.Budget{MaxIterations: 5},
	}

	tools := []tool.Tool{
		&tool.WriteFile{WorkingDir: dir},
		&tool.Bash{WorkingDir: dir},
	}
	loop := NewAgentLoop(spec, mock, "test-model", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	require.Equal(t, 3, res.Iterations)
	require.Len(t, res.VerifyAll, 2)
	for _, vr := range res.VerifyAll {
		require.True(t, vr.Passed, "%s: %v", vr.Name, vr)
	}
}

func TestAgentLoop_MaxIterations(t *testing.T) {
	// Mock that always returns tool_call (never stops)
	mock := provider.NewMockToolProvider([]provider.MockTurn{
		{
			ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
			StopReason: "tool_use",
		},
		{
			ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
			StopReason: "tool_use",
		},
		{
			ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
			StopReason: "tool_use",
		},
	})
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 3},
	}
	tools := []tool.Tool{&tool.Bash{WorkingDir: t.TempDir()}}
	loop := NewAgentLoop(spec, mock, "x", tools, verify.NewRunner(t.TempDir()))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "max_iterations", res.Status)
	require.Equal(t, 3, res.Iterations)
}

func TestAgentLoop_VerifyFailureFeedsBack(t *testing.T) {
	dir := t.TempDir()
	mock := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "Done", StopReason: "end_turn"}, // turn 1: skip tools, verify will fail
		{
			Text: "Trying again",
			ToolCalls: []provider.ToolCall{
				{ID: "x", Name: "write_file", Input: json.RawMessage(`{"path":"hello","content":"hi"}`)},
			},
			StopReason: "tool_use",
		},
		{Text: "Done now", StopReason: "end_turn"}, // turn 3: verify passes
	})
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "create hello"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 5},
	}
	tools := []tool.Tool{&tool.WriteFile{WorkingDir: dir}}
	loop := NewAgentLoop(spec, mock, "x", tools, verify.NewRunner(dir))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	require.Equal(t, 3, res.Iterations)
}

func TestAgentLoop_NilVerification_TreatsAsAllPass(t *testing.T) {
	mock := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "I'm done", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "do nothing"},
		// Verification is nil → no checks → vacuously pass
		Budget: &gilv1.Budget{MaxIterations: 3},
	}
	loop := NewAgentLoop(spec, mock, "x", nil, verify.NewRunner(t.TempDir()))
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	require.Equal(t, 1, res.Iterations)
}

func TestAgentLoop_SystemPromptIncludesChecks(t *testing.T) {
	tools := []tool.Tool{&tool.Bash{WorkingDir: "/tmp"}}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "build hello"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello"},
			},
		},
	}
	prompt := buildSystemPrompt(spec, tools, nil, "")
	require.Contains(t, prompt, "build hello")
	require.Contains(t, prompt, "exists")
	require.Contains(t, prompt, "test -f hello")
	require.Contains(t, prompt, "bash")
}

func TestAgentLoop_EmitsEventsToStream(t *testing.T) {
	dir := t.TempDir()
	mock := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "Creating", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hello"}`)},
		}, StopReason: "tool_use"},
		{Text: "Done", StopReason: "end_turn"},
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "create file"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f " + dir + "/hello.txt"}}},
		Budget:       &gilv1.Budget{MaxIterations: 5},
	}
	tools := []tool.Tool{&tool.WriteFile{WorkingDir: dir}, &tool.Bash{WorkingDir: dir}}

	stream := event.NewStream()
	sub := stream.Subscribe(64)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:     spec,
		Provider: mock,
		Model:    "test",
		Tools:    tools,
		Verifier: verify.NewRunner(dir),
		Events:   stream,
	}

	go func() {
		_, _ = loop.Run(context.Background())
	}()

	// Collect events for up to 2 seconds
	collected := []event.Event{}
	timeout := time.After(2 * time.Second)
collectLoop:
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				break collectLoop
			}
			collected = append(collected, e)
			if e.Type == "run_done" || e.Type == "run_max_iterations" || e.Type == "run_error" {
				break collectLoop
			}
		case <-timeout:
			break collectLoop
		}
	}

	require.NotEmpty(t, collected, "expected events to be emitted")

	types := map[string]int{}
	for _, e := range collected {
		types[e.Type]++
	}
	require.Greater(t, types["iteration_start"], 0, "got events: %+v", types)
	require.Greater(t, types["provider_request"], 0)
	require.Greater(t, types["tool_call"], 0)
	require.Greater(t, types["tool_result"], 0)
	require.Greater(t, types["verify_result"], 0)
	require.Greater(t, types["run_done"], 0)
}

// TestAgentLoop_StuckAbortsAfterThreshold verifies that without a recovery
// strategy the loop aborts with status="stuck" after StuckThreshold unrecovered
// signals, well before MaxIterations.
func TestAgentLoop_StuckAbortsAfterThreshold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Always emit the exact same bash tool call so PatternRepeatedActionObservation
	// fires as soon as 4 identical pairs accumulate.
	mock := &loopProvider{turn: provider.MockTurn{
		Text:       "looping",
		ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
		StopReason: "tool_use",
	}}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 30},
	}
	tools := []tool.Tool{&tool.Bash{WorkingDir: t.TempDir()}}

	loop := &AgentLoop{
		Spec:          spec,
		Provider:      mock,
		Model:         "m1",
		Tools:         tools,
		Verifier:      verify.NewRunner(t.TempDir()),
		StuckDetector: &stuck.Detector{Window: 50},
		// No StuckStrategy — every detected signal is unrecovered.
		StuckThreshold: 3,
	}

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, "stuck", res.Status)
	require.Less(t, res.Iterations, 30, "expected early abort, got %d iterations", res.Iterations)
	require.NotNil(t, res.FinalError)
}

// TestAgentLoop_StuckRecoversViaModelEscalate verifies that ModelEscalateStrategy
// swaps a.Model on each detection, and after exhausting the chain the loop
// eventually aborts with status="stuck". The stream must contain 2 stuck_recovered
// events for models "m2" and "m3".
func TestAgentLoop_StuckRecoversViaModelEscalate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mock := &loopProvider{turn: provider.MockTurn{
		Text:       "looping",
		ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
		StopReason: "tool_use",
	}}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 100},
	}
	tools := []tool.Tool{&tool.Bash{WorkingDir: t.TempDir()}}

	stream := event.NewStream()
	sub := stream.Subscribe(256)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       mock,
		Model:          "m1",
		Tools:          tools,
		Verifier:       verify.NewRunner(t.TempDir()),
		Events:         stream,
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.ModelEscalateStrategy{},
		ModelChain:     []string{"m1", "m2", "m3"},
		StuckThreshold: 3,
	}

	// Collect all events in a goroutine before (and while) the loop runs.
	var mu sync.Mutex
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, "stuck", res.Status)

	// Wait for collector to drain.
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	// Count stuck_recovered events and check new_model values.
	type recoveredInfo struct{ newModel string }
	var recoveries []recoveredInfo
	for _, e := range evs {
		if e.Type != "stuck_recovered" {
			continue
		}
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		nm, _ := d["new_model"].(string)
		recoveries = append(recoveries, recoveredInfo{nm})
	}

	require.GreaterOrEqual(t, len(recoveries), 2, "expected at least 2 model switches, got %d", len(recoveries))

	// Verify m2 was escalated to before m3 (order may have more than 2 entries
	// if the detector fires on the same iteration for different patterns).
	models := make([]string, len(recoveries))
	for i, r := range recoveries {
		models[i] = r.newModel
	}
	require.Contains(t, models, "m2")
	require.Contains(t, models, "m3")
}

// TestAgentLoop_NoDetectorMeansNoStuckAbort verifies that when StuckDetector is
// nil the loop runs to MaxIterations with no early abort despite the same loopy
// provider.
func TestAgentLoop_NoDetectorMeansNoStuckAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mock := &loopProvider{turn: provider.MockTurn{
		Text:       "looping",
		ToolCalls:  []provider.ToolCall{{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"echo loop"}`)}},
		StopReason: "tool_use",
	}}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 10},
	}
	tools := []tool.Tool{&tool.Bash{WorkingDir: t.TempDir()}}

	loop := &AgentLoop{
		Spec:          spec,
		Provider:      mock,
		Model:         "m1",
		Tools:         tools,
		Verifier:      verify.NewRunner(t.TempDir()),
		StuckDetector: nil, // no detector → no abort
	}

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, "max_iterations", res.Status)
	require.Equal(t, 10, res.Iterations)
}

// TestAgentLoop_CheckpointsCommittedPerToolIteration verifies that ShadowGit
// receives a commit after each tool-using iteration and a final commit at done.
func TestAgentLoop_CheckpointsCommittedPerToolIteration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	workDir := t.TempDir()
	baseDir := t.TempDir()

	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "a", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"a"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "b", Name: "write_file", Input: json.RawMessage(`{"path":"b.txt","content":"b"}`)}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "write files"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{}},
		Budget:       &gilv1.Budget{MaxIterations: 10},
	}

	tools := []tool.Tool{&tool.WriteFile{WorkingDir: workDir}}
	sg := checkpoint.New(workDir, baseDir)

	stream := event.NewStream()
	sub := stream.Subscribe(128)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:       spec,
		Provider:   prov,
		Model:      "test",
		Tools:      tools,
		Verifier:   verify.NewRunner(workDir),
		Events:     stream,
		Checkpoint: sg,
	}

	// Collect events while the loop runs.
	var mu sync.Mutex
	var collected []event.Event
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "run_done" || e.Type == "run_max_iterations" || e.Type == "run_error" {
					return
				}
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	select {
	case <-collectorDone:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	// Count checkpoint_committed events and verify sha fields are non-empty.
	var checkpointEvents []event.Event
	for _, e := range evs {
		if e.Type == "checkpoint_committed" {
			checkpointEvents = append(checkpointEvents, e)
		}
	}
	require.GreaterOrEqual(t, len(checkpointEvents), 2, "expected at least 2 checkpoint_committed events")

	for _, e := range checkpointEvents {
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		sha, _ := d["sha"].(string)
		require.NotEmpty(t, sha, "checkpoint_committed event missing sha: %s", string(e.Data))
	}

	// Verify commits exist in the shadow git.
	commits, err := sg.ListCommits(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(commits), 3, "expected at least 3 commits (2 tool iters + 1 final)")
}

// TestAgentLoop_NoCheckpointWithoutField verifies that nil Checkpoint causes no
// checkpoint events and no errors.
func TestAgentLoop_NoCheckpointWithoutField(t *testing.T) {
	workDir := t.TempDir()

	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "a", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"a"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "b", Name: "write_file", Input: json.RawMessage(`{"path":"b.txt","content":"b"}`)}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "write files"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{}},
		Budget:       &gilv1.Budget{MaxIterations: 10},
	}

	tools := []tool.Tool{&tool.WriteFile{WorkingDir: workDir}}

	stream := event.NewStream()
	sub := stream.Subscribe(128)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:       spec,
		Provider:   prov,
		Model:      "test",
		Tools:      tools,
		Verifier:   verify.NewRunner(workDir),
		Events:     stream,
		Checkpoint: nil, // no checkpointing
	}

	var mu sync.Mutex
	var collected []event.Event
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "run_done" || e.Type == "run_max_iterations" || e.Type == "run_error" {
					return
				}
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	select {
	case <-collectorDone:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	for _, e := range evs {
		require.NotEqual(t, "checkpoint_committed", e.Type, "unexpected checkpoint event when Checkpoint is nil")
		require.NotEqual(t, "checkpoint_init", e.Type)
		require.NotEqual(t, "checkpoint_init_error", e.Type)
		require.NotEqual(t, "checkpoint_error", e.Type)
	}
}

// TestAgentLoop_CheckpointInitFailureSoftDisables verifies that a failing Init
// emits checkpoint_init_error but does NOT abort the run.
func TestAgentLoop_CheckpointInitFailureSoftDisables(t *testing.T) {
	workDir := t.TempDir()
	baseDir := t.TempDir()

	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "a", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"a"}`)}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "write files"},
		Verification: &gilv1.Verification{Checks: []*gilv1.Check{}},
		Budget:       &gilv1.Budget{MaxIterations: 10},
	}

	tools := []tool.Tool{&tool.WriteFile{WorkingDir: workDir}}

	// Build a ShadowGit with a non-existent git binary so Init will fail.
	sg := checkpoint.New(workDir, baseDir)
	sg.GitBin = "git-does-not-exist-xyz"

	stream := event.NewStream()
	sub := stream.Subscribe(128)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:       spec,
		Provider:   prov,
		Model:      "test",
		Tools:      tools,
		Verifier:   verify.NewRunner(workDir),
		Events:     stream,
		Checkpoint: sg,
	}

	var mu sync.Mutex
	var collected []event.Event
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "run_done" || e.Type == "run_max_iterations" || e.Type == "run_error" {
					return
				}
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status, "init failure should not abort run")

	select {
	case <-collectorDone:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	var initErrCount int
	for _, e := range evs {
		if e.Type == "checkpoint_init_error" {
			initErrCount++
		}
		require.NotEqual(t, "checkpoint_committed", e.Type, "no commits expected after init failure")
	}
	require.Equal(t, 1, initErrCount, "expected exactly one checkpoint_init_error event")
}

// noopTool is a minimal tool for use in compaction tests.
type noopTool struct{}

func (n *noopTool) Name() string                                             { return "noop" }
func (n *noopTool) Description() string                                      { return "no-op" }
func (n *noopTool) Schema() json.RawMessage                                  { return json.RawMessage(`{"type":"object"}`) }
func (n *noopTool) Run(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

func TestAgentLoop_AutoCompactsAtThreshold(t *testing.T) {
	// Provider that returns large text responses to inflate token count.
	bigText := strings.Repeat("x", 4000) // ~1000 tokens per response
	seq := []provider.MockTurn{}
	for i := 0; i < 10; i++ {
		seq = append(seq, provider.MockTurn{Text: bigText, StopReason: "tool_use", ToolCalls: []provider.ToolCall{{
			ID: fmt.Sprintf("c%d", i), Name: "noop", Input: json.RawMessage(`{}`),
		}}})
	}
	seq = append(seq, provider.MockTurn{Text: "done", StopReason: "end_turn"})
	prov := provider.NewMockToolProvider(seq)

	// Compactor with mock provider that returns a short summary.
	summaryProv := provider.NewMock([]string{"## Goal\n- summary"})
	compactor := &compact.Compactor{Provider: summaryProv, Model: "m", HeadKeep: 1, TailKeep: 2, MinMiddle: 2}

	tools := []tool.Tool{&noopTool{}}

	spec := &gilv1.FrozenSpec{Budget: &gilv1.Budget{MaxIterations: 12}, Verification: &gilv1.Verification{}}
	ver := verify.NewRunner(t.TempDir())

	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m", Tools: tools, Verifier: ver,
		Compactor: compactor, MaxContextTokens: 5000, // low threshold to trigger compaction
		Events: event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res)
	sub.Close()
	<-done

	// Assert at least one compact_done event was emitted.
	var compactCount int
	for _, e := range collected {
		if e.Type == "compact_done" {
			compactCount++
		}
	}
	require.Greater(t, compactCount, 0, "expected at least one compact_done event")
}

func TestAgentLoop_NoCompactWhenCompactorNil(t *testing.T) {
	// Same big-text provider, but Compactor=nil → no compact events.
	bigText := strings.Repeat("x", 4000)
	prov := provider.NewMockToolProvider([]provider.MockTurn{{Text: bigText, StopReason: "end_turn"}})
	spec := &gilv1.FrozenSpec{Budget: &gilv1.Budget{MaxIterations: 2}, Verification: &gilv1.Verification{}}
	ver := verify.NewRunner(t.TempDir())
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m", Tools: nil, Verifier: ver,
		Compactor: nil, MaxContextTokens: 100, // would trigger if enabled
		Events: event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	done := make(chan struct{})
	var collected []event.Event
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()
	_, err := loop.Run(context.Background())
	require.NoError(t, err)
	sub.Close()
	<-done
	for _, e := range collected {
		require.NotEqual(t, "compact_start", e.Type)
		require.NotEqual(t, "compact_done", e.Type)
	}
}

func TestAgentLoop_CompactNow_TriggersCompaction(t *testing.T) {
	// Provider returns one tool call to compact_now, then end_turn.
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "needing compact", ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "compact_now", Input: json.RawMessage(`{"reason":"test"}`),
		}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})
	summaryProv := provider.NewMock([]string{"summary"})
	compactor := &compact.Compactor{Provider: summaryProv, Model: "m", HeadKeep: 0, TailKeep: 0, MinMiddle: 0}
	spec := &gilv1.FrozenSpec{Budget: &gilv1.Budget{MaxIterations: 5}, Verification: &gilv1.Verification{}}
	ver := verify.NewRunner(t.TempDir())
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m", Verifier: ver,
		Compactor: compactor, MaxContextTokens: 10_000_000, // never triggers via threshold
		Events: event.NewStream(),
	}
	// Wire compact_now tool with the loop as the requester.
	loop.Tools = []tool.Tool{&tool.CompactNow{Requester: loop}}
	sub := loop.Events.Subscribe(256)
	done := make(chan struct{})
	var collected []event.Event
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()
	_, err := loop.Run(context.Background())
	require.NoError(t, err)
	sub.Close()
	<-done
	var sawForced bool
	for _, e := range collected {
		if e.Type == "compact_start" {
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			if v, ok := d["forced"].(bool); ok && v {
				sawForced = true
			}
		}
	}
	require.True(t, sawForced, "expected compact_start with forced=true after compact_now tool call")
}

func TestAgentLoop_PrependsMemoryBank_FullWhenSmall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	bank := memory.New(dir)
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "## Done\n- step 1\n"))
	require.NoError(t, bank.Write(memory.FileProjectBrief, "Build a CLI\n"))

	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	sysPrompt := buildSystemPrompt(spec, nil, bank, "")
	require.Contains(t, sysPrompt, "## Memory Bank")
	require.Contains(t, sysPrompt, "### projectbrief.md")
	require.Contains(t, sysPrompt, "Build a CLI")
	require.Contains(t, sysPrompt, "### progress.md")
	require.Contains(t, sysPrompt, "- step 1")
	require.NotContains(t, sysPrompt, "exceeds inline budget")
}

func TestAgentLoop_PrependsMemoryBank_OnlyProgressWhenLarge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	bank := memory.New(dir)
	require.NoError(t, bank.Init())
	// Make the bank exceed 4000 tokens (~16000 chars). Stuff techContext.
	big := strings.Repeat("xxxx", 5000) // 20000 chars ≈ 5000 tokens
	require.NoError(t, bank.Write(memory.FileTechContext, big))
	require.NoError(t, bank.Write(memory.FileProgress, "## Done\n- progress shown\n"))

	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	sysPrompt := buildSystemPrompt(spec, nil, bank, "")
	require.Contains(t, sysPrompt, "## Memory Bank")
	require.Contains(t, sysPrompt, "exceeds inline budget")
	require.Contains(t, sysPrompt, "### progress.md")
	require.Contains(t, sysPrompt, "- progress shown")
	// Content of techContext should NOT be inlined
	require.NotContains(t, sysPrompt, "xxxx")
	// But it should be listed
	require.Contains(t, sysPrompt, "techContext.md")
	require.Contains(t, sysPrompt, "memory_load")
}

func TestAgentLoop_NilBank_NoMemorySection(t *testing.T) {
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	sysPrompt := buildSystemPrompt(spec, nil, nil, "")
	require.NotContains(t, sysPrompt, "## Memory Bank")
	require.NotContains(t, sysPrompt, "memory_load")
}

func TestAgentLoop_EmptyBank_NoMemorySection(t *testing.T) {
	// Bank exists but has no files written yet
	bank := memory.New(filepath.Join(t.TempDir(), "memory")) // Init NOT called
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	sysPrompt := buildSystemPrompt(spec, nil, bank, "")
	require.NotContains(t, sysPrompt, "## Memory Bank")
}

func TestAgentLoop_MilestoneGate_AgentCallsMemoryUpdate(t *testing.T) {
	workspace := t.TempDir()
	bankDir := filepath.Join(workspace, "memory")
	bank := memory.New(bankDir)
	require.NoError(t, bank.Init())

	// Provider scripted: turn 1 = no tool calls (triggers verifier), turn 2 (milestone) = call memory_update
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "I think I'm done.", StopReason: "end_turn"}, // initial done attempt
		{ // milestone turn
			Text: "Recording progress.",
			ToolCalls: []provider.ToolCall{{
				ID:    "m1",
				Name:  "memory_update",
				Input: json.RawMessage(`{"file":"progress","content":"completed test step","section":"Done"}`),
			}},
			StopReason: "tool_use",
		},
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test"},
		Verification: &gilv1.Verification{}, // no checks → allPass=true
	}
	ver := verify.NewRunner(workspace)

	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "m",
		Tools:    []tool.Tool{&tool.MemoryUpdate{Bank: bank}, &tool.MemoryLoad{Bank: bank}},
		Verifier: ver,
		Memory:   bank,
		Events:   event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done

	// Bank should now contain the appended content
	progress, err := bank.Read(memory.FileProgress)
	require.NoError(t, err)
	require.Contains(t, progress, "completed test step")

	// Expect milestone events
	var startSeen, doneSeen, callsCount int
	for _, e := range collected {
		switch e.Type {
		case "memory_milestone_start":
			startSeen++
		case "memory_milestone_done":
			doneSeen++
		case "tool_call":
			callsCount++
		}
	}
	require.Equal(t, 1, startSeen)
	require.Equal(t, 1, doneSeen)
	require.GreaterOrEqual(t, callsCount, 1)
}

func TestAgentLoop_MilestoneGate_AgentSkips(t *testing.T) {
	workspace := t.TempDir()
	bank := memory.New(filepath.Join(workspace, "memory"))
	require.NoError(t, bank.Init())

	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "done", StopReason: "end_turn"},      // initial done
		{Text: "no update", StopReason: "end_turn"}, // milestone reply with no tools
	})

	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test"},
		Verification: &gilv1.Verification{},
	}
	ver := verify.NewRunner(workspace)
	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "m",
		Tools:    []tool.Tool{&tool.MemoryUpdate{Bank: bank}},
		Verifier: ver,
		Memory:   bank,
		Events:   event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done

	// memory_milestone_done was emitted with memory_calls=0
	var doneEvents int
	for _, e := range collected {
		if e.Type == "memory_milestone_done" {
			doneEvents++
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			require.Equal(t, float64(0), d["memory_calls"])
		}
	}
	require.Equal(t, 1, doneEvents)
}

func TestAgentLoop_MilestoneGate_SkippedWhenBankNil(t *testing.T) {
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "done", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	ver := verify.NewRunner(workspace)
	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "m",
		Verifier: ver,
		Memory:   nil,
		Events:   event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done
	for _, e := range collected {
		require.NotEqual(t, "memory_milestone_start", e.Type)
		require.NotEqual(t, "memory_milestone_done", e.Type)
	}
}

// milestoneFailingProvider returns the first turn normally, then errors on the
// milestone follow-up call (simulates a real network blip mid-milestone or a
// scripted scenario whose turns are exhausted just as the gate fires).
type milestoneFailingProvider struct {
	mu    sync.Mutex
	calls int
}

func (m *milestoneFailingProvider) Name() string { return "milestone-failing" }
func (m *milestoneFailingProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		// First call: agent declares done with no tool calls so the verifier
		// passes and we drop into the milestone gate.
		return provider.Response{
			Text:         "done",
			StopReason:   "end_turn",
			InputTokens:  10,
			OutputTokens: 4,
		}, nil
	}
	// Second call (the milestone follow-up): always error.
	return provider.Response{}, fmt.Errorf("provider unavailable: simulated network blip")
}

// TestAgentLoop_MilestoneGate_ProviderErrorFallsBack ensures that when the
// milestone summarizer's provider call errors, the run still completes
// successfully and we emit a NOTE-kind `memory_milestone_skipped` event
// (NOT a `*_error` event). This protects the run loop from being poisoned
// by best-effort summarization failures.
func TestAgentLoop_MilestoneGate_ProviderErrorFallsBack(t *testing.T) {
	workspace := t.TempDir()
	bank := memory.New(filepath.Join(workspace, "memory"))
	require.NoError(t, bank.Init())

	prov := &milestoneFailingProvider{}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test"},
		Verification: &gilv1.Verification{},
	}
	ver := verify.NewRunner(workspace)

	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "m",
		Tools:    []tool.Tool{&tool.MemoryUpdate{Bank: bank}, &tool.MemoryLoad{Bank: bank}},
		Verifier: ver,
		Memory:   bank,
		Events:   event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()

	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done

	// Expect: memory_milestone_start fired, then memory_milestone_skipped
	// (with reason=provider_unavailable), and run_done after. No
	// memory_milestone_error or any other *_error event should appear.
	var startSeen, skippedSeen, doneSeen int
	for _, e := range collected {
		switch e.Type {
		case "memory_milestone_start":
			startSeen++
		case "memory_milestone_skipped":
			skippedSeen++
			var d map[string]any
			require.NoError(t, json.Unmarshal(e.Data, &d))
			require.Equal(t, "provider_unavailable", d["reason"])
			require.Contains(t, d["detail"], "simulated network blip")
		case "run_done":
			doneSeen++
		}
		require.False(t, strings.HasSuffix(e.Type, "_error"),
			"no *_error event expected, got %q", e.Type)
		require.NotEqual(t, "memory_milestone_error", e.Type,
			"old memory_milestone_error name should not be emitted anymore")
	}
	require.Equal(t, 1, startSeen, "memory_milestone_start should fire once")
	require.Equal(t, 1, skippedSeen, "memory_milestone_skipped should fire once")
	require.Equal(t, 1, doneSeen, "run_done should fire once")
}

// recordingProvider records every provider.Request so tests can inspect the
// system prompt that was passed to each Complete call. It always returns the
// same tool call (triggering stuck pattern detection).
type recordingProvider struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (r *recordingProvider) Name() string { return "recording" }
func (r *recordingProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return provider.Response{
		Text:         "looping",
		ToolCalls:    []provider.ToolCall{{ID: "x", Name: "noop", Input: json.RawMessage(`{}`)}},
		StopReason:   "tool_use",
		InputTokens:  10,
		OutputTokens: 7,
	}, nil
}

func (r *recordingProvider) anySysContains(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.requests {
		if strings.Contains(req.System, sub) {
			return true
		}
	}
	return false
}

// TestAgentLoop_AltToolOrder_InjectsHintAndContinues verifies that when
// AltToolOrderStrategy recovers from a stuck pattern, the runner:
//  1. Emits a stuck_recovered event with action=alt_tool_order.
//  2. Injects an URGENT NOTE into the system prompt for the next iteration.
//  3. Continues running (does not immediately abort).
func TestAgentLoop_AltToolOrder_InjectsHintAndContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rec := &recordingProvider{}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 20},
	}

	// noopTool is already defined above in this file; reuse it.
	tools := []tool.Tool{&noopTool{}}

	stream := event.NewStream()
	sub := stream.Subscribe(512)

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       rec,
		Model:          "m",
		Tools:          tools,
		Verifier:       verify.NewRunner(t.TempDir()),
		Events:         stream,
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.AltToolOrderStrategy{},
		StuckThreshold: 5, // give it several recoveries before aborting
	}

	var mu sync.Mutex
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	// Loop should either exhaust iterations or be aborted by stuck threshold,
	// not by a hard error.
	require.Contains(t, []string{"stuck", "max_iterations"}, res.Status)

	// Wait for collector to drain.
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}

	sub.Close()

	mu.Lock()
	evs := collected
	mu.Unlock()

	// 1. Expect at least one stuck_recovered with action=alt_tool_order.
	var altRecov int
	for _, e := range evs {
		if e.Type != "stuck_recovered" {
			continue
		}
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		if d["action"] == "alt_tool_order" {
			altRecov++
		}
	}
	require.Greater(t, altRecov, 0, "expected at least one alt_tool_order recovery event")

	// 2. Expect the URGENT NOTE to have been injected into at least one request.
	require.True(t, rec.anySysContains("URGENT NOTE"),
		"expected at least one provider request to contain URGENT NOTE in system prompt")
	require.True(t, rec.anySysContains("STUCK PATTERN DETECTED"),
		"expected URGENT NOTE to contain STUCK PATTERN DETECTED hint")
}

func TestAgentLoop_AltToolOrder_NoteIsSingleShot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rec := &recordingProvider{}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 25},
	}

	tools := []tool.Tool{&noopTool{}}

	stream := event.NewStream()
	sub := stream.Subscribe(512)

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       rec,
		Model:          "m",
		Tools:          tools,
		Verifier:       verify.NewRunner(t.TempDir()),
		Events:         stream,
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.AltToolOrderStrategy{},
		StuckThreshold: 5,
	}

	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				collected = append(collected, e)
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	_, err := loop.Run(ctx)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
	sub.Close()

	rec.mu.Lock()
	reqs := rec.requests
	rec.mu.Unlock()

	// Count how many requests contain URGENT NOTE. Must be strictly less than
	// total requests (i.e. the note is NOT present on every request).
	var urgentCount int
	for _, req := range reqs {
		if strings.Contains(req.System, "URGENT NOTE") {
			urgentCount++
		}
	}
	// Sanity: we ran at least a few iterations.
	require.Greater(t, len(reqs), 4, "expected multiple requests")
	// The note must have appeared at least once (recovery fired).
	require.Greater(t, urgentCount, 0, "expected at least one injection of URGENT NOTE")
	// And must be strictly less than all requests (it's single-shot, so most won't have it).
	require.Less(t, urgentCount, len(reqs), "URGENT NOTE should be single-shot, not in every request")
}

// TestAgentLoop_ResetSection_RollsBackAndContinues verifies that
// ResetSectionStrategy triggers a hard reset and emits a stuck_reset_section
// event with a non-empty sha.
func TestAgentLoop_ResetSection_RollsBackAndContinues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workDir := t.TempDir()
	baseDir := t.TempDir()

	// Seed the workspace with initial content and make an initial checkpoint commit.
	sg := checkpoint.New(workDir, baseDir)
	require.NoError(t, sg.Init(ctx))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "seed.txt"), []byte("seed"), 0o644))
	_, err := sg.Commit(ctx, "seed commit")
	require.NoError(t, err)

	// Provider always makes the same write_file call → triggers PatternRepeatedActionObservation.
	mock := &loopProvider{turn: provider.MockTurn{
		Text: "writing file",
		ToolCalls: []provider.ToolCall{{
			ID:   "c1",
			Name: "write_file",
			Input: json.RawMessage(`{"path":"stuck.txt","content":"stuck"}`),
		}},
		StopReason: "tool_use",
	}}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 50},
	}

	tools := []tool.Tool{&tool.WriteFile{WorkingDir: workDir}}

	stream := event.NewStream()
	sub := stream.Subscribe(512)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       mock,
		Model:          "m1",
		Tools:          tools,
		Verifier:       verify.NewRunner(workDir),
		Events:         stream,
		Checkpoint:     sg,
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.ResetSectionStrategy{},
		StuckThreshold: 5,
	}

	var mu sync.Mutex
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	// The loop should abort due to stuck (ResetSection fires but loop keeps
	// looping, eventually hitting threshold after resets are exhausted).
	require.Contains(t, []string{"stuck", "max_iterations"}, res.Status)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	// Must have at least one stuck_reset_section event with non-empty sha.
	var resetCount int
	for _, e := range evs {
		if e.Type != "stuck_reset_section" {
			continue
		}
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		sha, _ := d["sha"].(string)
		require.NotEmpty(t, sha, "stuck_reset_section event missing sha: %s", string(e.Data))
		resetCount++
	}
	require.Greater(t, resetCount, 0, "expected at least one stuck_reset_section event")
}

// loopingTool is a tool that always succeeds with "ok", used to generate
// repeated tool-call patterns for stuck detection tests.
type loopingTool struct{}

func (l *loopingTool) Name() string        { return "noop" }
func (l *loopingTool) Description() string { return "no-op looping tool" }
func (l *loopingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (l *loopingTool) Run(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

// interceptingProv routes calls based on whether the system prompt contains
// "adversarial reviewer": adversary calls get a suggestion; main calls get a
// repeated noop tool call to trigger stuck detection.
type interceptingProv struct {
	mu        sync.Mutex
	captureFn func(provider.Request)
}

func (p *interceptingProv) Name() string { return "intercept" }
func (p *interceptingProv) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	if p.captureFn != nil {
		p.captureFn(req)
	}
	if strings.Contains(req.System, "adversarial reviewer") {
		return provider.Response{
			Text:       "Use a different tool than 'noop' on the next iteration.",
			StopReason: "end_turn",
		}, nil
	}
	return provider.Response{
		Text:       "looping",
		ToolCalls:  []provider.ToolCall{{ID: "x", Name: "noop", Input: json.RawMessage(`{}`)}},
		StopReason: "tool_use",
	}, nil
}

func TestAgentLoop_AdversaryConsult_AppendsSuggestionToNextSystemPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type captured struct{ system, model string }
	var capturedSysMu sync.Mutex
	var capturedSys []captured

	prov := &interceptingProv{
		captureFn: func(req provider.Request) {
			capturedSysMu.Lock()
			defer capturedSysMu.Unlock()
			capturedSys = append(capturedSys, captured{system: req.System, model: req.Model})
		},
	}

	spec := &gilv1.FrozenSpec{
		Budget: &gilv1.Budget{MaxIterations: 8},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "fail", Kind: gilv1.CheckKind_SHELL, Command: "false"}},
		},
	}
	loop := &AgentLoop{
		Spec:           spec,
		Provider:       prov,
		Model:          "main-model",
		AdversaryModel: "adversary-model",
		Tools:          []tool.Tool{&loopingTool{}},
		Verifier:       verify.NewRunner(t.TempDir()),
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.AdversaryConsultStrategy{},
		StuckThreshold: 5,
		Events:         event.NewStream(),
	}
	_, err := loop.Run(ctx)
	require.NoError(t, err)

	capturedSysMu.Lock()
	defer capturedSysMu.Unlock()
	var sawAdversaryCall bool
	var sawNoteInjected bool
	for _, c := range capturedSys {
		if strings.Contains(c.system, "adversarial reviewer") && c.model == "adversary-model" {
			sawAdversaryCall = true
		}
		if strings.Contains(c.system, "URGENT NOTE") && strings.Contains(c.system, "ADVERSARY SUGGESTION") {
			sawNoteInjected = true
		}
	}
	require.True(t, sawAdversaryCall, "expected at least one Complete() call with adversary system prompt + adversary-model")
	require.True(t, sawNoteInjected, "expected at least one Complete() call where system prompt contained the injected ADVERSARY SUGGESTION note")
}

func TestAgentLoop_MilestoneGate_NonMemoryToolsIgnored(t *testing.T) {
	workspace := t.TempDir()
	bank := memory.New(filepath.Join(workspace, "memory"))
	require.NoError(t, bank.Init())

	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "done", StopReason: "end_turn"},
		{ // milestone turn calling a non-memory tool — should be skipped
			Text: "trying bash",
			ToolCalls: []provider.ToolCall{{
				ID:    "x",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"echo hi"}`),
			}},
			StopReason: "tool_use",
		},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	ver := verify.NewRunner(workspace)
	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "m",
		Tools:    []tool.Tool{&tool.Bash{WorkingDir: workspace}, &tool.MemoryUpdate{Bank: bank}},
		Verifier: ver,
		Memory:   bank,
		Events:   event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range sub.Events() {
			collected = append(collected, e)
		}
	}()
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done
	var skipSeen bool
	for _, e := range collected {
		if e.Type == "memory_milestone_tool_skipped" {
			skipSeen = true
		}
	}
	require.True(t, skipSeen)
}

func TestAgentLoop_Permission_AllowsWhenNil(t *testing.T) {
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "writing", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"hi"}`),
		}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.WriteFile{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		// Permission: nil → allow all
	}
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	// File was written
	_, err = os.Stat(filepath.Join(workspace, "a.txt"))
	require.NoError(t, err)
}

func TestAgentLoop_Permission_DeniesAndContinues(t *testing.T) {
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "trying bash", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf /"}`),
		}}, StopReason: "tool_use"},
		{Text: "ok giving up", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.Bash{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		Permission: &permission.Evaluator{Rules: []permission.Rule{
			{Tool: "bash", Key: "rm *", Action: permission.DecisionDeny},
		}},
		Events: event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() { defer close(done); for e := range sub.Events() { collected = append(collected, e) } }()
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done
	// Expect at least one permission_denied event
	var deniedSeen bool
	for _, e := range collected {
		if e.Type == "permission_denied" {
			deniedSeen = true
		}
	}
	require.True(t, deniedSeen)
}

func TestAgentLoop_Permission_AskTreatedAsDenyInPhase7(t *testing.T) {
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "trying", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`),
		}}, StopReason: "tool_use"},
		{Text: "giving up", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.Bash{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		// Empty rules → default Ask
		Permission: &permission.Evaluator{},
		Events:     event.NewStream(),
	}
	sub := loop.Events.Subscribe(256)
	var collected []event.Event
	done := make(chan struct{})
	go func() { defer close(done); for e := range sub.Events() { collected = append(collected, e) } }()
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	sub.Close()
	<-done
	// Expect a permission_denied event with decision=ask
	var askDenied bool
	for _, e := range collected {
		if e.Type == "permission_denied" {
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			if d["decision"] == "ask" {
				askDenied = true
			}
		}
	}
	require.True(t, askDenied, "expected permission_denied with decision=ask")
}

func TestAgentLoop_Permission_AllowMatchPasses(t *testing.T) {
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "writing", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "write_file", Input: json.RawMessage(`{"path":"ok.txt","content":"hi"}`),
		}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.WriteFile{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		Permission: &permission.Evaluator{Rules: []permission.Rule{
			{Tool: "write_file", Key: "*.txt", Action: permission.DecisionAllow},
		}},
	}
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	_, err = os.Stat(filepath.Join(workspace, "ok.txt"))
	require.NoError(t, err)
}

func TestPermissionKeyFor(t *testing.T) {
	cases := []struct {
		tool  string
		input string
		want  string
	}{
		{"bash", `{"command":"ls -la"}`, "ls -la"},
		{"write_file", `{"path":"a/b.txt","content":"x"}`, "a/b.txt"},
		{"read_file", `{"path":"x"}`, "x"},
		{"memory_update", `{"file":"progress","content":"x"}`, "progress"},
		{"edit", `{"blocks":"..."}`, ""},
		{"unknown", `{"x":"y"}`, ""},
		{"bash", `{not json`, ""},
	}
	for _, c := range cases {
		got := permissionKeyFor(c.tool, json.RawMessage(c.input))
		require.Equal(t, c.want, got, "tool=%s input=%s", c.tool, c.input)
	}
}

func TestAgentLoop_Permission_AskCallback_Allow(t *testing.T) {
	// Permission says Ask (empty rules → default Ask); callback returns true → tool actually runs.
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "asking", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "write_file", Input: json.RawMessage(`{"path":"x.txt","content":"hi"}`),
		}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	var asked int
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.WriteFile{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		// Empty rules → default Ask
		Permission: &permission.Evaluator{},
		AskCallback: func(ctx context.Context, r AskRequest) bool {
			asked++
			return true // human approves
		},
	}
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	require.Equal(t, 1, asked)
	// File was written — callback approval propagated correctly.
	_, err = os.Stat(filepath.Join(workspace, "x.txt"))
	require.NoError(t, err)
}

func TestAgentLoop_Permission_AskCallback_Deny(t *testing.T) {
	// Permission says Ask; callback returns false → tool is denied, run still completes.
	workspace := t.TempDir()
	prov := provider.NewMockToolProvider([]provider.MockTurn{
		{Text: "asking", ToolCalls: []provider.ToolCall{{
			ID: "x", Name: "write_file", Input: json.RawMessage(`{"path":"x.txt","content":"hi"}`),
		}}, StopReason: "tool_use"},
		{Text: "denied", StopReason: "end_turn"},
	})
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "test"}, Verification: &gilv1.Verification{}}
	var asked int
	loop := &AgentLoop{
		Spec: spec, Provider: prov, Model: "m",
		Tools:    []tool.Tool{&tool.WriteFile{WorkingDir: workspace}},
		Verifier: verify.NewRunner(workspace),
		Permission: &permission.Evaluator{},
		AskCallback: func(ctx context.Context, r AskRequest) bool {
			asked++
			return false // human denies
		},
	}
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)
	require.Equal(t, 1, asked)
	// File was NOT written — deny was respected.
	_, err = os.Stat(filepath.Join(workspace, "x.txt"))
	require.True(t, os.IsNotExist(err), "file should not exist after deny")
}

// subagentRecordingProvider routes calls differently for the sub-loop vs the
// parent loop. When the system prompt mentions "reconnaissance" (the subgoal
// string), it returns a final answer. Otherwise it always loops a tool call
// to trigger stuck detection.
type subagentRecordingProvider struct {
	mu            sync.Mutex
	mainRequests  []provider.Request
	subRequests   []provider.Request
}

func (p *subagentRecordingProvider) Name() string { return "subagent-recording" }
func (p *subagentRecordingProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Detect sub-loop calls by the presence of "stuck" in the user message (SeedUserMessage).
	var isSubLoop bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "main agent is stuck") {
			isSubLoop = true
			break
		}
	}
	if isSubLoop {
		p.subRequests = append(p.subRequests, req)
		return provider.Response{
			Text:       "the command syntax is wrong, try using a relative path instead of absolute",
			StopReason: "end_turn",
		}, nil
	}
	p.mainRequests = append(p.mainRequests, req)
	return provider.Response{
		Text:       "looping",
		ToolCalls:  []provider.ToolCall{{ID: "x", Name: "noop", Input: json.RawMessage(`{}`)}},
		StopReason: "tool_use",
	}, nil
}

// TestAgentLoop_SubagentBranch_InjectsFindingIntoSystemPrompt verifies that
// SubagentBranchStrategy:
//  1. Fires RunSubagent on the parent AgentLoop (calls the sub-provider).
//  2. The sub-loop's text response becomes the SUBAGENT FINDING in the parent's system prompt.
//  3. A stuck_recovered event is emitted with action=subagent_branch.
func TestAgentLoop_SubagentBranch_InjectsFindingIntoSystemPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec := &subagentRecordingProvider{}

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "loop forever"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "never", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 20},
	}

	stream := event.NewStream()
	sub := stream.Subscribe(512)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       rec,
		Model:          "main-model",
		Tools:          []tool.Tool{&noopTool{}},
		Verifier:       verify.NewRunner(t.TempDir()),
		Events:         stream,
		StuckDetector:  &stuck.Detector{Window: 50},
		StuckStrategy:  stuck.SubagentBranchStrategy{MaxIterations: 3},
		StuckThreshold: 5,
	}

	var mu sync.Mutex
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	require.Contains(t, []string{"stuck", "max_iterations"}, res.Status)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	// 1. At least one stuck_recovered event with action=subagent_branch.
	var subagentRecov int
	for _, e := range evs {
		if e.Type != "stuck_recovered" {
			continue
		}
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		if d["action"] == "subagent_branch" {
			subagentRecov++
		}
	}
	require.Greater(t, subagentRecov, 0, "expected at least one subagent_branch recovery event")

	// 2. The sub-loop was actually called (sub-provider saw requests).
	rec.mu.Lock()
	subReqs := len(rec.subRequests)
	mainReqs := rec.mainRequests
	rec.mu.Unlock()
	require.Greater(t, subReqs, 0, "expected sub-loop provider calls")

	// 3. At least one main-loop provider request contains "SUBAGENT FINDING" in system prompt.
	var sawFinding bool
	for _, req := range mainReqs {
		if strings.Contains(req.System, "SUBAGENT FINDING") {
			sawFinding = true
			break
		}
	}
	require.True(t, sawFinding, "expected at least one main-loop provider request to contain SUBAGENT FINDING in system prompt")
}

// systemRecordingProvider captures every Request.System the runner passes
// in. Used by the AGENTS.md injection tests to assert what landed in the
// system block on the very first iteration.
type systemRecordingProvider struct {
	mu       sync.Mutex
	systems  []string
	stop     bool
	endAfter int // emit StopReason="end_turn" after this many calls
}

func (p *systemRecordingProvider) Name() string { return "system-recording" }
func (p *systemRecordingProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	p.mu.Lock()
	p.systems = append(p.systems, req.System)
	calls := len(p.systems)
	p.mu.Unlock()
	if calls >= p.endAfter {
		return provider.Response{Text: "done", StopReason: "end_turn"}, nil
	}
	// Default: end immediately so the loop terminates quickly.
	return provider.Response{Text: "ok", StopReason: "end_turn"}, nil
}

func TestAgentLoop_DiscoversAGENTSMDFromWorkspace(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"),
		[]byte("# AGENTS\nUse tabs not spaces.\n"), 0o644))

	prov := &systemRecordingProvider{endAfter: 1}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test"},
		Verification: &gilv1.Verification{}, // empty checks → vacuous pass
		Budget:       &gilv1.Budget{MaxIterations: 2},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(64)

	loop := &AgentLoop{
		Spec:      spec,
		Provider:  prov,
		Model:     "x",
		Verifier:  verify.NewRunner(t.TempDir()),
		Events:    stream,
		Workspace: ws,
	}
	res, err := loop.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", res.Status)

	prov.mu.Lock()
	require.NotEmpty(t, prov.systems)
	first := prov.systems[0]
	prov.mu.Unlock()
	require.Contains(t, first, "## Project Instructions")
	require.Contains(t, first, "Use tabs not spaces")
	require.Contains(t, first, "--- BEGIN workspace: AGENTS.md ---")

	// Drain events and verify system_instructions_loaded was emitted.
	var sawLoaded bool
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				goto done
			}
			if e.Type == "system_instructions_loaded" {
				sawLoaded = true
			}
		default:
			goto done
		}
	}
done:
	require.True(t, sawLoaded, "expected system_instructions_loaded event")
}

func TestAgentLoop_NoWorkspaceMeansNoDiscovery(t *testing.T) {
	prov := &systemRecordingProvider{endAfter: 1}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "test"},
		Verification: &gilv1.Verification{},
		Budget:       &gilv1.Budget{MaxIterations: 2},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(64)

	loop := &AgentLoop{
		Spec:     spec,
		Provider: prov,
		Model:    "x",
		Verifier: verify.NewRunner(t.TempDir()),
		Events:   stream,
		// Workspace deliberately left empty.
	}
	_, err := loop.Run(context.Background())
	require.NoError(t, err)

	prov.mu.Lock()
	first := prov.systems[0]
	prov.mu.Unlock()
	require.NotContains(t, first, "## Project Instructions")

	// No event should have been emitted.
	var sawLoaded bool
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				goto done2
			}
			if e.Type == "system_instructions_loaded" {
				sawLoaded = true
			}
		default:
			goto done2
		}
	}
done2:
	require.False(t, sawLoaded, "system_instructions_loaded should not fire without workspace")
}

func TestAgentLoop_InstructionSourcesOverrideSkipsDiscovery(t *testing.T) {
	// Even with a Workspace set, an explicit InstructionSources slice
	// should bypass discovery (no on-disk read) and render directly.
	ws := t.TempDir()
	// Plant an AGENTS.md that should NOT be picked up because the
	// override takes precedence.
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"),
		[]byte("ON DISK\n"), 0o644))

	prov := &systemRecordingProvider{endAfter: 1}
	spec := &gilv1.FrozenSpec{
		Goal:         &gilv1.Goal{OneLiner: "t"},
		Verification: &gilv1.Verification{},
		Budget:       &gilv1.Budget{MaxIterations: 2},
	}

	loop := &AgentLoop{
		Spec:      spec,
		Provider:  prov,
		Model:     "x",
		Verifier:  verify.NewRunner(t.TempDir()),
		Workspace: ws,
		InstructionSources: []instructions.Source{
			{Path: "/virtual/AGENTS.md", Origin: "workspace", Body: "FROM OVERRIDE\n"},
		},
	}
	_, err := loop.Run(context.Background())
	require.NoError(t, err)

	prov.mu.Lock()
	first := prov.systems[0]
	prov.mu.Unlock()
	require.Contains(t, first, "FROM OVERRIDE")
	require.NotContains(t, first, "ON DISK")
}

func TestAgentLoop_MemoryBankAppearsAfterInstructions(t *testing.T) {
	// Phase 19 Track B: memory bank is now LAZY — skipped on iter 1 to
	// keep the first-call prompt slim, included on iter 2+. This test
	// asserts the order rule (instructions BEFORE bank) on the second
	// iteration's system prompt, where both are present.
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"),
		[]byte("INSTR_MARKER\n"), 0o644))

	bankDir := filepath.Join(t.TempDir(), "memory")
	bank := memory.New(bankDir)
	require.NoError(t, bank.Init())
	require.NoError(t, bank.Write(memory.FileProgress, "BANK_MARKER\n"))

	prov := &systemRecordingProvider{endAfter: 2}
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "t"},
		Verification: &gilv1.Verification{
			// Inject one always-failing check so the loop runs a
			// second iteration before the verifier passes; without
			// this it'd end on the first end_turn.
			Checks: []*gilv1.Check{{Name: "x", Kind: gilv1.CheckKind_SHELL, Command: "false", ExpectedExitCode: 0}},
		},
		Budget: &gilv1.Budget{MaxIterations: 3},
	}
	loop := &AgentLoop{
		Spec:      spec,
		Provider:  prov,
		Model:     "x",
		Verifier:  verify.NewRunner(t.TempDir()),
		Memory:    bank,
		Workspace: ws,
	}
	_, _ = loop.Run(context.Background())

	prov.mu.Lock()
	require.GreaterOrEqual(t, len(prov.systems), 2, "expected at least two iterations recorded")
	first := prov.systems[0]
	second := prov.systems[1]
	prov.mu.Unlock()

	// Iter 1: instructions present, bank absent (lazy).
	require.Contains(t, first, "INSTR_MARKER", "iter 1 should include AGENTS.md")
	require.NotContains(t, first, "BANK_MARKER", "iter 1 should NOT include memory bank (lazy)")

	// Iter 2: both present, instructions precede bank.
	instrIdx := strings.Index(second, "INSTR_MARKER")
	bankIdx := strings.Index(second, "BANK_MARKER")
	require.Greater(t, instrIdx, 0, "iter 2 expected AGENTS.md content in system prompt")
	require.Greater(t, bankIdx, 0, "iter 2 expected memory bank content in system prompt")
	require.Less(t, instrIdx, bankIdx, "instructions must precede memory bank")
}

// TestAgentLoop_NoProgress_FiresOnVariedFutileWork reproduces self-dogfood
// Run 8: an agent that varies its actions every iteration but cannot make
// progress on an impossible verification check. Each scripted turn ends
// with end_turn so the verifier runs and emits verify_run/verify_result —
// giving the NoProgress detector the per-iter signal it needs to compare
// across iterations. The verifier never improves (single check that always
// fails), and no successful file edits land, so NoProgress should fire and
// the loop aborts via stuck_unrecovered before exhausting MaxIterations.
func TestAgentLoop_NoProgress_FiresOnVariedFutileWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()

	// Build a sequence of varied turns: each one ends with end_turn (no
	// tool calls) so the verifier runs every iteration. The verifier
	// always fails (passing=0), and no edit ever lands, so the agent's
	// state never improves. Mix in some failed bash calls + reads so the
	// existing repetition detectors stay quiet.
	turns := []provider.MockTurn{}
	for i := 0; i < 8; i++ {
		// Iter Ai: a tool-use turn (varied), then iter Ai+1: an end_turn
		// turn that lets the verifier fire.
		turns = append(turns, provider.MockTurn{
			Text: "trying approach " + fmt.Sprintf("%d", i),
			ToolCalls: []provider.ToolCall{{
				ID:    fmt.Sprintf("c%d", i),
				Name:  "bash",
				Input: json.RawMessage(fmt.Sprintf(`{"command":"echo attempt%d"}`, i)),
			}},
			StopReason: "tool_use",
		})
		turns = append(turns, provider.MockTurn{
			Text:       "I think I'm done.",
			StopReason: "end_turn",
		})
	}

	mock := provider.NewMockToolProvider(turns)

	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "impossible task"},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{
				Name:             "never_passes",
				Kind:             gilv1.CheckKind_SHELL,
				Command:          "false",
				ExpectedExitCode: 0,
			}},
		},
		Budget: &gilv1.Budget{MaxIterations: 16},
	}

	tools := []tool.Tool{&tool.Bash{WorkingDir: dir}}

	stream := event.NewStream()
	sub := stream.Subscribe(512)
	defer sub.Close()

	loop := &AgentLoop{
		Spec:           spec,
		Provider:       mock,
		Model:          "m",
		Tools:          tools,
		Verifier:       verify.NewRunner(dir),
		Events:         stream,
		StuckDetector:  &stuck.Detector{Window: 500},
		StuckThreshold: 3,
		// No StuckStrategy → every detection counts as unrecovered, so we
		// abort fast when NoProgress hits the threshold.
	}

	var mu sync.Mutex
	var collected []event.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					return
				}
				mu.Lock()
				collected = append(collected, e)
				mu.Unlock()
				if e.Type == "stuck_unrecovered" || e.Type == "run_done" || e.Type == "run_max_iterations" {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := loop.Run(ctx)
	require.NoError(t, err)
	// Loop should abort via stuck (NoProgress), not via max_iterations.
	require.Equal(t, "stuck", res.Status,
		"expected stuck abort from NoProgress, got status=%s after %d iters", res.Status, res.Iterations)
	require.Less(t, res.Iterations, 16, "expected early abort, got %d iters", res.Iterations)

	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}

	mu.Lock()
	evs := collected
	mu.Unlock()

	// At least one stuck_detected with pattern=NoProgress must appear.
	var noProgressDetections int
	for _, e := range evs {
		if e.Type != "stuck_detected" {
			continue
		}
		var d map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &d))
		if d["pattern"] == "NoProgress" {
			noProgressDetections++
		}
	}
	require.Greater(t, noProgressDetections, 0,
		"expected at least one stuck_detected with pattern=NoProgress, events: %v", evs)
}

// TestSplitBashChain — Phase 22.A bash-chain permission bypass fix.
// Verifies the chain-decomposer extracts each sub-command's verb so that
// e.g. "cp x y && mv y z" is gated on BOTH cp and mv (not just cp).
func TestSplitBashChain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string(nil)},
		{"single", "ls -la", []string{"ls -la"}},
		{"and", "cp a b && mv c d", []string{"cp a b", "mv c d"}},
		{"or", "go build || echo failed", []string{"go build", "echo failed"}},
		{"semicolon", "echo a; echo b", []string{"echo a", "echo b"}},
		{"pipe", "ls | grep go", []string{"ls", "grep go"}},
		{"mixed", "cd x && grep -r foo . | head", []string{"cd x", "grep -r foo .", "head"}},
		{"quoted-and", "echo 'a && b'", []string{"echo 'a && b'"}},
		{"quoted-pipe", `printf "%s|%s\n" a b`, []string{`printf "%s|%s\n" a b`}},
		{"escape", `echo \&\& not-a-chain`, []string{`echo \&\& not-a-chain`}},
		{"empty-segment", "echo a && && echo b", []string{"echo a", "echo b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitBashChain(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part[%d]: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
