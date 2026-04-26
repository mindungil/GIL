// Package runner implements the autonomous AgentLoop that drives a Frozen
// Spec to completion using a Provider + Tools + Verifier.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/stuck"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Result is the final outcome of an AgentLoop run.
type Result struct {
	Status     string // "done" | "max_iterations" | "error" | "stuck"
	Iterations int
	Tokens     int64
	VerifyAll  []verify.Result
	FinalError error
}

// AgentLoop drives Spec to completion.
type AgentLoop struct {
	Spec     *gilv1.FrozenSpec
	Provider provider.Provider
	Model    string
	Tools    []tool.Tool
	Verifier *verify.Runner
	Events   *event.Stream // optional; if nil, no events emitted

	// Stuck detector + recovery strategy. Both optional. If nil, no detection.
	StuckDetector  *stuck.Detector
	StuckStrategy  stuck.Strategy // currently ModelEscalateStrategy
	ModelChain     []string       // ordered list for ModelEscalateStrategy
	StuckThreshold int            // abort after this many UN-recovered signals; default 3 if zero
	StuckCheckEvery int           // run detector every N iterations; default 1 if zero

	// internal: buffer of recent events for the detector (ring of last 200)
	recent      []event.Event
	recentMax   int
	unrecovered int // counter of stuck signals not handled by a recovery
}

// NewAgentLoop constructs a loop.
func NewAgentLoop(spec *gilv1.FrozenSpec, prov provider.Provider, model string, tools []tool.Tool, ver *verify.Runner) *AgentLoop {
	return &AgentLoop{Spec: spec, Provider: prov, Model: model, Tools: tools, Verifier: ver}
}

// Run executes the loop until verifier passes, max iterations exhausted, or error.
func (a *AgentLoop) Run(ctx context.Context) (*Result, error) {
	maxIter := 100
	if a.Spec != nil && a.Spec.Budget != nil && a.Spec.Budget.MaxIterations > 0 {
		maxIter = int(a.Spec.Budget.MaxIterations)
	}

	system := buildSystemPrompt(a.Spec, a.Tools)
	tools := make([]provider.ToolDef, 0, len(a.Tools))
	toolByName := map[string]tool.Tool{}
	for _, t := range a.Tools {
		tools = append(tools, provider.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
		toolByName[t.Name()] = t
	}

	messages := []provider.Message{{
		Role:    provider.RoleUser,
		Content: "Begin. Use the tools to satisfy the verification checks. When you believe you're done, stop calling tools.",
	}}

	var totalTokens int64
	for iter := 1; iter <= maxIter; iter++ {
		a.emit(event.SourceSystem, event.KindNote, "iteration_start", map[string]any{"iter": iter})

		a.emit(event.SourceAgent, event.KindAction, "provider_request", map[string]any{
			"model":   a.Model,
			"msgs":    len(messages),
			"tools":   len(tools),
		})

		resp, err := a.Provider.Complete(ctx, provider.Request{
			Model:     a.Model,
			System:    system,
			Messages:  messages,
			Tools:     tools,
			MaxTokens: 4096,
		})
		if err != nil {
			a.emit(event.SourceSystem, event.KindNote, "run_error", map[string]any{"err": err.Error()})
			return &Result{Status: "error", Iterations: iter, FinalError: err}, err
		}
		totalTokens += resp.InputTokens + resp.OutputTokens

		a.emit(event.SourceAgent, event.KindObservation, "provider_response", map[string]any{
			"text_len":      len(resp.Text),
			"tool_calls":    len(resp.ToolCalls),
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
			"stop_reason":   resp.StopReason,
		})

		// Stuck detection: run after each provider_response (or every N iters).
		if a.StuckDetector != nil {
			every := a.StuckCheckEvery
			if every <= 0 {
				every = 1
			}
			if iter%every == 0 {
				signals := a.StuckDetector.Check(a.recent)
				for _, sig := range signals {
					a.emit(event.SourceSystem, event.KindNote, "stuck_detected", map[string]any{
						"pattern": sig.Pattern.String(),
						"detail":  sig.Detail,
						"count":   sig.Count,
					})
					recovered := false
					if a.StuckStrategy != nil {
						dec, err := a.StuckStrategy.Apply(ctx, stuck.ApplyRequest{
							Signal:       sig,
							CurrentModel: a.Model,
							ModelChain:   a.ModelChain,
							Iteration:    iter,
						})
						if err == nil && dec.Action == stuck.ActionSwitchModel {
							a.emit(event.SourceSystem, event.KindNote, "stuck_recovered", map[string]any{
								"strategy":    a.StuckStrategy.Name(),
								"new_model":   dec.NewModel,
								"explanation": dec.Explanation,
							})
							a.Model = dec.NewModel
							recovered = true
						}
					}
					if !recovered {
						a.unrecovered++
					}
				}
				threshold := a.StuckThreshold
				if threshold <= 0 {
					threshold = 3
				}
				if a.unrecovered >= threshold {
					a.emit(event.SourceSystem, event.KindNote, "stuck_unrecovered", map[string]any{
						"count":      a.unrecovered,
						"threshold":  threshold,
						"iterations": iter,
					})
					return &Result{
						Status:     "stuck",
						Iterations: iter,
						Tokens:     totalTokens,
						FinalError: errors.New("aborted: 3 unrecovered stuck signals"),
					}, nil
				}
			}
		}

		// Append assistant turn (with tool_calls if any)
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			// No more tool calls — assume agent thinks it's done. Run verifier.
			a.emit(event.SourceSystem, event.KindAction, "verify_run", nil)
			results, allPass := a.Verifier.RunAll(ctx, a.Spec.GetVerification().GetChecks())
			for _, vr := range results {
				a.emit(event.SourceEnvironment, event.KindObservation, "verify_result", map[string]any{
					"name":      vr.Name,
					"passed":    vr.Passed,
					"exit_code": vr.ExitCode,
				})
			}
			if allPass {
				a.emit(event.SourceSystem, event.KindNote, "run_done", map[string]any{"iterations": iter, "tokens": totalTokens})
				return &Result{Status: "done", Iterations: iter, Tokens: totalTokens, VerifyAll: results}, nil
			}
			// Feed verifier failures back as a user message and let agent continue.
			messages = append(messages, provider.Message{
				Role:    provider.RoleUser,
				Content: formatVerifyFeedback(results),
			})
			continue
		}

		// Execute each tool call, build tool_result blocks for next user message
		toolResults := make([]provider.ToolResult, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			a.emit(event.SourceAgent, event.KindAction, "tool_call", map[string]any{
				"name":  tc.Name,
				"input": truncateJSON(tc.Input, 512),
			})

			t, ok := toolByName[tc.Name]
			if !ok {
				toolResults = append(toolResults, provider.ToolResult{
					ToolUseID: tc.ID,
					Content:   fmt.Sprintf("unknown tool: %s", tc.Name),
					IsError:   true,
				})
				a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
					"name":     tc.Name,
					"is_error": true,
					"content":  "unknown tool",
				})
				continue
			}
			r, err := t.Run(ctx, tc.Input)
			if err != nil {
				msg := err.Error()
				if len(msg) > 500 {
					msg = msg[:500] + "... (truncated)"
				}
				toolResults = append(toolResults, provider.ToolResult{
					ToolUseID: tc.ID,
					Content:   "tool error: " + msg,
					IsError:   true,
				})
				a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
					"name":     tc.Name,
					"is_error": true,
					"content":  truncateString(msg, 512),
				})
				continue
			}
			toolResults = append(toolResults, provider.ToolResult{
				ToolUseID: tc.ID,
				Content:   r.Content,
				IsError:   r.IsError,
			})
			a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
				"name":     tc.Name,
				"is_error": r.IsError,
				"content":  truncateString(r.Content, 512),
			})
		}
		messages = append(messages, provider.Message{
			Role:        provider.RoleUser,
			ToolResults: toolResults,
		})
	}

	a.emit(event.SourceSystem, event.KindNote, "run_max_iterations", map[string]any{"iterations": maxIter, "tokens": totalTokens})
	return &Result{Status: "max_iterations", Iterations: maxIter, Tokens: totalTokens}, nil
}

func buildSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool) string {
	goal := "(no goal specified)"
	if spec != nil && spec.Goal != nil {
		goal = spec.Goal.OneLiner
	}

	var toolList string
	for _, t := range tools {
		toolList += fmt.Sprintf("- %s: %s\n", t.Name(), t.Description())
	}

	var checkList string
	if spec != nil && spec.Verification != nil {
		for _, c := range spec.Verification.Checks {
			checkList += fmt.Sprintf("- %s: `%s`\n", c.Name, c.Command)
		}
	}
	if checkList == "" {
		checkList = "(no checks defined — any non-tool response will be considered done)\n"
	}

	return fmt.Sprintf(`You are an autonomous coding agent. Your job is to make all verification checks pass.

Goal: %s

Verification checks (all must pass):
%s
Available tools:
%s
Strategy:
1. Use tools to inspect, write, or run code.
2. Verify your work matches the check commands above before stopping.
3. When you believe all checks will pass, stop calling tools — the system will run the checks.
4. If any check fails, you'll receive the output and should fix the issue.

Be decisive. Don't ask the user — they're not present. Make reasonable assumptions and act.`, goal, checkList, toolList)
}

func formatVerifyFeedback(results []verify.Result) string {
	out := "Verification results:\n"
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		out += fmt.Sprintf("- %s: %s (exit=%d)\n", r.Name, status, r.ExitCode)
		if !r.Passed {
			if r.Stdout != "" {
				out += "  stdout: " + r.Stdout + "\n"
			}
			if r.Stderr != "" {
				out += "  stderr: " + r.Stderr + "\n"
			}
		}
	}
	out += "\nKeep going — fix the failing checks."
	return out
}

// emit appends an event to a.Events if non-nil and always buffers locally for
// stuck detection (bounded ring buffer of recentMax events).
func (a *AgentLoop) emit(source event.Source, kind event.Kind, eventType string, data any) {
	var dataJSON []byte
	if data != nil {
		dataJSON, _ = json.Marshal(data)
	}
	e := event.Event{
		Timestamp: time.Now().UTC(),
		Source:    source,
		Kind:      kind,
		Type:      eventType,
		Data:      dataJSON,
	}
	if a.Events != nil {
		_, _ = a.Events.Append(e)
	}
	// Always buffer locally for stuck detection (cheap, bounded).
	if a.recentMax == 0 {
		a.recentMax = 200
	}
	a.recent = append(a.recent, e)
	if len(a.recent) > a.recentMax {
		a.recent = a.recent[len(a.recent)-a.recentMax:]
	}
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func truncateJSON(b []byte, max int) string {
	return truncateString(string(b), max)
}
