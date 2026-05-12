package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeSubagentRunner is a minimal SubagentRunner that records the config
// it was called with and returns whatever Result+Err the test set.
type fakeSubagentRunner struct {
	seenCfg SubagentRunConfig
	result  SubagentRunResult
	err     error
	calls   int
}

func (f *fakeSubagentRunner) RunSubagentWithConfig(_ context.Context, cfg SubagentRunConfig) (SubagentRunResult, error) {
	f.calls++
	f.seenCfg = cfg
	return f.result, f.err
}

func TestSubagent_HappyPath(t *testing.T) {
	fr := &fakeSubagentRunner{
		result: SubagentRunResult{
			Summary:    "core/runner/runner.go has the main agent loop in func (a *AgentLoop) Run().",
			Status:     "done",
			Iterations: 3,
			Tokens:     1234,
		},
	}
	s := &Subagent{Runner: fr}

	args := json.RawMessage(`{"goal":"find which file defines the main agent loop"}`)
	res, err := s.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, 1, fr.calls)
	require.Equal(t, "find which file defines the main agent loop", fr.seenCfg.Goal)
	// Empty AllowedTools → runner falls back to its read-only default
	// (the tool itself MUST NOT inject an override; that's the runner's
	// job so the default lives in one place).
	require.Empty(t, fr.seenCfg.AllowedTools)
	require.Contains(t, res.Content, "Subagent finding")
	require.Contains(t, res.Content, "status=done")
	require.Contains(t, res.Content, "iterations=3")
	require.Contains(t, res.Content, "tokens=1234")
	require.Contains(t, res.Content, "core/runner/runner.go")
}

func TestSubagent_PassesMaxIterationsAndToolOverride(t *testing.T) {
	fr := &fakeSubagentRunner{result: SubagentRunResult{Summary: "ok", Status: "done"}}
	s := &Subagent{Runner: fr}

	args := json.RawMessage(`{"goal":"scout","max_iterations":12,"tools":["read_file","repomap"]}`)
	res, err := s.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, 12, fr.seenCfg.MaxIterations)
	require.Equal(t, []string{"read_file", "repomap"}, fr.seenCfg.AllowedTools)
}

func TestSubagent_EmptyGoal_IsError(t *testing.T) {
	fr := &fakeSubagentRunner{}
	s := &Subagent{Runner: fr}
	res, err := s.Run(context.Background(), json.RawMessage(`{"goal":"   "}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "goal is required")
	require.Equal(t, 0, fr.calls, "runner must not be called when goal is empty")
}

func TestSubagent_NilRunner_IsError(t *testing.T) {
	s := &Subagent{} // no runner wired
	res, err := s.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "not configured")
}

func TestSubagent_BadJSON_Errors(t *testing.T) {
	fr := &fakeSubagentRunner{}
	s := &Subagent{Runner: fr}
	_, err := s.Run(context.Background(), json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestSubagent_RunnerError_SurfacesIsError(t *testing.T) {
	fr := &fakeSubagentRunner{
		err:    errors.New("provider exploded"),
		result: SubagentRunResult{Summary: "got partway"},
	}
	s := &Subagent{Runner: fr}
	res, err := s.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "provider exploded")
	// Partial finding surfaces too — agent might still salvage signal
	// from a sub-loop that died mid-investigation.
	require.Contains(t, res.Content, "got partway")
}

func TestSubagent_EmptySummary_IsError(t *testing.T) {
	// max_iterations exhaustion with no FinalText is a real outcome.
	// Tell the agent what happened rather than returning a blank tool_result.
	fr := &fakeSubagentRunner{
		result: SubagentRunResult{
			Summary:    "",
			Status:     "max_iterations",
			Iterations: 8,
			Tokens:     20000,
		},
	}
	s := &Subagent{Runner: fr}
	res, err := s.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "no finding")
	require.Contains(t, res.Content, "max_iterations")
}

func TestSubagent_TruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("A", subagentResultMaxBytes+500)
	fr := &fakeSubagentRunner{result: SubagentRunResult{Summary: long, Status: "done"}}
	s := &Subagent{Runner: fr}
	res, err := s.Run(context.Background(), json.RawMessage(`{"goal":"x"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "truncated")
	// The full content includes header lines + truncation marker, so we
	// allow some slop above the cap rather than asserting an exact size.
	require.Less(t, len(res.Content), subagentResultMaxBytes+512)
}

func TestSubagent_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&Subagent{}).Schema(), &v))
}

func TestSubagent_ImplementsToolInterface(t *testing.T) {
	var _ Tool = (*Subagent)(nil)
}

func TestSubagent_NameAndDescription(t *testing.T) {
	s := &Subagent{}
	require.Equal(t, "subagent", s.Name())
	require.Contains(t, s.Description(), "read-only")
	require.Contains(t, s.Description(), "CANNOT modify files")
}
