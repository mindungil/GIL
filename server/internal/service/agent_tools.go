package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	"github.com/mindungil/gil/core/tool"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// agent_tools.go is the V1 tool registry for SessionService.Prompt's
// agent loop (docs/design/chat-architecture.md §2.3). Each tool is a
// thin wrapper around an existing service call. The LLM picks tools
// to invoke based on the user's natural-language input — there is no
// client-side dispatch.
//
// V1 ships read-only meta tools, write/exec tools, M5 verify-loop
// tools, and (since G1) the session-lifecycle tools — freeze_spec,
// start_run, apply_diff. apply_diff is the honest minimal form: it
// reports the turn-scoped tracker in chat mode and surfaces the
// shadow diff in run mode; a separate merge-to-original verb is a
// follow-up beyond G1.

// chatTool is the interface the V1 chat agent loop uses for its tool
// registry. Mirrors core/tool.Tool but takes a sessionID at Run time
// so the implementations can reach into per-session daemon state
// (specstore, run progress, repo) without packing it into the
// argsJSON.
type chatTool interface {
	name() string
	description() string
	schema() json.RawMessage
	run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error)
}

// coreToolAdapter wraps a core/tool.Tool (the run-mode tool shape) as
// a chatTool. Used to surface MCP-advertised tools (which implement
// core/tool.Tool) into the chat agent's registry without duplicating
// the MCP client lifecycle. The sessionID parameter is ignored — MCP
// tools are stateless from the registry's perspective; per-session
// scoping lives in the cache that owns the underlying *mcp.Client.
type coreToolAdapter struct{ t tool.Tool }

func (c *coreToolAdapter) name() string            { return c.t.Name() }
func (c *coreToolAdapter) description() string     { return c.t.Description() }
func (c *coreToolAdapter) schema() json.RawMessage { return c.t.Schema() }
func (c *coreToolAdapter) run(ctx context.Context, _ string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	res, err := c.t.Run(ctx, argsJSON)
	if err != nil {
		return provider.ToolResult{}, err
	}
	return provider.ToolResult{Content: res.Content, IsError: res.IsError}, nil
}

// chatToolRegistry holds the active toolset for a single Prompt
// turn. Constructed per-call so tool implementations can capture
// references to the SessionService receiver (which holds repo,
// budgets, etc.) at construction.
type chatToolRegistry struct {
	tools []chatTool
}

func (r *chatToolRegistry) defs() []provider.ToolDef {
	out := make([]provider.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, provider.ToolDef{
			Name:        t.name(),
			Description: t.description(),
			Schema:      t.schema(),
		})
	}
	return out
}

func (r *chatToolRegistry) lookup(name string) (chatTool, bool) {
	for _, t := range r.tools {
		if t.name() == name {
			return t, true
		}
	}
	return nil, false
}

// filterByName narrows the registry to tools whose name appears in
// allow. Empty allow returns the registry unchanged (full toolset).
// Unknown names in allow are silently dropped — we don't fail the
// turn just because an agent profile referenced a tool that's no
// longer registered. The Agent struct's Tools list is the contract;
// this is the enforcement point.
func (r *chatToolRegistry) filterByName(allow []string) *chatToolRegistry {
	if len(allow) == 0 {
		return r
	}
	allowSet := make(map[string]struct{}, len(allow))
	for _, n := range allow {
		allowSet[n] = struct{}{}
	}
	out := make([]chatTool, 0, len(r.tools))
	for _, t := range r.tools {
		if _, ok := allowSet[t.name()]; ok {
			out = append(out, t)
		}
	}
	return &chatToolRegistry{tools: out}
}

