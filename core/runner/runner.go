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

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/compact"
	"github.com/mindungil/gil/core/cost"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/instructions"
	"github.com/mindungil/gil/core/memory"
	"github.com/mindungil/gil/core/permission"
	"github.com/mindungil/gil/core/plan"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/stuck"
	"github.com/mindungil/gil/core/tool"
	"github.com/mindungil/gil/core/verify"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"google.golang.org/protobuf/proto"
)

// Result is the final outcome of an AgentLoop run.
//
// Status values: "done" | "max_iterations" | "error" | "stuck" |
// "budget_exhausted". When Status == "budget_exhausted", BudgetReason
// records which dimension hit the cap ("tokens" or "cost").
type Result struct {
	Status       string
	Iterations   int
	Tokens       int64
	CostUSD      float64
	VerifyAll    []verify.Result
	FinalError   error
	FinalText    string // last assistant text emitted before the loop exited
	BudgetReason string // "tokens" | "cost" — populated when Status=="budget_exhausted"
}

// AskRequest carries the details surfaced to a human reviewer when
// Permission returns DecisionAsk and AskCallback is non-nil.
type AskRequest struct {
	Tool string
	Key  string
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

	// AdversaryModel is used by AdversaryConsultStrategy. If empty, falls back
	// to a.Model.
	AdversaryModel string

	// Permission, when non-nil, gates each tool call. nil → no gating (Allow all).
	// The interface form (Decider) lets the server pass an EvaluatorWithStore
	// so persistent + session-scoped allow/deny rules layer on top of the
	// spec-derived Evaluator without the runner needing to know about them.
	Permission permission.Decider

	// AskCallback, when non-nil AND Permission returns Ask, is called to ask
	// for human permission. Returning true treats the call as Allow; false as
	// Deny. Called synchronously from the run goroutine; should respect ctx.
	AskCallback func(ctx context.Context, req AskRequest) bool

	// Memory bank, optional. If non-nil, the system prompt prepends bank
	// contents (full when small, progress-only when large).
	Memory *memory.Bank

	// Plan, when non-nil, is the per-session run plan (TODO checklist).
	// SessionID below disambiguates which session's plan to load. The
	// runner reads the plan once per iteration to prepend a brief
	// summary into the system prompt. All mutations flow through the
	// plan tool, never the loop directly. Both nil-checked: leaving
	// Plan unset disables the feature entirely (no prompt prepend).
	Plan      *plan.Store
	SessionID string

	// Compactor + budget. If nil, no compaction.
	Compactor        *compact.Compactor
	MaxContextTokens int // default 200_000 if zero; compaction triggers at 0.95 * this

	// SeedUserMessage overrides the default "Begin. Use the tools..." first
	// user message. Used by RunSubagent to inject a custom subgoal.
	SeedUserMessage string

	// Workspace, when non-empty, is the root from which AGENTS.md /
	// CLAUDE.md / .cursor/rules/*.mdc are discovered at Run() startup and
	// injected into the system prompt between the base prompt and the
	// memory bank. Empty disables discovery silently — the runner does
	// NOT fall back to cwd because gild owns the per-session workspace
	// and a wrong default would leak the wrong project's conventions
	// into the model.
	Workspace string

	// InstructionSources, when non-nil, overrides discovery: the runner
	// renders these directly. Used by tests and by callers that want to
	// pre-seed instructions from somewhere other than the on-disk walk.
	InstructionSources []instructions.Source

	// InstructionDiscoverOptions, when set, is passed through to the
	// instructions.Discover call at Run() startup. Defaults are sensible
	// (StopAtGitRoot=true, no global/home dirs); callers that want to
	// include $XDG_CONFIG_HOME/gil/AGENTS.md should set GlobalConfigDir
	// here.
	InstructionDiscoverOptions instructions.DiscoverOptions

	// instructionsRendered is the resolved + rendered string built once at
	// Run() startup; reused across iterations so the cache prefix stays
	// stable for prompt-caching providers.
	instructionsRendered string

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

	// CostCalculator estimates USD spend per iteration so the runner can
	// enforce Budget.MaxTotalCostUsd. When nil and the budget enables
	// cost enforcement, Run() lazily constructs one from the embedded
	// catalog. Models absent from the catalog cost $0 and never trigger
	// the cost cap (warned but not enforced); the iteration/token caps
	// still apply.
	CostCalculator *cost.Calculator

	// internal: tracks which budget thresholds we've already warned about
	// so each crossing emits one budget_warning rather than one per
	// iteration after 75%.
	warnedTokens bool
	warnedCost   bool
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

// QueueSystemNote stages a one-shot note that will be appended to the
// system prompt for the NEXT iteration only. Subsequent calls overwrite
// any pending note (single-shot semantics — only the most recent
// suggestion gets through). Used by RunService.PostHint to deliver
// surface-issued hints (e.g., model preference) without preempting the
// in-flight tool call. The agent decides whether to honor it.
func (a *AgentLoop) QueueSystemNote(note string) {
	a.extraSystemNote = note
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

	// Resolve project-level instructions (AGENTS.md / CLAUDE.md /
	// cursor rules) once per run. The result lives between the base
	// system prompt and the memory bank and is invariant for the life of
	// the run — important so the cache prefix stays stable across
	// iterations on prompt-caching providers (Anthropic system block).
	a.loadInstructions()
	system := buildSystemPrompt(a.Spec, a.Tools, a.Memory, a.instructionsRendered)
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

	seedMessage := a.SeedUserMessage
	if seedMessage == "" {
		seedMessage = "Begin. Use the tools to satisfy the verification checks. When you believe you're done, stop calling tools."
	}
	messages := []provider.Message{{
		Role:    provider.RoleUser,
		Content: seedMessage,
	}}
	var lastAssistantText string

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
	var totalInTokens, totalOutTokens int64
	var totalCostUSD float64
	// Resolve budget caps once. Zero means "unbounded on this dimension".
	var budgetMaxTokens int64
	var budgetMaxCostUSD float64
	if a.Spec != nil && a.Spec.Budget != nil {
		budgetMaxTokens = a.Spec.Budget.MaxTotalTokens
		budgetMaxCostUSD = a.Spec.Budget.MaxTotalCostUsd
	}
	// Lazily build a calculator only when cost enforcement is wanted.
	if a.CostCalculator == nil && budgetMaxCostUSD > 0 {
		a.CostCalculator = cost.NewCalculator()
	}
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
		// Plan prepend (Phase 18): when the agent has populated a plan
		// for this session, include a short summary at the top of the
		// system prompt so the model carries it across iterations and
		// after compaction. We render fresh from disk each iteration —
		// the plan is the source of truth, not whatever rendering the
		// previous iteration emitted. Empty plan → no prepend (keeps
		// the cache prefix stable for sessions that never use plan).
		if a.Plan != nil && a.SessionID != "" {
			if pl, perr := a.Plan.Load(a.SessionID); perr == nil && !pl.IsEmpty() {
				iterSystem = renderPlanForPrompt(pl) + "\n\n" + iterSystem
			}
		}
		if a.extraSystemNote != "" {
			iterSystem = iterSystem + "\n\n## URGENT NOTE\n" + a.extraSystemNote
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
			return &Result{Status: "error", Iterations: iter, Tokens: totalTokens, CostUSD: totalCostUSD, FinalError: err, FinalText: lastAssistantText}, err
		}
		totalTokens += resp.InputTokens + resp.OutputTokens
		totalInTokens += resp.InputTokens
		totalOutTokens += resp.OutputTokens
		if a.CostCalculator != nil {
			if usd, ok := a.CostCalculator.Estimate(a.Model, cost.Usage{
				InputTokens:  resp.InputTokens,
				OutputTokens: resp.OutputTokens,
			}); ok {
				totalCostUSD += usd
			}
		}

		a.emit(event.SourceAgent, event.KindObservation, "provider_response", map[string]any{
			"text_len":      len(resp.Text),
			"tool_calls":    len(resp.ToolCalls),
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
			"stop_reason":   resp.StopReason,
		})

		// Budget enforcement: emit warning at the 75% threshold (once per
		// dimension), and stop with status=budget_exhausted at >=100%.
		// Both emissions are additive — existing event consumers ignoring
		// these types keep working. Iteration cap remains the for-loop
		// guard above; here we add token + cost dimensions.
		if budgetMaxTokens > 0 {
			frac := float64(totalTokens) / float64(budgetMaxTokens)
			if !a.warnedTokens && frac >= 0.75 && frac < 1.0 {
				a.warnedTokens = true
				a.emit(event.SourceSystem, event.KindNote, "budget_warning", map[string]any{
					"reason":   "tokens",
					"used":     totalTokens,
					"limit":    budgetMaxTokens,
					"fraction": frac,
				})
			}
			if frac >= 1.0 {
				a.emit(event.SourceSystem, event.KindNote, "budget_exceeded", map[string]any{
					"reason":   "tokens",
					"used":     totalTokens,
					"limit":    budgetMaxTokens,
					"fraction": frac,
				})
				return &Result{
					Status:       "budget_exhausted",
					Iterations:   iter,
					Tokens:       totalTokens,
					CostUSD:      totalCostUSD,
					BudgetReason: "tokens",
					FinalText:    lastAssistantText,
				}, nil
			}
		}
		if budgetMaxCostUSD > 0 {
			frac := totalCostUSD / budgetMaxCostUSD
			if !a.warnedCost && frac >= 0.75 && frac < 1.0 {
				a.warnedCost = true
				a.emit(event.SourceSystem, event.KindNote, "budget_warning", map[string]any{
					"reason":   "cost",
					"used":     totalCostUSD,
					"limit":    budgetMaxCostUSD,
					"fraction": frac,
				})
			}
			if frac >= 1.0 {
				a.emit(event.SourceSystem, event.KindNote, "budget_exceeded", map[string]any{
					"reason":   "cost",
					"used":     totalCostUSD,
					"limit":    budgetMaxCostUSD,
					"fraction": frac,
				})
				return &Result{
					Status:       "budget_exhausted",
					Iterations:   iter,
					Tokens:       totalTokens,
					CostUSD:      totalCostUSD,
					BudgetReason: "cost",
					FinalText:    lastAssistantText,
				}, nil
			}
		}
		_ = totalInTokens
		_ = totalOutTokens

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
							Signal:         sig,
							CurrentModel:   a.Model,
							ModelChain:     a.ModelChain,
							Iteration:      iter,
							Checkpoint:     a.Checkpoint,
							Provider:       a.Provider,
							AdversaryModel: a.AdversaryModel,
							RecentMessages: snapshotMessages(messages, 10),
							SubagentRunner: a,
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
						if err == nil && dec.Action == stuck.ActionAdversaryConsult {
							a.emit(event.SourceSystem, event.KindNote, "stuck_recovered", map[string]any{
								"strategy":    a.StuckStrategy.Name(),
								"action":      "adversary_consult",
								"explanation": dec.Explanation,
							})
							a.extraSystemNote = dec.Explanation // same single-shot mechanism as AltToolOrder
							recovered = true
						}
						if err == nil && dec.Action == stuck.ActionSubagentBranch {
							a.emit(event.SourceSystem, event.KindNote, "stuck_recovered", map[string]any{
								"strategy":    a.StuckStrategy.Name(),
								"action":      "subagent_branch",
								"explanation": dec.Explanation,
							})
							a.extraSystemNote = dec.Explanation // same single-shot mechanism as AltToolOrder
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
						CostUSD:    totalCostUSD,
						FinalError: errors.New("aborted: 3 unrecovered stuck signals"),
						FinalText:  lastAssistantText,
					}, nil
				}
			}
		}

		// Append assistant turn (with tool_calls if any)
		if resp.Text != "" {
			lastAssistantText = resp.Text
		}
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
						// Emit a NOTE-kind event with a non-error name so downstream
						// consumers that filter on "*_error" do not flag this as a
						// real error — milestone summarization is best-effort.
						a.emit(event.SourceSystem, event.KindNote, "memory_milestone_skipped", map[string]any{
							"reason": "provider_unavailable",
							"detail": mErr.Error(),
						})
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
				return &Result{Status: "done", Iterations: iter, Tokens: totalTokens, CostUSD: totalCostUSD, VerifyAll: results, FinalText: lastAssistantText}, nil
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

			if a.Permission != nil {
				key := permissionKeyFor(tc.Name, tc.Input)
				decision := a.Permission.Evaluate(tc.Name, key)
				if decision != permission.DecisionAllow {
					// When decision is Ask and AskCallback is wired, call it.
					// If it returns true we treat the call as Allow (fall through);
					// if false (or no callback), treat as Deny.
					if decision == permission.DecisionAsk && a.AskCallback != nil {
						if a.AskCallback(ctx, AskRequest{Tool: tc.Name, Key: key}) {
							// Human approved — fall through to actual tool dispatch.
							goto dispatchTool
						}
					}
					// Deny path (DecisionDeny, or Ask with no callback, or Ask+callback=false).
					reason := "permission denied"
					if decision == permission.DecisionAsk {
						reason = "permission requires interactive ask (not supported in this run mode); treating as deny"
					}
					a.emit(event.SourceSystem, event.KindNote, "permission_denied", map[string]any{
						"tool":     tc.Name,
						"key":      key,
						"decision": decision.String(),
					})
					toolResults = append(toolResults, provider.ToolResult{
						ToolUseID: tc.ID,
						Content:   reason,
						IsError:   true,
					})
					a.emit(event.SourceEnvironment, event.KindObservation, "tool_result", map[string]any{
						"name":             tc.Name,
						"is_error":         true,
						"content":          reason,
						"permission_block": true,
					})
					continue
				}
			}
		dispatchTool:

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
	return &Result{Status: "max_iterations", Iterations: maxIter, Tokens: totalTokens, CostUSD: totalCostUSD, FinalText: lastAssistantText}, nil
}

