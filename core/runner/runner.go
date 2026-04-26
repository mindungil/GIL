// Package runner implements the autonomous AgentLoop that drives a Frozen
// Spec to completion using a Provider + Tools + Verifier.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/jedutools/gil/core/compact"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/memory"
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

	// Checkpoint is optional; if non-nil, committed after each tool-using iteration.
	Checkpoint *checkpoint.ShadowGit

	// Stuck detector + recovery strategy. Both optional. If nil, no detection.
	StuckDetector  *stuck.Detector
	StuckStrategy  stuck.Strategy // currently ModelEscalateStrategy
	ModelChain     []string       // ordered list for ModelEscalateStrategy
	StuckThreshold int            // abort after this many UN-recovered signals; default 3 if zero
	StuckCheckEvery int           // run detector every N iterations; default 1 if zero

	// Memory bank, optional. If non-nil, the system prompt prepends bank
	// contents (full when small, progress-only when large).
	Memory *memory.Bank

	// Compactor + budget. If nil, no compaction.
	Compactor        *compact.Compactor
	MaxContextTokens int // default 200_000 if zero; compaction triggers at 0.95 * this

	// internal: buffer of recent events for the detector (ring of last 200)
	recent      []event.Event
	recentMax   int
	unrecovered int // counter of stuck signals not handled by a recovery

	// internal: set by compact_now tool; cleared after one compaction
	compactNowRequested bool

	// extraSystemNote, when non-empty, is appended to the system prompt for the
	// NEXT iteration only and then cleared. Used by stuck recovery strategies
	// (AltToolOrder, AdversaryConsult) to inject one-shot guidance.
	extraSystemNote string
}

// CompactRequester is satisfied by AgentLoop; the compact_now tool uses it
// to signal that compaction should run at the next iteration boundary.
type CompactRequester interface {
	RequestCompact()
}