// buildChatToolRegistry assembles the V1 tool registry for a Prompt
// call. Returns the FULL registry; agent profiles' tool whitelists are
// applied separately via filterByName so we have one canonical list of
// what the daemon supports.
func (s *SessionService) buildChatToolRegistry(runSvc *RunService, parentProvider, parentModel string) *chatToolRegistry {
	return &chatToolRegistry{
		tools: []chatTool{
			// Read-only meta tools (V1 baseline).
			&toolShowDiff{rs: runSvc, tracker: s.diffTracker},
			&toolShowSpec{sess: s, base: s.sessionsBase},
			&toolShowStatus{sess: s},
			&toolListSessions{repo: s.repo},
			&toolRequestCompact{rs: runSvc},
			// Write/exec tools — gives the agent actual coding ability.
			// Each is scoped to the session's working_dir and capped on
			// output / runtime; see agent_tools_write.go for the limits.
			//
			// The write/edit tools share s.diffTracker so show_diff can
			// drain the turn's deltas without re-reading the FS.
			// run_bash flips the tracker's polluted flag — its output
			// isn't parsed back, but show_diff appends a caveat so the
			// agent knows the tracker may be incomplete.
			&toolReadFile{repo: s.repo},
			&toolWriteFile{repo: s.repo, tracker: s.diffTracker},
			&toolRunBash{repo: s.repo, tracker: s.diffTracker},
			&toolGrep{repo: s.repo},
			&toolGlob{repo: s.repo},
			// High-value extras (M4) — see agent_tools_extra.go.
			&toolEditFile{repo: s.repo, tracker: s.diffTracker},
			&toolTodoWrite{},
			&toolWebFetch{},
			// Multi-hunk atomic patch (M5.2) — see apply_patch.go.
			&toolApplyPatch{repo: s.repo, tracker: s.diffTracker},
			// Verify-loop discipline (M5.3) — see agent_tools_plan_verify.go.
			// plan_steps declares the work + its acceptance commands;
			// verify runs those commands and transitions step state.
			// The system enforces "no verified status without a verify
			// pass" — discipline as a state machine, not a prompt.
			&toolPlanSteps{},
			&toolVerify{repo: s.repo, tracker: s.diffTracker},
			// Session lifecycle (G1) — see agent_tools_lifecycle.go.
			// freeze_spec is the FrozenSpec producer that was deleted
			// with InterviewService in M3; without it the system_prompt's
			// spec slot stays empty.
			&toolFreezeSpec{sess: s, base: s.sessionsBase},
			&toolStartRun{rs: runSvc, parentProvider: parentProvider, parentModel: parentModel},
			&toolApplyDiff{rs: runSvc, tracker: s.diffTracker},
			// Subagent delegation (G5) — see agent_tools_subagent.go.
			// spawn_agent creates a child session with a sliced spec
			// and detached run; wait_agent blocks until terminal;
			// agent_status peeks without blocking.
			&toolSpawnAgent{
				sess: s, rs: runSvc, registry: s.subagentRegistry, base: s.sessionsBase,
				parentProvider: parentProvider, parentModel: parentModel,
			},
			&toolWaitAgent{sess: s},
			&toolAgentStatus{sess: s},
			// §2.6 verb-tool wave — see agent_tools_verbs.go.
			// Folds the chat REPL's former slash commands (/add,
			// /drop, /ls, /interrupt, /compact, /undo, /save, /clear,
			// /instructions) into agent-callable tools. The chat
			// surface is 100% natural language — no client-side
			// slash dispatch survives.
			&toolAddToWorkingSet{sess: s},
			&toolDropFromWorkingSet{sess: s},
			&toolListWorkingSet{sess: s},
			&toolStopRun{rs: runSvc},
			&toolListCheckpoints{repo: s.repo, base: s.sessionsBase},
			&toolRestoreCheckpoint{rs: runSvc},
			&toolShowInstructions{sess: s},
			&toolExportSession{sess: s},
			&toolResetSession{sess: s},
			// P55 cross-session memory bank — see agent_tools_memory.go.
			// Lazy-wires the DB from the repo at registration time so
			// test setups without a repo silently degrade (the tool
			// returns "noted (no durable storage wired)").
			&toolRemember{db: rememberDB(s)},
		},
	}
}

// rememberDB extracts the *sql.DB for the toolRemember wiring. Wrapped
// in a tiny helper so test code can pass a SessionService with nil
// repo without panicking.
func rememberDB(s *SessionService) *sql.DB {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.DB()
}