// SubagentConfig parameterises a sub-loop spawn. Used by both stuck-recovery
// (via the legacy RunSubagent shape) and the agent-callable subagent tool.
//
// All fields except Goal are optional:
//   - AllowedTools: filter for the parent's tool set. Empty → use the
//     conservative read-only default (read_file + repomap + memory_load +
//     web_fetch + lsp).
//   - MaxIterations: hard cap on sub-loop iters. Zero or negative → 8.
//     Clamped to 20 so a runaway prompt can't blow the parent's budget.
//   - MaxTokens: soft cap on the sub-loop's combined input+output token
//     usage. The cap is plumbed through the cloned Budget so the runner's
//     existing token-budget enforcement triggers a clean budget_exhausted
//     return rather than running away. Zero → 30_000.
//   - Model: provider model id override. Empty → reuse the parent's model.
type SubagentConfig struct {
	Goal          string
	AllowedTools  []string
	MaxIterations int
	MaxTokens     int64
	Model         string
}

// SubagentResult is what the parent sees after a sub-loop completes.
// Tokens is the combined input+output count so the parent can charge the
// sub-loop's spend against its own budget if it wants to (the agent-callable
// subagent tool inspects this on the parent's side).
type SubagentResult struct {
	Summary    string
	Status     string
	Iterations int
	Tokens     int64
}

