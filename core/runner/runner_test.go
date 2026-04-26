package runner

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

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