// runRegistryRunService fetches the daemon's RunService instance.
// SessionService doesn't store a *RunService directly; the Prompt
// handler reaches for it via the budgets BudgetGetter (the live
// instance is the same struct). Returns nil when budgets isn't a
// *RunService — tools that need run-side state then return a clear
// "not available" error to the LLM.
func (s *SessionService) runService() *RunService {
	if rs, ok := s.budgets.(*RunService); ok {
		return rs
	}
	return nil
}

// --- show_diff -------------------------------------------------------

type toolShowDiff struct {
	rs      *RunService
	tracker *turnDiffTracker
}

func (t *toolShowDiff) name() string { return "show_diff" }

func (t *toolShowDiff) description() string {
	return "Show what files have changed during this turn. " +
		"For chat sessions this is the in-memory turn-scoped diff (write_file/edit_file/apply_patch). " +
		"For run sessions it falls back to the unified diff vs the latest shadow-git checkpoint. " +
		"Empty output means nothing has changed yet."
}

func (t *toolShowDiff) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolShowDiff) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	// Prefer the turn-scoped tracker — it shows exactly what THIS chat
	// turn's edits have done, with no I/O on the read side. Falls back
	// to the shadow-git diff (for run sessions) when the tracker is
	// empty AND not polluted by run_bash, so we don't return a bare
	// "no changes" when there genuinely was a checkpoint diff.
	if t.tracker != nil {
		files, polluted := t.tracker.snapshot(sessionID)
		if len(files) > 0 || polluted {
			body, fileCount, added, removed := renderTrackerSummary(files, polluted)
			summary := fmt.Sprintf("%d files changed, +%d/-%d (turn-scoped)\n",
				fileCount, added, removed)
			return provider.ToolResult{Content: summary + body}, nil
		}
	}
	if t.rs == nil {
		return provider.ToolResult{Content: "no changes this turn"}, nil
	}
	resp, err := t.rs.Diff(ctx, &gilv1.DiffRequest{SessionId: sessionID})
	if err != nil {
		return provider.ToolResult{Content: "diff failed: " + err.Error(), IsError: true}, nil
	}
	if resp.GetUnifiedDiff() == "" {
		note := resp.GetNote()
		if note == "" {
			note = "no changes this turn"
		}
		return provider.ToolResult{Content: note}, nil
	}
	body := resp.GetUnifiedDiff()
	if resp.GetTruncated() {
		body += fmt.Sprintf("\n... (%d bytes truncated)", resp.GetTruncatedBytes())
	}
	summary := fmt.Sprintf("%d files changed, +%d/-%d lines (since last checkpoint)\n",
		resp.GetFilesChanged(), resp.GetLinesAdded(), resp.GetLinesRemoved())
	return provider.ToolResult{Content: summary + body}, nil
}

// --- show_spec -------------------------------------------------------

type toolShowSpec struct {
	sess *SessionService
	base string // sessionsBase — read directly via specstore for V1
}

func (t *toolShowSpec) name() string { return "show_spec" }

func (t *toolShowSpec) description() string {
	return "Read the frozen spec for the current session. " +
		"Returns 'no spec frozen yet' when freeze_spec hasn't been called. " +
		"Use when the user asks to see the spec, the plan, the brief, or what was agreed."
}

func (t *toolShowSpec) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolShowSpec) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	if t.base == "" {
		return provider.ToolResult{Content: "spec unavailable: daemon has no on-disk session store", IsError: true}, nil
	}
	dir := t.base + "/" + sessionID
	store := specstore.NewStore(dir)
	fs, err := store.Load()
	if err != nil || fs == nil {
		return provider.ToolResult{Content: "no spec frozen yet for this session"}, nil
	}
	bs, _ := json.MarshalIndent(fs, "", "  ")
	return provider.ToolResult{Content: string(bs)}, nil
}

// --- show_status -----------------------------------------------------

type toolShowStatus struct {
	sess *SessionService
}

func (t *toolShowStatus) name() string { return "show_status" }

func (t *toolShowStatus) description() string {
	return "Show terse status for the current session: phase, iteration, cost. " +
		"Use when the user asks 'how's it going', 'what's the status', '진행 상황 어때'."
}

