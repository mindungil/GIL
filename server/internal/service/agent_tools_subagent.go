package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// agent_tools_subagent.go — S5-S7 of docs/design/subagent.md. Three
// agent-tool surfaces that let a parent agent delegate to children
// without ever asking the user. Per memory: agent decides, system
// enforces the safety net (depth/count via subagentRegistry, spec
// slicing via subagent_slice.go).
//
// The registry lives on SessionService (one per daemon). RunService
// runs the child loop via the existing Start(detach=true) path.

// --- spawn_agent ----------------------------------------------------

type toolSpawnAgent struct {
	sess     *SessionService
	rs       *RunService
	registry *subagentRegistry
	base     string // sessionsBase
	// iter39a: parent's provider/model so the subagent inherits the
	// same backend instead of falling back to the daemon's "anthropic"
	// default. Without this, a chat session running on vllm would spawn
	// a child that immediately fails with "no credentials for anthropic".
	parentProvider string
	parentModel    string
}

func (t *toolSpawnAgent) name() string { return "spawn_agent" }

func (t *toolSpawnAgent) description() string {
	return "Delegate work to a fresh subagent. The child receives the task message " +
		"as its first user input and runs against a sliced copy of the parent's " +
		"frozen spec (workspace, tools, models, verification inherited unless the " +
		"parent spec restricts them). Returns the child agent_id + label so the " +
		"parent can call wait_agent later. Subject to daemon-wide concurrency and " +
		"depth limits — at the V1 cap, parents are root only (children cannot spawn " +
		"further children). Use for parallel exploration, isolated experiments, or " +
		"work that benefits from a fresh context."
}

