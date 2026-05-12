package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// agent_tools_lifecycle.go — G1 follow-up to the deferral noted in
// agent_tools.go:99. Three agent tools that complete the chat-agent
// session lifecycle: freeze_spec (FrozenSpec producer that was deleted
// with InterviewService in M3), start_run (kicks the runner loop on a
// frozen session), apply_diff (honest, state-aware response after
// show_diff).
//
// All three are exposed only on the "default" agent profile — explore
// is read-only, plan is plan-only. The system prompt steers when to
// call them; the agent decides, the system enforces invariants
// (already-frozen rejects, session-status gating).

// --- freeze_spec ----------------------------------------------------

// toolFreezeSpec is the producer side for FrozenSpec. M3 deleted the
// InterviewService that used to populate spec.yaml; without a producer
// the system_prompt's FrozenSpec slot has been empty in chat mode. The
// agent now decides when it has gathered enough understanding from the
// conversation and calls this tool with the structured slots.
//
// The schema is intentionally lightweight for V1: only goal.one_liner
// is required. Optional slots (constraints, verification, budget,
// autonomy) follow the existing FrozenSpec proto shape. Workspace and
// models inherit from the session config; the agent doesn't need to
// echo them back.
type toolFreezeSpec struct {
	sess *SessionService
	base string // sessionsBase — matches show_spec's pattern.
}

func (t *toolFreezeSpec) name() string { return "freeze_spec" }

func (t *toolFreezeSpec) description() string {
	return "Freeze the session's goal + constraints + verification into a persistent spec. " +
		"Call when the user has agreed on what to do AND wants to proceed to an autonomous run. " +
		"Only `goal.one_liner` is required; pass whatever else you've extracted from the conversation. " +
		"Once frozen the spec is immutable for this session — refusing further changes is the point. " +
		"After freezing you may call start_run to kick the runner loop."
}

