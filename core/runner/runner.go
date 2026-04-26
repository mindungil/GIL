// Package runner implements the autonomous AgentLoop that drives a Frozen
// Spec to completion using a Provider + Tools + Verifier.
package runner

import (
	"context"
	"fmt"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Result is the final outcome of an AgentLoop run.
type Result struct {
	Status     string          // "done" | "max_iterations" | "error"
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
		resp, err := a.Provider.Complete(ctx, provider.Request{
			Model:     a.Model,
			System:    system,
			Messages:  messages,
			Tools:     tools,
			MaxTokens: 4096,
		})
		if err != nil {
			return &Result{Status: "error", Iterations: iter, FinalError: err}, err
		}
		totalTokens += resp.InputTokens + resp.OutputTokens

		// Append assistant turn (with tool_calls if any)
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			// No more tool calls — assume agent thinks it's done. Run verifier.
			results, allPass := a.Verifier.RunAll(ctx, a.Spec.GetVerification().GetChecks())
			if allPass {
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
			t, ok := toolByName[tc.Name]
			if !ok {
				toolResults = append(toolResults, provider.ToolResult{
					ToolUseID: tc.ID,
					Content:   fmt.Sprintf("unknown tool: %s", tc.Name),
					IsError:   true,
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
				continue
			}
			toolResults = append(toolResults, provider.ToolResult{
				ToolUseID: tc.ID,
				Content:   r.Content,
				IsError:   r.IsError,
			})
		}
		messages = append(messages, provider.Message{
			Role:        provider.RoleUser,
			ToolResults: toolResults,
		})
	}

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
