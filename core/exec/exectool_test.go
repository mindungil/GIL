package exec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/tool"
)

// execFakeTool is a minimal tool.Tool for testing ExecTool without external deps.
type execFakeTool struct {
	name string
	out  string
}

func (f *execFakeTool) Name() string        { return f.name }
func (f *execFakeTool) Description() string { return "fake" }
func (f *execFakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f *execFakeTool) Run(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: f.out}, nil
}

type errFakeTool struct{}

func (e *errFakeTool) Name() string        { return "boom" }
func (e *errFakeTool) Description() string { return "explodes" }
func (e *errFakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (e *errFakeTool) Run(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "kaboom", IsError: true}, nil
}

func TestExecTool_LinearRecipe(t *testing.T) {
	e := &ExecTool{Tools: []tool.Tool{
		&execFakeTool{name: "a", out: "alpha"},
		&execFakeTool{name: "b", out: "beta"},
	}}
	args, _ := json.Marshal(map[string]any{
		"recipe": map[string]any{
			"steps": []map[string]any{
				{"tool": "a", "args": map[string]any{}},
				{"tool": "b", "args": map[string]any{}},
			},
			"summary": "{{step_1_output}}+{{step_2_output}}",
		},
	})
	res, err := e.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "alpha+beta", res.Content)
}

func TestExecTool_DefaultSummary_NoTemplate(t *testing.T) {
	e := &ExecTool{Tools: []tool.Tool{&execFakeTool{name: "a", out: "x"}}}
	args, _ := json.Marshal(map[string]any{
		"recipe": map[string]any{
			"steps": []map[string]any{{"tool": "a", "args": map[string]any{}}},
		},
	})
	res, err := e.Run(context.Background(), args)
	require.NoError(t, err)
	require.Contains(t, res.Content, "step 1 (a): ok")
}

func TestExecTool_NoSteps_IsError(t *testing.T) {
	e := &ExecTool{}
	args, _ := json.Marshal(map[string]any{"recipe": map[string]any{"steps": []map[string]any{}}})
	res, err := e.Run(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "no steps")
}

func TestExecTool_ImplementsToolInterface(t *testing.T) {
	var _ tool.Tool = (*ExecTool)(nil)
}

func TestExecTool_BadJSON(t *testing.T) {
	e := &ExecTool{}
	_, err := e.Run(context.Background(), json.RawMessage(`{"recipe":`))
	require.Error(t, err)
}

func TestExecTool_FiltersOutSelfFromInnerTools(t *testing.T) {
	// If somehow the caller passes another ExecTool in Tools, it's filtered out.
	other := &ExecTool{}
	e := &ExecTool{Tools: []tool.Tool{
		&execFakeTool{name: "a", out: "x"},
		other, // same name "exec"
	}}
	args, _ := json.Marshal(map[string]any{
		"recipe": map[string]any{
			"steps": []map[string]any{{"tool": "exec", "args": map[string]any{}}},
		},
	})
	res, err := e.Run(context.Background(), args)
	require.NoError(t, err)
	// The exec step is "skipped" because the inner Tools filter removed exec.
	require.Contains(t, res.Content, "skipped")
	require.False(t, res.IsError, "skipped is not an error")
}

func TestExecTool_StepError_OverallIsError(t *testing.T) {
	e := &ExecTool{Tools: []tool.Tool{&errFakeTool{}}}
	args, _ := json.Marshal(map[string]any{
		"recipe": map[string]any{
			"steps":   []map[string]any{{"tool": "boom", "args": map[string]any{}}},
			"summary": "x",
		},
	})
	res, err := e.Run(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestExecTool_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&ExecTool{}).Schema(), &v))
}