// Default tools for a sub-loop when the caller passes no AllowedTools.
// The list matches the read-only investigation surface called out in the
// subagent tool description: read_file, repomap, memory_load, web_fetch,
// lsp. NO bash, edit, write_file, apply_patch, exec — those are explicitly
// dropped so sub-loops stay side-effect-free.
var defaultSubagentTools = []string{
	"read_file",
	"repomap",
	"memory_load",
	"web_fetch",
	"lsp",
}

// Bounds enforced on every SubagentConfig regardless of caller — keeps a
// runaway tool call from spending the parent's budget on an ill-conceived
// sub-loop.
const (
	subagentMaxIterCeiling   = 20
	subagentDefaultMaxIter   = 8
	subagentDefaultMaxTokens = 30_000
)

// RunSubagent satisfies stuck.SubagentRunner. Thin wrapper over
// RunSubagentWithConfig that preserves the original positional shape
// (subgoal, allowedTools, maxIters) the stuck-recovery interface expects.
//
// Stuck-recovery callers (SubagentBranch) come through this path; the
// agent-callable subagent tool calls RunSubagentWithConfig directly so it
// can plumb token caps + model overrides + return token usage.
func (a *AgentLoop) RunSubagent(ctx context.Context, subgoal string, allowedTools []string, maxIters int) (string, error) {
	res, err := a.RunSubagentWithConfig(ctx, SubagentConfig{
		Goal:          subgoal,
		AllowedTools:  allowedTools,
		MaxIterations: maxIters,
	})
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Summary, nil
}