func (t *toolFreezeSpec) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"goal": {
				"type": "object",
				"properties": {
					"one_liner": {"type": "string", "description": "Required. Single-sentence statement of what the user wants done."},
					"detailed": {"type": "string", "description": "Optional multi-paragraph elaboration."},
					"success_criteria": {"type": "array", "items": {"type": "string"}, "description": "Natural-language criteria the user agreed signal completion."},
					"non_goals": {"type": "array", "items": {"type": "string"}, "description": "What is explicitly out of scope."}
				},
				"required": ["one_liner"],
				"additionalProperties": false
			},
			"constraints": {
				"type": "object",
				"properties": {
					"tech_stack": {"type": "array", "items": {"type": "string"}},
					"forbidden": {"type": "array", "items": {"type": "string"}},
					"license": {"type": "string"},
					"code_style": {"type": "string"}
				},
				"additionalProperties": false
			},
			"verification": {
				"type": "object",
				"properties": {
					"checks": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"name": {"type": "string"},
								"command": {"type": "string"},
								"expected_exit_code": {"type": "integer"}
							},
							"required": ["name", "command"],
							"additionalProperties": false
						}
					}
				},
				"additionalProperties": false
			},
			"budget": {
				"type": "object",
				"properties": {
					"max_iterations": {"type": "integer"},
					"max_total_tokens": {"type": "integer"},
					"max_total_cost_usd": {"type": "number"}
				},
				"additionalProperties": false
			},
			"autonomy": {
				"type": "string",
				"enum": ["plan_only", "ask_per_action", "ask_destructive_only", "full"]
			}
		},
		"required": ["goal"],
		"additionalProperties": false
	}`)
}

// freezeSpecArgs is the parsed shape of the agent's freeze_spec call.
// Mirrors the schema above. JSON tags use snake_case to match the
// schema; unmarshal silently drops unknown fields.
type freezeSpecArgs struct {
	Goal struct {
		OneLiner         string   `json:"one_liner"`
		Detailed         string   `json:"detailed"`
		SuccessCriteria  []string `json:"success_criteria"`
		NonGoals         []string `json:"non_goals"`
	} `json:"goal"`
	Constraints struct {
		TechStack []string `json:"tech_stack"`
		Forbidden []string `json:"forbidden"`
		License   string   `json:"license"`
		CodeStyle string   `json:"code_style"`
	} `json:"constraints"`
	Verification struct {
		Checks []struct {
			Name             string `json:"name"`
			Command          string `json:"command"`
			ExpectedExitCode int32  `json:"expected_exit_code"`
		} `json:"checks"`
	} `json:"verification"`
	Budget struct {
		MaxIterations   int32   `json:"max_iterations"`
		MaxTotalTokens  int64   `json:"max_total_tokens"`
		MaxTotalCostUsd float64 `json:"max_total_cost_usd"`
	} `json:"budget"`
	Autonomy string `json:"autonomy"`
}

func (t *toolFreezeSpec) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	if t.base == "" {
		return provider.ToolResult{
			Content: "freeze_spec unavailable: daemon has no on-disk session store",
			IsError: true,
		}, nil
	}

	var args freezeSpecArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{
			Content: "freeze_spec invalid arguments: " + err.Error(),
			IsError: true,
		}, nil
	}
	if strings.TrimSpace(args.Goal.OneLiner) == "" {
		return provider.ToolResult{
			Content: "freeze_spec requires goal.one_liner — a single-sentence statement of the goal",
			IsError: true,
		}, nil
	}

	dir := filepath.Join(t.base, sessionID)
	store := specstore.NewStore(dir)
	if store.IsFrozen() {
		return provider.ToolResult{
			Content: "spec is already frozen for this session — freeze is one-shot per session. " +
				"Call show_spec to read the existing spec, or start a new session to write a different one.",
			IsError: true,
		}, nil
	}

	fs := buildFrozenSpec(sessionID, args)
	if err := store.Save(fs); err != nil {
		return provider.ToolResult{
			Content: "freeze_spec failed to write spec.yaml: " + err.Error(),
			IsError: true,
		}, nil
	}
	if err := store.Freeze(); err != nil {
		return provider.ToolResult{
			Content: "freeze_spec wrote spec.yaml but failed to compute lock: " + err.Error(),
			IsError: true,
		}, nil
	}
	if t.sess != nil && t.sess.repo != nil {
		// Mark the session frozen so RunService.Start (and other
		// downstream gates) accept it. Failure to update status is a
		// soft warning — spec.yaml and spec.lock are already written
		// and the next status-bump can fix the drift.
		if uerr := t.sess.repo.UpdateStatus(ctx, sessionID, "frozen"); uerr != nil &&
			!errors.Is(uerr, session.ErrNotFound) {
			return provider.ToolResult{
				Content: fmt.Sprintf("spec frozen but status update failed: %v", uerr),
				IsError: true,
			}, nil
		}
	}

	return provider.ToolResult{Content: renderFreezeSummary(fs)}, nil
}

// buildFrozenSpec converts agent-provided args into a FrozenSpec proto.
// Empty slots stay nil so layered workspace defaults can fill them at
// RunService.Start time — the spec is intentionally minimal.
func buildFrozenSpec(sessionID string, args freezeSpecArgs) *gilv1.FrozenSpec {
	fs := &gilv1.FrozenSpec{
		SpecId:    ulid.Make().String(),
		SessionId: sessionID,
		FrozenAt:  timestamppb.New(time.Now().UTC()),
		Goal: &gilv1.Goal{
			OneLiner:               args.Goal.OneLiner,
			Detailed:               args.Goal.Detailed,
			SuccessCriteriaNatural: args.Goal.SuccessCriteria,
			NonGoals:               args.Goal.NonGoals,
		},
	}
	if len(args.Constraints.TechStack) > 0 ||
		len(args.Constraints.Forbidden) > 0 ||
		args.Constraints.License != "" ||
		args.Constraints.CodeStyle != "" {
		fs.Constraints = &gilv1.Constraints{
			TechStack: args.Constraints.TechStack,
			Forbidden: args.Constraints.Forbidden,
			License:   args.Constraints.License,
			CodeStyle: args.Constraints.CodeStyle,
		}
	}
	if len(args.Verification.Checks) > 0 {
		checks := make([]*gilv1.Check, 0, len(args.Verification.Checks))
		for _, c := range args.Verification.Checks {
			checks = append(checks, &gilv1.Check{
				Name:             c.Name,
				Kind:             gilv1.CheckKind_SHELL,
				Command:          c.Command,
				ExpectedExitCode: c.ExpectedExitCode,
			})
		}
		fs.Verification = &gilv1.Verification{Checks: checks}
	}
	if args.Budget.MaxIterations != 0 ||
		args.Budget.MaxTotalTokens != 0 ||
		args.Budget.MaxTotalCostUsd != 0 {
		fs.Budget = &gilv1.Budget{
			MaxIterations:   args.Budget.MaxIterations,
			MaxTotalTokens:  args.Budget.MaxTotalTokens,
			MaxTotalCostUsd: args.Budget.MaxTotalCostUsd,
		}
	}
	if dial := parseAutonomyDial(args.Autonomy); dial != gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED {
		fs.Risk = &gilv1.RiskProfile{Autonomy: dial}
	}
	return fs
}

func parseAutonomyDial(s string) gilv1.AutonomyDial {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "plan_only":
		return gilv1.AutonomyDial_PLAN_ONLY
	case "ask_per_action":
		return gilv1.AutonomyDial_ASK_PER_ACTION
	case "ask_destructive_only":
		return gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY
	case "full":
		return gilv1.AutonomyDial_FULL
	default:
		return gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED
	}
}

// renderFreezeSummary builds the human-readable confirmation the agent
// reads back to the user. Keep it terse so the agent can paraphrase
// rather than dumping the whole proto.
func renderFreezeSummary(fs *gilv1.FrozenSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "spec frozen · spec_id=%s\n", fs.SpecId)
	fmt.Fprintf(&b, "goal: %s\n", fs.Goal.OneLiner)
	if len(fs.Goal.SuccessCriteriaNatural) > 0 {
		fmt.Fprintf(&b, "success criteria: %d\n", len(fs.Goal.SuccessCriteriaNatural))
	}
	if len(fs.Goal.NonGoals) > 0 {
		fmt.Fprintf(&b, "non-goals: %d\n", len(fs.Goal.NonGoals))
	}
	if fs.Verification != nil && len(fs.Verification.Checks) > 0 {
		fmt.Fprintf(&b, "verification checks: %d\n", len(fs.Verification.Checks))
	}
	if fs.Budget != nil {
		if fs.Budget.MaxIterations > 0 {
			fmt.Fprintf(&b, "budget: max_iterations=%d\n", fs.Budget.MaxIterations)
		}
	}
	if fs.Risk != nil && fs.Risk.Autonomy != gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED {
		fmt.Fprintf(&b, "autonomy: %s\n", strings.ToLower(strings.TrimPrefix(fs.Risk.Autonomy.String(), "AUTONOMY_")))
	}
	b.WriteString("session status → frozen. Call start_run to begin the autonomous run.")
	return b.String()
}

// --- start_run ------------------------------------------------------

// toolStartRun kicks the RunService loop on a frozen session. Always
// runs detached so the chat turn returns immediately; the run streams
// progress via the existing event mechanism (Tail RPC, run_progress
// events). The agent reports back the run start; further observation
// is the user's call ("how's it going?" → show_status tool).
type toolStartRun struct {
	rs *RunService
}

func (t *toolStartRun) name() string { return "start_run" }

func (t *toolStartRun) description() string {
	return "Start the autonomous run loop on the session's frozen spec. " +
		"Requires freeze_spec to have been called first. " +
		"Runs detached — returns immediately; check progress via show_status. " +
		"The runner follows the spec's verification checks until they pass or budget exhausts."
}

func (t *toolStartRun) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"provider": {"type": "string", "description": "Optional provider override. Empty = use session config."},
			"model": {"type": "string", "description": "Optional model override. Empty = use spec.models.main."}
		},
		"additionalProperties": false
	}`)
}

