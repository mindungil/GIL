package exec

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mindungil/gil/core/tool"
)

// Runner executes a Recipe.
type Runner struct {
	Tools          []tool.Tool
	StepTimeoutSec int     // 0 → DefaultStepTimeoutSec
	MaxSteps       int     // 0 → DefaultMaxSteps
	MaxOutputBytes int     // 0 → DefaultMaxOutputBytes
	Emit           Emitter // optional
}

// limits returns resolved resource limits, applying defaults for zero values.
func (r *Runner) limits() (stepTimeout, maxSteps, maxOut int) {
	stepTimeout = r.StepTimeoutSec
	if stepTimeout <= 0 {
		stepTimeout = DefaultStepTimeoutSec
	}
	maxSteps = r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	maxOut = r.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}
	return
}

// Run executes the recipe. Returns a Result whose Summary field is the
// template-substituted summary string. Errors only when the recipe itself
// is malformed (e.g., empty Steps); per-step failures are captured in
// StepResults and reflected in the summary.
func (r *Runner) Run(ctx context.Context, recipe Recipe) (*Result, error) {
	if len(recipe.Steps) == 0 {
		return nil, fmt.Errorf("exec.Run: recipe has no steps")
	}
	stepTimeout, maxSteps, maxOut := r.limits()

	if len(recipe.Steps) > maxSteps {
		return nil, fmt.Errorf("exec.Run: recipe has %d steps, exceeding max %d", len(recipe.Steps), maxSteps)
	}

	toolByName := map[string]tool.Tool{}
	for _, t := range r.Tools {
		toolByName[t.Name()] = t
	}

	res := &Result{Steps: make([]StepResult, 0, len(recipe.Steps))}
	for i, step := range recipe.Steps {
		sr := StepResult{Step: i + 1, Tool: step.Tool}
		if r.Emit != nil {
			r.Emit("exec_step_start", map[string]any{"step": sr.Step, "tool": step.Tool})
		}

		t, ok := toolByName[step.Tool]
		if !ok {
			sr.Status = "skipped"
			sr.ErrMsg = "unknown tool: " + step.Tool
			res.Steps = append(res.Steps, sr)
			if r.Emit != nil {
				r.Emit("exec_step_done", srToMap(sr))
			}
			continue
		}

		stepCtx, cancel := context.WithTimeout(ctx, time.Duration(stepTimeout)*time.Second)
		out, err := t.Run(stepCtx, step.Args)
		cancel()

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				sr.Status = "timeout"
				sr.ErrMsg = "step exceeded " + strconv.Itoa(stepTimeout) + "s timeout"
			} else {
				sr.Status = "error"
				sr.ErrMsg = truncate(err.Error(), maxOut)
			}
		} else if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			sr.Status = "timeout"
			sr.ErrMsg = "step exceeded " + strconv.Itoa(stepTimeout) + "s timeout"
		} else if out.IsError {
			sr.Status = "error"
			sr.Output = truncate(out.Content, maxOut)
			sr.ErrMsg = truncate(out.Content, 200) // short error preview
		} else {
			sr.Status = "ok"
			sr.Output = truncate(out.Content, maxOut)
		}

		res.Steps = append(res.Steps, sr)
		if r.Emit != nil {
			r.Emit("exec_step_done", srToMap(sr))
		}
	}

	res.Summary = applyTemplate(recipe.Summary, res.Steps)
	return res, nil
}

// applyTemplate substitutes {{step_N_output}}, {{step_N_status}}, and
// {{step_N_err}} in tmpl. If tmpl is empty, returns a default summary
// listing each step + status.
func applyTemplate(tmpl string, steps []StepResult) string {
	if tmpl == "" {
		var sb strings.Builder
		sb.WriteString("Recipe complete:\n")
		for _, s := range steps {
			fmt.Fprintf(&sb, "- step %d (%s): %s", s.Step, s.Tool, s.Status)
			if s.ErrMsg != "" {
				fmt.Fprintf(&sb, " — %s", s.ErrMsg)
			}
			sb.WriteString("\n")
		}
		return sb.String()
	}
	out := tmpl
	for _, s := range steps {
		out = strings.ReplaceAll(out, fmt.Sprintf("{{step_%d_output}}", s.Step), s.Output)
		out = strings.ReplaceAll(out, fmt.Sprintf("{{step_%d_status}}", s.Step), s.Status)
		out = strings.ReplaceAll(out, fmt.Sprintf("{{step_%d_err}}", s.Step), s.ErrMsg)
	}
	return out
}

// truncate caps s to max bytes, appending a truncation notice if cut.
// Lifted from Hermes MAX_STDOUT_BYTES capping pattern.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated to " + strconv.Itoa(max) + " bytes)"
}

// srToMap converts a StepResult to an event-safe map for the Emitter.
func srToMap(sr StepResult) map[string]any {
	return map[string]any{
		"step":         sr.Step,
		"tool":         sr.Tool,
		"status":       sr.Status,
		"output_bytes": len(sr.Output),
		"err":          sr.ErrMsg,
	}
}