func (t *toolShowStatus) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolShowStatus) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	sess, err := t.sess.repo.Get(ctx, sessionID)
	if err != nil {
		return provider.ToolResult{Content: "session lookup failed: " + err.Error(), IsError: true}, nil
	}
	parts := []string{
		"id=" + sess.ID,
		"status=" + sess.Status,
	}
	if sess.GoalHint != "" {
		parts = append(parts, "goal="+sess.GoalHint)
	}
	if t.sess.progress != nil {
		if iters, tokens, ok := t.sess.progress.Progress(sessionID); ok {
			parts = append(parts, fmt.Sprintf("iter=%d tokens=%d", iters, tokens))
		}
	}
	if t.sess.budgets != nil {
		if cost, exceeded, _, ok := t.sess.budgets.Budget(sessionID); ok {
			parts = append(parts, fmt.Sprintf("cost=$%.4f", cost))
			if exceeded {
				parts = append(parts, "BUDGET_EXCEEDED")
			}
		}
	}
	return provider.ToolResult{Content: strings.Join(parts, " · ")}, nil
}

// --- list_sessions ---------------------------------------------------

type toolListSessions struct {
	repo *session.Repo
}

func (t *toolListSessions) name() string { return "list_sessions" }

func (t *toolListSessions) description() string {
	return "List recent sessions (newest first, up to 10). " +
		"Use when the user asks what they were working on, recent tasks, or to find a session by description."
}

func (t *toolListSessions) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolListSessions) run(ctx context.Context, _ string, _ json.RawMessage) (provider.ToolResult, error) {
	all, err := t.repo.List(ctx, session.ListOptions{Limit: 10})
	if err != nil {
		return provider.ToolResult{Content: "list failed: " + err.Error(), IsError: true}, nil
	}
	if len(all) == 0 {
		return provider.ToolResult{Content: "no sessions yet"}, nil
	}
	var b strings.Builder
	for i, sess := range all {
		hint := sess.GoalHint
		if hint == "" {
			hint = "(no description)"
		}
		// iter151a: surface persisted token / cost totals (iter133c
		// writes these on run completion) so list_sessions is useful
		// for spotting expensive vs cheap past runs without needing
		// show_status on each one. Skip when both are zero so fresh
		// or never-ran sessions stay terse.
		extra := ""
		if sess.TotalTokens > 0 || sess.TotalCostUSD > 0 {
			extra = fmt.Sprintf(" · tokens=%d cost=$%.4f",
				sess.TotalTokens, sess.TotalCostUSD)
		}
		fmt.Fprintf(&b, "%d. %s · %s · %s%s\n", i+1, sess.ID[:10], sess.Status, hint, extra)
	}
	return provider.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- request_compact -------------------------------------------------

type toolRequestCompact struct {
	rs *RunService
}

func (t *toolRequestCompact) name() string { return "request_compact" }

func (t *toolRequestCompact) description() string {
	return "Ask the runner to compact the conversation history at the next turn boundary. " +
		"Only takes effect when a run is in flight. Use when the user mentions context being long, " +
		"asking to compact, summarise the conversation, or free up tokens."
}

func (t *toolRequestCompact) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolRequestCompact) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	if t.rs == nil {
		return provider.ToolResult{Content: "compact unavailable: no run service", IsError: true}, nil
	}
	resp, err := t.rs.RequestCompact(ctx, &gilv1.RequestCompactRequest{SessionId: sessionID})
	if err != nil {
		return provider.ToolResult{Content: "compact failed: " + err.Error(), IsError: true}, nil
	}
	if !resp.GetQueued() {
		reason := resp.GetReason()
		if reason == "" {
			reason = "no run in flight"
		}
		return provider.ToolResult{Content: "compaction not queued: " + reason}, nil
	}
	return provider.ToolResult{Content: "compaction queued for the next turn boundary"}, nil
}

// errToolNotFound is returned when the LLM calls a tool name the
// registry doesn't know. The Prompt handler maps this to a tool
// result with IsError=true so the LLM can self-correct.
var errToolNotFound = errors.New("tool not registered")