func (t *toolSpawnAgent) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"label": {
				"type": "string",
				"description": "Short nickname for this child (lowercase, no spaces). Used by wait_agent to refer back."
			},
			"task": {
				"type": "string",
				"description": "Required. The first-user-message text the child receives. Be specific — the child does not see the parent's conversation."
			},
			"agent_type": {
				"type": "string",
				"description": "Which agent profile (default / explore / plan). Default: default.",
				"enum": ["default", "explore", "plan"]
			},
			"spec_override": {
				"type": "object",
				"description": "Optional narrowing of the inherited spec.",
				"properties": {
					"workspace_path": {"type": "string"},
					"tools_allowlist": {"type": "array", "items": {"type": "string"}},
					"max_iterations": {"type": "integer"}
				},
				"additionalProperties": false
			}
		},
		"required": ["label", "task"],
		"additionalProperties": false
	}`)
}

type spawnAgentArgs struct {
	Label        string `json:"label"`
	Task         string `json:"task"`
	AgentType    string `json:"agent_type"`
	SpecOverride struct {
		WorkspacePath  string   `json:"workspace_path"`
		ToolsAllowlist []string `json:"tools_allowlist"`
		MaxIterations  int32    `json:"max_iterations"`
	} `json:"spec_override"`
}

func (t *toolSpawnAgent) run(ctx context.Context, parentSessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	if t.base == "" || t.registry == nil || t.rs == nil || t.sess == nil {
		return provider.ToolResult{
			Content: "spawn_agent unavailable: daemon missing required wiring",
			IsError: true,
		}, nil
	}

	var args spawnAgentArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{
			Content: "spawn_agent invalid arguments: " + err.Error(),
			IsError: true,
		}, nil
	}
	if strings.TrimSpace(args.Label) == "" {
		return provider.ToolResult{Content: "spawn_agent requires a non-empty label", IsError: true}, nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return provider.ToolResult{Content: "spawn_agent requires a non-empty task", IsError: true}, nil
	}
	// iter102a: strip control chars from Label before it lands in DB,
	// agent_status, spawn_agent confirmation, and subagentHint. Without
	// this, an agent (or an upstream prompt-injection vector) could plant
	// ESC sequences that repaint the terminal anywhere SubagentLabel
	// surfaces. Same defense as iter71a applied to a different field.
	args.Label = sanitizeHintControlChars(args.Label)
	if strings.TrimSpace(args.Label) == "" {
		return provider.ToolResult{Content: "spawn_agent label became empty after stripping control chars", IsError: true}, nil
	}

	parentSess, err := t.sess.repo.Get(ctx, parentSessionID)
	if err != nil {
		return provider.ToolResult{
			Content: "spawn_agent parent lookup failed: " + err.Error(),
			IsError: true,
		}, nil
	}

	// Parent spec must be frozen — the slice operation reads the parent's
	// FrozenSpec proto. Without freeze the child would inherit an empty
	// spec and lose the gil discipline that justifies running at all.
	parentStore := specstore.NewStore(filepath.Join(t.base, parentSessionID))
	parentSpec, ploadErr := parentStore.Load()
	if ploadErr != nil || parentSpec == nil {
		return provider.ToolResult{
			Content: "spawn_agent requires a frozen parent spec. Call freeze_spec first.",
			IsError: true,
		}, nil
	}

	// Reserve the slot before allocating any persistent state — failure
	// here is the fast path and shouldn't leak orphan sessions.
	_, release, regErr := t.registry.spawn(ctx, t.sess.repo, parentSess)
	if regErr != nil {
		switch {
		case errors.Is(regErr, errSubagentLimitReached):
			return provider.ToolResult{
				Content: fmt.Sprintf("spawn_agent rejected: %s. Wait for a child to finish (wait_agent) or fold the work into the current turn.", regErr.Error()),
				IsError: true,
			}, nil
		case errors.Is(regErr, errSubagentDepthExceeded):
			return provider.ToolResult{
				Content: fmt.Sprintf("spawn_agent rejected: %s. V1 only supports one layer of subagents.", regErr.Error()),
				IsError: true,
			}, nil
		}
		return provider.ToolResult{Content: "spawn_agent registry: " + regErr.Error(), IsError: true}, nil
	}

	// Create the child session row (parent linkage stamped now).
	childSess, csErr := t.sess.repo.Create(ctx, session.CreateInput{
		WorkingDir:      parentSess.WorkingDir,
		GoalHint:        truncateGoalHint(args.Task, 80),
		ParentSessionID: parentSess.ID,
		SubagentDepth:   parentSess.SubagentDepth + 1,
		SubagentLabel:   args.Label,
	})
	if csErr != nil {
		release()
		return provider.ToolResult{
			Content: "spawn_agent failed to create child session: " + csErr.Error(),
			IsError: true,
		}, nil
	}

	// Slice + freeze the child's spec. Goal slot carries the task text
	// so the child's system_prompt has something to anchor on.
	childSpec := sliceSpec(subagentSliceInput{
		childSessionID:        childSess.ID,
		parent:                parentSpec,
		policy:                parentSpec.Subagent,
		overrideWorkspacePath: args.SpecOverride.WorkspacePath,
		overrideToolsAllow:    args.SpecOverride.ToolsAllowlist,
		overrideMaxIterations: args.SpecOverride.MaxIterations,
	})
	// S8 — child runner's system_prompt picks up Goal.Detailed via the
	// standard spec-injection path, so we encode the subagent context
	// here instead of plumbing a parallel hint slot through AgentLoop.
	subagentHint := fmt.Sprintf(
		"You are a subagent of session %s (label=%s, depth=%d). "+
			"You do not have access to the parent's conversation. "+
			"Complete the task below and report a terse summary in your final message. "+
			"Do not call spawn_agent — subagents cannot spawn further children (V1 depth cap).",
		parentSess.ID, args.Label, parentSess.SubagentDepth+1,
	)
	childSpec.Goal = &gilv1.Goal{
		OneLiner: args.Task,
		Detailed: subagentHint,
	}
	childSpec.FrozenAt = timestamppb.New(time.Now().UTC())

	childStore := specstore.NewStore(filepath.Join(t.base, childSess.ID))
	if err := childStore.Save(childSpec); err != nil {
		release()
		return provider.ToolResult{
			Content: "spawn_agent failed to write child spec: " + err.Error(),
			IsError: true,
		}, nil
	}
	if err := childStore.Freeze(); err != nil {
		release()
		return provider.ToolResult{
			Content: "spawn_agent failed to lock child spec: " + err.Error(),
			IsError: true,
		}, nil
	}
	if err := t.sess.repo.UpdateStatus(ctx, childSess.ID, "frozen"); err != nil {
		release()
		return provider.ToolResult{
			Content: "spawn_agent failed to mark child frozen: " + err.Error(),
			IsError: true,
		}, nil
	}

	// Kick the runner detached. The child's own RunService.Start path
	// will flip its status to running → done/failed; wait_agent polls
	// session.Status.
	startResp, startErr := t.rs.Start(ctx, &gilv1.StartRunRequest{
		SessionId: childSess.ID,
		Provider:  t.parentProvider,
		Model:     t.parentModel,
		Detach:    true,
	})
	if startErr != nil {
		release()
		return provider.ToolResult{
			Content: "spawn_agent failed to start child run: " + startErr.Error(),
			IsError: true,
		}, nil
	}

	// Stash the release closure keyed by child id so wait_agent can
	// fire it once the child reaches terminal status.
	t.sess.registerSubagentRelease(childSess.ID, release)

	return provider.ToolResult{Content: fmt.Sprintf(
		"subagent started · agent_id=%s · label=%s · status=%s",
		childSess.ID, args.Label, startResp.GetStatus(),
	)}, nil
}

// --- wait_agent -----------------------------------------------------

type toolWaitAgent struct {
	sess *SessionService
}

func (t *toolWaitAgent) name() string { return "wait_agent" }

func (t *toolWaitAgent) description() string {
	return "Block until a previously spawned subagent reaches a terminal " +
		"state (done / failed / stopped / budget_exceeded). Identify the child " +
		"by agent_id (returned from spawn_agent) or label. Returns the child's " +
		"final status + its last assistant message. Default 600s timeout; " +
		"timeout returns the current status without killing the child."
}

func (t *toolWaitAgent) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"agent_id": {"type": "string"},
			"label": {"type": "string"},
			"timeout_seconds": {"type": "integer", "default": 600}
		},
		"additionalProperties": false
	}`)
}