type startRunArgs struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (t *toolStartRun) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	if t.rs == nil {
		return provider.ToolResult{
			Content: "start_run unavailable: chat agent loop has no RunService wired",
			IsError: true,
		}, nil
	}

	var args startRunArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return provider.ToolResult{
				Content: "start_run invalid arguments: " + err.Error(),
				IsError: true,
			}, nil
		}
	}

	// RunService.Start does its own frozen-status gate, but we
	// pre-check so the agent gets a clear "freeze first" message
	// rather than a generic FailedPrecondition.
	sess, err := t.rs.repo.Get(ctx, sessionID)
	if err != nil {
		return provider.ToolResult{
			Content: "start_run session lookup failed: " + err.Error(),
			IsError: true,
		}, nil
	}
	if sess.Status != "frozen" {
		return provider.ToolResult{
			Content: fmt.Sprintf("start_run requires a frozen spec — current session status is %q. Call freeze_spec first.", sess.Status),
			IsError: true,
		}, nil
	}

	resp, err := t.rs.Start(ctx, &gilv1.StartRunRequest{
		SessionId: sessionID,
		Provider:  args.Provider,
		Model:     args.Model,
		Detach:    true,
	})
	if err != nil {
		return provider.ToolResult{
			Content: "start_run failed: " + err.Error(),
			IsError: true,
		}, nil
	}
	return provider.ToolResult{Content: fmt.Sprintf(
		"run started (status=%s). The runner will stream progress; use show_status to check iter/tokens/cost or list_sessions to see all sessions.",
		resp.GetStatus(),
	)}, nil
}

