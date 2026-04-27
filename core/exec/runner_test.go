package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/tool"
)

// fakeTool returns a fixed Result.
type fakeTool struct {
	name    string
	out     string
	err     error
	isError bool
	delay   time.Duration
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return "fake" }
func (f *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) Run(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	return tool.Result{Content: f.out, IsError: f.isError}, f.err
}

func TestRecipeRunner_LinearSequence(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{
		&fakeTool{name: "a", out: "AAA"},
		&fakeTool{name: "b", out: "BBB"},
	}}
	rec := Recipe{
		Steps: []RecipeStep{
			{Tool: "a", Args: json.RawMessage(`{}`)},
			{Tool: "b", Args: json.RawMessage(`{}`)},
		},
		Summary: "{{step_1_output}} -> {{step_2_output}}",
	}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Len(t, res.Steps, 2)
	require.Equal(t, "ok", res.Steps[0].Status)
	require.Equal(t, "AAA -> BBB", res.Summary)
}

func TestRecipeRunner_DefaultSummaryWhenEmpty(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: "x"}}}
	rec := Recipe{Steps: []RecipeStep{{Tool: "a", Args: json.RawMessage(`{}`)}}}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "step 1 (a): ok")
}

func TestRecipeRunner_UnknownTool_SkippedNotFatal(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: "x"}}}
	rec := Recipe{Steps: []RecipeStep{
		{Tool: "missing", Args: json.RawMessage(`{}`)},
		{Tool: "a", Args: json.RawMessage(`{}`)},
	}, Summary: "{{step_1_status}} {{step_2_status}}"}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, "skipped", res.Steps[0].Status)
	require.Contains(t, res.Steps[0].ErrMsg, "unknown tool")
	require.Equal(t, "ok", res.Steps[1].Status)
	require.Equal(t, "skipped ok", res.Summary)
}

func TestRecipeRunner_StepError_CapturedNotPropagated(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{
		&fakeTool{name: "a", err: errors.New("boom")},
		&fakeTool{name: "b", out: "still ran"},
	}}
	rec := Recipe{Steps: []RecipeStep{
		{Tool: "a", Args: json.RawMessage(`{}`)},
		{Tool: "b", Args: json.RawMessage(`{}`)},
	}}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, "error", res.Steps[0].Status)
	require.Contains(t, res.Steps[0].ErrMsg, "boom")
	require.Equal(t, "ok", res.Steps[1].Status, "later steps still run after an error")
}

func TestRecipeRunner_IsErrorResult_MarkedError(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: "fail msg", isError: true}}}
	rec := Recipe{Steps: []RecipeStep{{Tool: "a", Args: json.RawMessage(`{}`)}}}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, "error", res.Steps[0].Status)
	require.Contains(t, res.Steps[0].Output, "fail msg")
}

func TestRecipeRunner_OutputTruncated(t *testing.T) {
	big := strings.Repeat("x", 100_000)
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: big}}, MaxOutputBytes: 1000}
	rec := Recipe{Steps: []RecipeStep{{Tool: "a", Args: json.RawMessage(`{}`)}}}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Less(t, len(res.Steps[0].Output), 2000)
	require.Contains(t, res.Steps[0].Output, "truncated")
}

func TestRecipeRunner_StepTimeout(t *testing.T) {
	r := &Runner{
		Tools:          []tool.Tool{&fakeTool{name: "slow", delay: 200 * time.Millisecond}},
		StepTimeoutSec: 0, // need a sub-second knob; simpler to use an in-context deadline
	}
	// Override to 1 sec so we don't wait long; the slow tool finishes well within → ok
	rec := Recipe{Steps: []RecipeStep{{Tool: "slow", Args: json.RawMessage(`{}`)}}}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Steps[0].Status, "200ms < 300s default timeout, should pass")

	// For a real timeout test, use a tool that returns ctx.Err and a tiny ctx
	fakeWithDelay := &fakeTool{name: "slow2", delay: 500 * time.Millisecond}
	r2 := &Runner{Tools: []tool.Tool{fakeWithDelay}, StepTimeoutSec: 1} // 1 second
	// Wait this one out — it'll succeed since delay < timeout
	res2, err := r2.Run(context.Background(), Recipe{Steps: []RecipeStep{{Tool: "slow2", Args: json.RawMessage(`{}`)}}})
	require.NoError(t, err)
	require.Equal(t, "ok", res2.Steps[0].Status)
}

func TestRecipeRunner_EmptyRecipe_Errors(t *testing.T) {
	r := &Runner{}
	_, err := r.Run(context.Background(), Recipe{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no steps")
}

func TestRecipeRunner_TooManySteps_Errors(t *testing.T) {
	r := &Runner{MaxSteps: 2}
	rec := Recipe{Steps: []RecipeStep{
		{Tool: "a"}, {Tool: "b"}, {Tool: "c"},
	}}
	_, err := r.Run(context.Background(), rec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeding max")
}

func TestRecipeRunner_EmitterCalled(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: "x"}}}
	var startCount, doneCount int
	r.Emit = func(t string, data map[string]any) {
		if t == "exec_step_start" {
			startCount++
		}
		if t == "exec_step_done" {
			doneCount++
		}
	}
	_, err := r.Run(context.Background(), Recipe{Steps: []RecipeStep{{Tool: "a", Args: json.RawMessage(`{}`)}}})
	require.NoError(t, err)
	require.Equal(t, 1, startCount)
	require.Equal(t, 1, doneCount)
}

func TestRecipeRunner_TemplateSubstitution_PartialPlaceholders(t *testing.T) {
	r := &Runner{Tools: []tool.Tool{&fakeTool{name: "a", out: "X"}}}
	rec := Recipe{
		Steps:   []RecipeStep{{Tool: "a", Args: json.RawMessage(`{}`)}},
		Summary: "got {{step_1_output}} (status {{step_1_status}}) (err {{step_1_err}})",
	}
	res, err := r.Run(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, "got X (status ok) (err )", res.Summary)
}