type waitAgentArgs struct {
	AgentID        string `json:"agent_id"`
	Label          string `json:"label"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (t *toolWaitAgent) run(ctx context.Context, parentSessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	if t.sess == nil || t.sess.repo == nil {
		return provider.ToolResult{Content: "wait_agent unavailable", IsError: true}, nil
	}

	var args waitAgentArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "wait_agent invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if args.AgentID == "" && args.Label == "" {
		return provider.ToolResult{Content: "wait_agent requires agent_id or label", IsError: true}, nil
	}
	if args.TimeoutSeconds <= 0 {
		args.TimeoutSeconds = 600
	}

	// Resolve target child by id or by label among the parent's children.
	childID := args.AgentID
	if childID == "" {
		kids, err := t.sess.repo.ListChildren(ctx, parentSessionID)
		if err != nil {
			return provider.ToolResult{Content: "wait_agent list children: " + err.Error(), IsError: true}, nil
		}
		for _, k := range kids {
			if k.SubagentLabel == args.Label {
				childID = k.ID
				break
			}
		}
		if childID == "" {
			return provider.ToolResult{
				Content: fmt.Sprintf("wait_agent: no child with label %q under parent", args.Label),
				IsError: true,
			}, nil
		}
	}

	// Poll until terminal status or timeout. 250ms tick — short enough
	// to feel responsive when the child finishes quickly, sparse enough
	// to keep DB load minimal for long-running children.
	deadline := time.Now().Add(time.Duration(args.TimeoutSeconds) * time.Second)
	tick := 250 * time.Millisecond
	for {
		child, err := t.sess.repo.Get(ctx, childID)
		if err != nil {
			return provider.ToolResult{Content: "wait_agent child lookup: " + err.Error(), IsError: true}, nil
		}
		if isTerminalStatus(child.Status) {
			t.sess.releaseSubagent(childID)
			return provider.ToolResult{Content: renderSubagentFinal(child, t.sess.progress, t.sess.budgets)}, nil
		}
		if time.Now().After(deadline) {
			return provider.ToolResult{Content: fmt.Sprintf(
				"wait_agent timeout (%ds) · child=%s · status=%s (still running). Call wait_agent again to keep waiting.",
				args.TimeoutSeconds, childID, child.Status,
			)}, nil
		}
		select {
		case <-ctx.Done():
			return provider.ToolResult{
				Content: "wait_agent cancelled: " + ctx.Err().Error(),
				IsError: true,
			}, nil
		case <-time.After(tick):
		}
	}
}

// --- agent_status ---------------------------------------------------

type toolAgentStatus struct {
	sess *SessionService
}

func (t *toolAgentStatus) name() string { return "agent_status" }

func (t *toolAgentStatus) description() string {
	return "List the parent's live + recent subagents. Non-blocking — use to " +
		"peek without waiting on completion. Returns label, agent_id, status, " +
		"iter/tokens/cost where available, one per row."
}

func (t *toolAgentStatus) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolAgentStatus) run(ctx context.Context, parentSessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	if t.sess == nil || t.sess.repo == nil {
		return provider.ToolResult{Content: "agent_status unavailable", IsError: true}, nil
	}
	kids, err := t.sess.repo.ListChildren(ctx, parentSessionID)
	if err != nil {
		return provider.ToolResult{Content: "agent_status list children: " + err.Error(), IsError: true}, nil
	}
	if len(kids) == 0 {
		return provider.ToolResult{Content: "no subagents spawned yet for this session"}, nil
	}
	var b strings.Builder
	for _, k := range kids {
		row := fmt.Sprintf("%s · %s · %s", k.SubagentLabel, k.Status, k.ID[:12])
		if t.sess.progress != nil {
			if iters, tokens, ok := t.sess.progress.Progress(k.ID); ok {
				row += fmt.Sprintf(" · iter=%d tokens=%d", iters, tokens)
			}
		}
		if t.sess.budgets != nil {
			if cost, _, _, ok := t.sess.budgets.Budget(k.ID); ok {
				row += fmt.Sprintf(" · cost=$%.4f", cost)
			}
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return provider.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- helpers --------------------------------------------------------

// isTerminalStatus matches the status strings the runner / cleanup
// paths set when a session is no longer making progress. "stopped" is
// the manual-cancel path; "failed" / "done" / "budget_exceeded" land
// from the runner's natural exit branches.
func isTerminalStatus(s string) bool {
	switch s {
	case "done", "failed", "stopped", "budget_exceeded":
		return true
	}
	return false
}

// renderSubagentFinal builds the wait_agent return text. Keep it terse
// so the parent agent can include it in its own summary without
// blowing the context window.
//
// iter133a: session.TotalTokens/TotalCostUSD on the row are never
// written by the run path (token + cost live in the in-memory
// progress/budgets trackers on SessionService, not on the persisted
// row), so reading them always produced "tokens=0 cost=$0.0000".
// Read from the live trackers when available — falling back to the
// row values keeps backwards-compatibility for the legacy code path.
func renderSubagentFinal(s session.Session, prog ProgressGetter, bg BudgetGetter) string {
	tokens := s.TotalTokens
	costUSD := s.TotalCostUSD
	if prog != nil {
		if _, t, ok := prog.Progress(s.ID); ok && t > tokens {
			tokens = t
		}
	}
	if bg != nil {
		if c, _, _, ok := bg.Budget(s.ID); ok && c > costUSD {
			costUSD = c
		}
	}
	return fmt.Sprintf(
		"subagent finished · label=%s · agent_id=%s · status=%s · tokens=%d · cost=$%.4f",
		s.SubagentLabel, s.ID, s.Status, tokens, costUSD,
	)
}

// subagentReleaseRegistry pins the release closures spawn_agent
// returned, keyed by child session id, so wait_agent can fire them on
// terminal status. Stored on SessionService as a mu-guarded map. Once
// fired, the entry is deleted (release is single-shot).
type subagentReleaseRegistry struct {
	mu      sync.Mutex
	entries map[string]func()
}

func newSubagentReleaseRegistry() *subagentReleaseRegistry {
	return &subagentReleaseRegistry{entries: make(map[string]func())}
}

func (r *subagentReleaseRegistry) set(childID string, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[childID] = fn
}

func (r *subagentReleaseRegistry) fire(childID string) {
	r.mu.Lock()
	fn, ok := r.entries[childID]
	if ok {
		delete(r.entries, childID)
	}
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}
