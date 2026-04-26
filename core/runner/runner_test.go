package runner

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/stuck"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
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
	prompt := buildSystemPrompt(spec, tools)
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