// RequestCompact implements CompactRequester. It sets the flag that causes
// the next iteration to compact before calling the provider.
func (a *AgentLoop) RequestCompact() {
	a.compactNowRequested = true
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

	system := buildSystemPrompt(a.Spec, a.Tools, a.Memory)
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

	if a.Checkpoint != nil {
		if err := a.Checkpoint.Init(ctx); err != nil {
			a.emit(event.SourceSystem, event.KindNote, "checkpoint_init_error", map[string]any{"err": err.Error()})
			// Soft failure: disable checkpointing for the rest of the run.
			a.Checkpoint = nil
		} else {
			a.emit(event.SourceSystem, event.KindNote, "checkpoint_init", map[string]any{"git_dir": a.Checkpoint.GitDir})
		}
	}

	var totalTokens int64
	for iter := 1; iter <= maxIter; iter++ {
		a.emit(event.SourceSystem, event.KindNote, "iteration_start", map[string]any{"iter": iter})

		// Compaction check: runs before provider_request so the context is
		// already trimmed before the next LLM call.
		if a.Compactor != nil {
			maxCtx := a.MaxContextTokens
			if maxCtx <= 0 {
				maxCtx = 200_000
			}
			estimated := estimateMessagesTokens(messages)
			threshold := int64(float64(maxCtx) * 0.95)
			forced := a.compactNowRequested
			a.compactNowRequested = false
			if forced || estimated >= threshold {
				a.emit(event.SourceSystem, event.KindNote, "compact_start", map[string]any{
					"estimated_tokens": estimated,
					"threshold":        threshold,
					"forced":           forced,
				})
				compacted, res, cerr := a.Compactor.Compact(ctx, messages)
				if cerr != nil {
					a.emit(event.SourceSystem, event.KindNote, "compact_error", map[string]any{
						"err": cerr.Error(),
					})
					// Soft failure: continue with current messages.
				} else {
					a.emit(event.SourceSystem, event.KindNote, "compact_done", map[string]any{
						"original":     res.OriginalCount,
						"compacted":    res.CompactedCount,
						"saved_tokens": res.SavedTokens,
						"skipped":      res.Skipped,
					})
					messages = compacted
				}
			}
		}

		a.emit(event.SourceAgent, event.KindAction, "provider_request", map[string]any{
			"model":   a.Model,
			"msgs":    len(messages),
			"tools":   len(tools),
		})

		// Build the effective system prompt for this iteration. When
		// extraSystemNote is set (injected by a stuck-recovery strategy),
		// append it as an URGENT NOTE and then clear it (single-shot).
		iterSystem := system
		if a.extraSystemNote != "" {
			iterSystem = system + "\n\n## URGENT NOTE\n" + a.extraSystemNote
			a.extraSystemNote = "" // single-shot: clear after one use
		}

		resp, err := a.Provider.Complete(ctx, provider.Request{
			Model:     a.Model,
			System:    iterSystem,
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
							Checkpoint:   a.Checkpoint,
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
						if err == nil && dec.Action == stuck.ActionAltToolOrder {
							a.emit(event.SourceSystem, event.KindNote, "stuck_recovered", map[string]any{
								"strategy":    a.StuckStrategy.Name(),
								"action":      "alt_tool_order",
								"explanation": dec.Explanation,
							})
							a.extraSystemNote = dec.Explanation
							recovered = true
						}
						if err == nil && dec.Action == stuck.ActionResetSection && a.Checkpoint != nil {
							rerr := a.Checkpoint.Reset(ctx, dec.RestoreSHA)
							if rerr != nil {
								a.emit(event.SourceSystem, event.KindNote, "stuck_reset_failed", map[string]any{
									"strategy": a.StuckStrategy.Name(),
									"sha":      dec.RestoreSHA,
									"err":      rerr.Error(),
								})
								// not recovered — fall through; let unrecovered counter increment normally
							} else {
								a.emit(event.SourceSystem, event.KindNote, "stuck_reset_section", map[string]any{
									"strategy":    a.StuckStrategy.Name(),
									"sha":         dec.RestoreSHA,
									"explanation": dec.Explanation,
								})
								// After hard reset, also clear the recent buffer so the same patterns
								// don't fire again immediately on the next iteration.
								a.recent = nil
								recovered = true
							}
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
				// Memory milestone gate: if Memory is non-nil, give the agent one
				// chance to call memory_update before we declare done.
				if a.Memory != nil {
					a.emit(event.SourceSystem, event.KindNote, "memory_milestone_start", nil)
					nudge := provider.Message{
						Role:    provider.RoleUser,
						Content: "Verification passed. Before declaring done, review the memory bank: is there anything from this run worth recording for future sessions? If yes, call memory_update once or twice now. If no, just reply with 'no update'.",
					}
					milestoneMsgs := append(messages[:len(messages):len(messages)], nudge)
					mResp, mErr := a.Provider.Complete(ctx, provider.Request{
						Model:     a.Model,
						System:    system,
						Messages:  milestoneMsgs,
						Tools:     tools,
						MaxTokens: 1024,
					})
					if mErr != nil {
						// Soft failure: log + proceed to done as if no milestone existed.
						a.emit(event.SourceSystem, event.KindNote, "memory_milestone_error", map[string]any{"err": mErr.Error()})
					} else {
						totalTokens += mResp.InputTokens + mResp.OutputTokens
						a.emit(event.SourceAgent, event.KindObservation, "memory_milestone_response", map[string]any{
							"tool_calls":  len(mResp.ToolCalls),
							"stop_reason": mResp.StopReason,
						})
						// Dispatch any memory_update / memory_load tool calls.
						// Other tools are ignored (memory-only gate; no broader work).
						for _, tc := range mResp.ToolCalls {
							if tc.Name != "memory_update" && tc.Name != "memory_load" {
								a.emit(event.SourceSystem, event.KindNote, "memory_milestone_tool_skipped", map[string]any{
									"name":   tc.Name,
									"reason": "only memory_* tools allowed at milestone",
								})
								continue
							}
							t, ok := toolByName[tc.Name]
							if !ok {
								continue
							}
							a.emit(event.SourceAgent, event.KindAction, "tool_call", map[string]any{
								"name":      tc.Name,
								"input":     truncateJSON(tc.Input, 512),
								"milestone": true,
							})
							r, err := t.Run(ctx, tc.Input)
							if err != nil {
								a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
									"name":      tc.Name,
									"is_error":  true,
									"content":   truncateString(err.Error(), 512),
									"milestone": true,
								})
								continue
							}
							a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
								"name":      tc.Name,
								"is_error":  r.IsError,
								"content":   truncateString(r.Content, 512),
								"milestone": true,
							})
						}
						a.emit(event.SourceSystem, event.KindNote, "memory_milestone_done", map[string]any{
							"memory_calls": countMemoryCalls(mResp.ToolCalls),
						})
					}
				}
				a.emit(event.SourceSystem, event.KindNote, "run_done", map[string]any{"iterations": iter, "tokens": totalTokens})
				if a.Checkpoint != nil {
					sha, err := a.Checkpoint.Commit(ctx, fmt.Sprintf("done iter %d", iter))
					if err == nil {
						a.emit(event.SourceSystem, event.KindNote, "checkpoint_committed", map[string]any{
							"iter": iter, "sha": sha, "final": true,
						})
					}
				}
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
		if a.Checkpoint != nil && len(resp.ToolCalls) > 0 {
			sha, err := a.Checkpoint.Commit(ctx, fmt.Sprintf("iter %d", iter))
			if err != nil {
				a.emit(event.SourceSystem, event.KindNote, "checkpoint_error", map[string]any{
					"iter": iter,
					"err":  err.Error(),
				})
			} else {
				a.emit(event.SourceSystem, event.KindNote, "checkpoint_committed", map[string]any{
					"iter": iter,
					"sha":  sha,
				})
			}
		}
	}

	a.emit(event.SourceSystem, event.KindNote, "run_max_iterations", map[string]any{"iterations": maxIter, "tokens": totalTokens})
	return &Result{Status: "max_iterations", Iterations: maxIter, Tokens: totalTokens}, nil
}

func buildSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool, bank *memory.Bank) string {
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

	base := fmt.Sprintf(`You are an autonomous coding agent. Your job is to make all verification checks pass.

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

	if bank == nil {
		return base
	}
	bankSection, err := buildMemoryBankSection(bank)
	if err != nil {
		// Soft failure: skip prepend, log nothing here (caller may emit event)
		return base
	}
	if bankSection == "" {
		return base
	}
	return base + "\n\n" + bankSection
}

const bankFullThresholdTokens = 4000

// buildMemoryBankSection returns the markdown to append to the system prompt.
// When the bank's estimated total tokens <= bankFullThresholdTokens, all six
// files are inlined. Otherwise only progress.md is inlined and a hint is
// emitted listing the other files retrievable via memory_load.
func buildMemoryBankSection(bank *memory.Bank) (string, error) {
	tokens, err := bank.EstimateTokens()
	if err != nil {
		return "", err
	}
	snap, err := bank.Snapshot()
	if err != nil {
		return "", err
	}
	if len(snap) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("## Memory Bank\n\n")
	sb.WriteString("These files in the session memory directory persist state across compactions and iterations. Update them via the memory_update tool when you complete meaningful work.\n\n")

	if tokens <= bankFullThresholdTokens {
		for _, name := range memory.AllFiles {
			content, ok := snap[name]
			if !ok {
				continue
			}
			sb.WriteString("### ")
			sb.WriteString(name)
			sb.WriteString("\n\n")
			sb.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}

	// Large bank: only progress.md + hint
	sb.WriteString("(Memory bank exceeds inline budget; only progress is shown. Use memory_load to fetch other files.)\n\n")
	if content, ok := snap[memory.FileProgress]; ok {
		sb.WriteString("### ")
		sb.WriteString(memory.FileProgress)
		sb.WriteString("\n\n")
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Other available files (use memory_load to fetch):\n")
	for _, name := range memory.AllFiles {
		if name == memory.FileProgress {
			continue
		}
		if _, ok := snap[name]; ok {
			sb.WriteString("- ")
			sb.WriteString(name)
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
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

// countMemoryCalls returns how many of the given tool calls are memory_update or memory_load.
func countMemoryCalls(calls []provider.ToolCall) int {
	n := 0
	for _, c := range calls {
		if c.Name == "memory_update" || c.Name == "memory_load" {
			n++
		}
	}
	return n
}

// estimateMessagesTokens uses the same 4-chars-per-token heuristic as compact.estimateTokens.
func estimateMessagesTokens(msgs []provider.Message) int64 {
	var total int64
	for _, m := range msgs {
		total += int64(len(m.Content)) / 4
		for _, tc := range m.ToolCalls {
			total += int64(len(tc.Input)) / 4
		}
		for _, tr := range m.ToolResults {
			total += int64(len(tr.Content)) / 4
		}
	}
	return total
}