// RunSubagentWithConfig is the full-fidelity entrypoint used by the
// agent-callable subagent tool. It clones the parent's spec, applies the
// config's iteration + token caps, filters the tool set, and runs the
// sub-loop in-process (NO new session, NO new event stream — the sub-loop
// inherits the parent's stream so subagent_started / subagent_done /
// provider_request events all surface to the same TUI/CLI viewers).
func (a *AgentLoop) RunSubagentWithConfig(ctx context.Context, cfg SubagentConfig) (*SubagentResult, error) {
	if strings.TrimSpace(cfg.Goal) == "" {
		return nil, errors.New("subagent: goal is required")
	}

	// Resolve caps with defaults + ceilings.
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = subagentDefaultMaxIter
	}
	if maxIter > subagentMaxIterCeiling {
		maxIter = subagentMaxIterCeiling
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = subagentDefaultMaxTokens
	}

	// Resolve allowed tools. Empty → conservative read-only default.
	allowedTools := cfg.AllowedTools
	if len(allowedTools) == 0 {
		allowedTools = defaultSubagentTools
	}
	allow := make(map[string]bool, len(allowedTools))
	for _, t := range allowedTools {
		allow[t] = true
	}
	var subTools []tool.Tool
	var actualToolNames []string
	for _, t := range a.Tools {
		if allow[t.Name()] {
			subTools = append(subTools, t)
			actualToolNames = append(actualToolNames, t.Name())
		}
	}

	// Build a sub-spec that overrides iteration + token caps for the
	// sub-loop only. Clone keeps the parent's spec untouched.
	var subSpec *gilv1.FrozenSpec
	if a.Spec != nil {
		subSpec = proto.Clone(a.Spec).(*gilv1.FrozenSpec)
	} else {
		subSpec = &gilv1.FrozenSpec{}
	}
	if subSpec.Budget == nil {
		subSpec.Budget = &gilv1.Budget{}
	}
	subSpec.Budget.MaxIterations = int32(maxIter)
	subSpec.Budget.MaxTotalTokens = maxTokens
	// Cost budget intentionally not inherited; the parent's
	// CostCalculator already accounts for every provider call (the
	// sub-loop reuses the same provider) and a separate cap on the child
	// would double-count.
	subSpec.Budget.MaxTotalCostUsd = 0
	if subSpec.Verification == nil {
		subSpec.Verification = &gilv1.Verification{}
	}
	// No verifier checks for sub-loops: their goal is reconnaissance, not verify-pass.
	subSpec.Verification.Checks = nil

	model := cfg.Model
	if model == "" {
		model = a.Model
	}

	a.emit(event.SourceSystem, event.KindNote, "subagent_started", map[string]any{
		"goal":           cfg.Goal,
		"max_iterations": maxIter,
		"max_tokens":     maxTokens,
		"model":          model,
		"tools":          actualToolNames,
	})

	sub := &AgentLoop{
		Spec:            subSpec,
		Provider:        a.Provider,
		Model:           model,
		Tools:           subTools,
		Verifier:        verify.NewRunner(""),
		SeedUserMessage: cfg.Goal,
		// Deliberately do NOT share the parent's event stream — the
		// parent has already emitted subagent_started above and emits
		// subagent_done with the final summary on return. Plumbing every
		// per-iteration sub-loop event (provider_request / tool_call /
		// run_done) into the parent stream would (1) confuse stream
		// consumers that can't tell parent vs child events apart and
		// (2) break stuck-recovery wiring that filters on run_done /
		// run_max_iterations to know when the *parent* finished. The
		// sub-loop runs to completion silently; the parent's two
		// subagent_* events are the surface API.
		//
		// Inherit memory bank READ-ONLY: the runner reads it for the
		// system-prompt prepend, but without memory_update in the
		// allowed-tool default the sub-loop can't write back.
		Memory: a.Memory,
		// Workspace flows through so AGENTS.md / CLAUDE.md project
		// instructions feed into the sub-loop's system prompt the same
		// way they feed the parent's.
		Workspace: a.Workspace,
		// Deliberately leave Stuck/Checkpoint/Plan/Permission/AskCallback nil
		// so the sub-loop is a clean, sandbox-free, no-side-effect investigator.
		// (Permission gating is enforced at the PARENT level when the agent
		// invokes the subagent tool — the sub-loop's restricted tool set
		// makes a second permission layer redundant.)
	}

	res, err := sub.Run(ctx)
	out := &SubagentResult{}
	if res != nil {
		out.Summary = res.FinalText
		out.Status = res.Status
		out.Iterations = res.Iterations
		out.Tokens = res.Tokens
	}
	a.emit(event.SourceSystem, event.KindNote, "subagent_done", map[string]any{
		"goal":       cfg.Goal,
		"status":     out.Status,
		"iterations": out.Iterations,
		"tokens":     out.Tokens,
		"summary":    truncateString(out.Summary, 512),
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

// AsSubagentRunner returns an adapter that satisfies tool.SubagentRunner
// for the agent-callable subagent tool. The interface lives in core/tool
// (alongside the tool itself) to avoid an import cycle (runner → tool →
// runner). This adapter lives here because it has to reach into the
// AgentLoop's RunSubagentWithConfig — declaring it on the runner side
// keeps the tool side completely loop-agnostic.
func (a *AgentLoop) AsSubagentRunner() tool.SubagentRunner {
	return &subagentRunnerAdapter{loop: a}
}

type subagentRunnerAdapter struct {
	loop *AgentLoop
}

func (s *subagentRunnerAdapter) RunSubagentWithConfig(ctx context.Context, cfg tool.SubagentRunConfig) (tool.SubagentRunResult, error) {
	res, err := s.loop.RunSubagentWithConfig(ctx, SubagentConfig{
		Goal:          cfg.Goal,
		AllowedTools:  cfg.AllowedTools,
		MaxIterations: cfg.MaxIterations,
		MaxTokens:     cfg.MaxTokens,
		Model:         cfg.Model,
	})
	out := tool.SubagentRunResult{}
	if res != nil {
		out.Summary = res.Summary
		out.Status = res.Status
		out.Iterations = res.Iterations
		out.Tokens = res.Tokens
	}
	return out, err
}

func buildSystemPrompt(spec *gilv1.FrozenSpec, tools []tool.Tool, bank *memory.Bank, instructionsSection string) string {
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

	// Order is fixed: base → instructions (AGENTS.md/CLAUDE.md/cursor)
	// → memory bank. Instructions sit between base and bank because
	// they're persistent project context (model should read them once
	// per run, never per-iteration), whereas the bank can churn between
	// iterations as the agent updates progress.md.
	out := base
	if instructionsSection != "" {
		out += "\n\n## Project Instructions\n\nThe following content was discovered from AGENTS.md, CLAUDE.md, and/or .cursor/rules/*.mdc files in this workspace and its ancestors. Treat it as durable project conventions and persona signals from the user.\n\n" + instructionsSection
	}
	if bank == nil {
		return out
	}
	bankSection, err := buildMemoryBankSection(bank)
	if err != nil {
		// Soft failure: skip prepend, log nothing here (caller may emit event)
		return out
	}
	if bankSection == "" {
		return out
	}
	return out + "\n\n" + bankSection
}

// loadInstructions populates a.instructionsRendered exactly once per
// AgentLoop. Called from Run() before the first iteration. When neither
// Workspace nor InstructionSources is provided the call is a no-op and
// no event is emitted.
func (a *AgentLoop) loadInstructions() {
	// Pre-rendered override beats discovery.
	if len(a.InstructionSources) > 0 {
		a.instructionsRendered = instructions.Render(a.InstructionSources, a.InstructionDiscoverOptions.MaxBytes)
		a.emit(event.SourceSystem, event.KindNote, "system_instructions_loaded", map[string]any{
			"sources": len(a.InstructionSources),
			"bytes":   len(a.instructionsRendered),
			"source":  "override",
		})
		return
	}
	// No workspace → silently skip discovery (do NOT default to cwd).
	if a.Workspace == "" {
		return
	}

	opts := a.InstructionDiscoverOptions
	opts.Workspace = a.Workspace
	// Default to stop-at-git-root unless caller explicitly set otherwise.
	// We can't distinguish "false because zero value" from "false because
	// caller asked for full walk", so the convention is: callers either
	// accept the default (true) or set StopAtGitRoot=true explicitly. The
	// rare "walk all the way up" case sets it to false, which we honour.
	if !opts.StopAtGitRoot && opts.GlobalConfigDir == "" && opts.HomeDir == "" {
		// Heuristic: if the caller hasn't set anything, prefer git-root
		// stop because monorepos are the common case.
		opts.StopAtGitRoot = true
	}

	srcs, err := instructions.Discover(opts)
	if err != nil {
		a.emit(event.SourceSystem, event.KindNote, "system_instructions_error", map[string]any{
			"err": err.Error(),
		})
		return
	}
	if len(srcs) == 0 {
		return
	}
	a.instructionsRendered = instructions.Render(srcs, opts.MaxBytes)
	paths := make([]string, 0, len(srcs))
	for _, s := range srcs {
		paths = append(paths, s.Path)
	}
	a.emit(event.SourceSystem, event.KindNote, "system_instructions_loaded", map[string]any{
		"sources": len(srcs),
		"bytes":   len(a.instructionsRendered),
		"paths":   paths,
		"source":  "discover",
	})
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

// snapshotMessages returns a copy of the last n messages (or all if fewer).
func snapshotMessages(msgs []provider.Message, n int) []provider.Message {
	start := len(msgs) - n
	if start < 0 {
		start = 0
	}
	out := make([]provider.Message, len(msgs)-start)
	copy(out, msgs[start:])
	return out
}

// permissionKeyFor extracts the tool-specific detail string used as the
// permission key. Best-effort: parses tc.Input as JSON and pulls a
// well-known field (command for bash, path for file ops, blocks/patch for
// edit/apply_patch). Falls back to the raw input JSON when no field matches.
func permissionKeyFor(toolName string, input json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch toolName {
	case "bash":
		if v, ok := m["command"].(string); ok {
			return v
		}
	case "write_file", "read_file":
		if v, ok := m["path"].(string); ok {
			return v
		}
	case "memory_update", "memory_load":
		if v, ok := m["file"].(string); ok {
			return v
		}
	case "edit":
		// Per-block path matching is not meaningful (one tool call may touch
		// many files). Use empty key — rules should match by tool name only.
		return ""
	case "apply_patch":
		return ""
	case "repomap", "compact_now", "plan":
		return ""
	case "lsp":
		// Use the operation name so users can scope persistent rules
		// (e.g., always allow lsp/hover but ask on lsp/rename). The
		// agent passes operation as the discriminator field on every
		// call, so this stays meaningful even across renames.
		if v, ok := m["operation"].(string); ok {
			return v
		}
		return ""
	case "web_fetch":
		// Use the URL as the rule key so users can pin allow/deny
		// patterns by host or full URL (e.g., "https://internal.corp/*").
		if v, ok := m["url"].(string); ok {
			return v
		}
		return ""
	case "web_search":
		// Use the query so users can deny obviously-sensitive lookups
		// at the rule layer if they wish.
		if v, ok := m["query"].(string); ok {
			return v
		}
		return ""
	}
	return ""
}

// renderPlanForPrompt builds the brief plan summary that gets prepended
// to the per-iteration system prompt. Format follows the spec:
//
//	=== PLAN (3 items: 1 done, 1 in progress, 1 pending) ===
//	✓ analyze repomap
//	● refactor theme provider
//	○ add toggle
//	=========================================================
//
// Aesthetic glyphs (✓ ● ○) per terminal-aesthetic.md §3 (Iconography).
// We use the Unicode glyphs unconditionally here — this string lives in
// the system prompt sent to the model, not on the human's terminal, so
// the locale-based ASCII fallback that the TUI/CLI apply doesn't apply.
//
// One level of sub-items is rendered with two-space indent. Note text
// (when set) is appended as " — <note>" to keep one-item-per-line.
func renderPlanForPrompt(p *plan.Plan) string {
	if p == nil || len(p.Items) == 0 {
		return ""
	}
	pen, ip, comp := p.Counts()
	total := pen + ip + comp
	header := fmt.Sprintf("=== PLAN (%d items: %d done, %d in progress, %d pending) ===",
		total, comp, ip, pen)

	var lines []string
	lines = append(lines, header)
	for _, it := range p.Items {
		lines = append(lines, planLine(it, ""))
		for _, sub := range it.Sub {
			lines = append(lines, planLine(sub, "  "))
		}
	}
	footer := strings.Repeat("=", len(header))
	lines = append(lines, footer)
	return strings.Join(lines, "\n")
}

// planLine renders one item under renderPlanForPrompt with the given
// indent prefix. Glyphs are spec-aligned: ✓ done, ● in progress, ○
// pending.
func planLine(it plan.Item, indent string) string {
	var glyph string
	switch it.Status {
	case plan.Completed:
		glyph = "✓"
	case plan.InProgress:
		glyph = "●"
	default:
		glyph = "○"
	}
	body := fmt.Sprintf("%s%s %s", indent, glyph, it.Text)
	if it.Note != "" {
		body += " — " + it.Note
	}
	return body
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