// --- apply_diff -----------------------------------------------------

// toolApplyDiff is the V1 honest implementation of the "apply it" verb.
// In chat mode, edits via write_file/edit_file/apply_patch land
// directly on the working directory — there is nothing pending to
// apply. We tell the agent that, so it can communicate the architecture
// to the user instead of pretending a no-op succeeded.
//
// In run mode the runner writes to its workspace (which may be a
// shadow checkout depending on backend); a "merge back" verb is not
// yet defined, so we surface the diff and explain that explicitly.
// The future merge-semantics work (followup, not G1) will replace this
// branch.
type toolApplyDiff struct {
	rs      *RunService
	tracker *turnDiffTracker
}

func (t *toolApplyDiff) name() string { return "apply_diff" }

func (t *toolApplyDiff) description() string {
	return "Confirm the agent's edits are applied to the workspace. " +
		"In chat mode edits via write_file/edit_file/apply_patch write directly to your working directory — " +
		"this tool reports what was applied this turn. " +
		"Use after show_diff when the user says 'apply it' / 'looks good, apply' / '적용해'."
}

func (t *toolApplyDiff) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolApplyDiff) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	// Run mode: a runner is or was active for this session. Surface
	// the unified diff from the latest checkpoint and a clear note
	// that an explicit merge verb is not yet implemented.
	if t.rs != nil {
		resp, err := t.rs.Diff(ctx, &gilv1.DiffRequest{SessionId: sessionID})
		if err == nil && resp.GetUnifiedDiff() != "" {
			summary := fmt.Sprintf(
				"%d files changed, +%d/-%d lines (run-mode shadow diff)\n\n%s\n\n"+
					"NOTE: run-mode 'merge to original workspace' is not yet implemented. "+
					"The runner has written to its workspace path per spec.workspace; "+
					"there is no separate apply step.",
				resp.GetFilesChanged(),
				resp.GetLinesAdded(),
				resp.GetLinesRemoved(),
				resp.GetUnifiedDiff(),
			)
			return provider.ToolResult{Content: summary}, nil
		}
	}

	// Chat mode: edits already wrote to working_dir. Show the
	// turn-scoped tracker summary so the agent can confirm what
	// landed.
	if t.tracker != nil {
		files, polluted := t.tracker.snapshot(sessionID)
		if len(files) > 0 || polluted {
			body, fileCount, added, removed := renderTrackerSummary(files, polluted)
			summary := fmt.Sprintf(
				"%d files changed, +%d/-%d (already applied to working directory)\n%s\n"+
					"Chat-mode edits write directly. To roll back use shadow-git checkpoint restore.",
				fileCount, added, removed, body,
			)
			return provider.ToolResult{Content: summary}, nil
		}
	}

	return provider.ToolResult{Content: "no edits to apply this turn"}, nil
}
