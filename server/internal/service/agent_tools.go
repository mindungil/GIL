package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// agent_tools.go is the V1 tool registry for SessionService.Prompt's
// agent loop (docs/design/chat-architecture.md §2.3). Each tool is a
// thin wrapper around an existing service call. The LLM picks tools
// to invoke based on the user's natural-language input — there is no
// client-side dispatch.
//
// V1 ships read-only tools plus request_compact. Destructive write
// tools (freeze_spec, start_run, apply_diff) are deferred to a follow-
// up commit because they need careful design (spec-build state shape,
// merge semantics).

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

// buildChatToolRegistry assembles the V1 tool registry for a Prompt
// call. The agent has access to:
//   - show_diff: read the unified diff vs the latest checkpoint
//   - show_spec: read the frozen spec JSON
//   - show_status: terse session metadata (status, iter, cost)
//   - list_sessions: enumerate recent sessions
//   - request_compact: ask the runner to compact at the next boundary
//
// Destructive tools (freeze_spec, start_run, apply_diff) are not yet
// registered; the agent will tell the user it can't perform those
// actions yet when asked.
func (s *SessionService) buildChatToolRegistry(runSvc *RunService) *chatToolRegistry {
	return &chatToolRegistry{
		tools: []chatTool{
			// Read-only meta tools (V1 baseline).
			&toolShowDiff{rs: runSvc},
			&toolShowSpec{sess: s, base: s.sessionsBase},
			&toolShowStatus{sess: s},
			&toolListSessions{repo: s.repo},
			&toolRequestCompact{rs: runSvc},
			// Write/exec tools — gives the agent actual coding ability.
			// Each is scoped to the session's working_dir and capped on
			// output / runtime; see agent_tools_write.go for the limits.
			&toolReadFile{repo: s.repo},
			&toolWriteFile{repo: s.repo},
			&toolRunBash{repo: s.repo},
			&toolGrep{repo: s.repo},
			&toolGlob{repo: s.repo},
		},
	}
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
	rs *RunService
}

func (t *toolShowDiff) name() string { return "show_diff" }

func (t *toolShowDiff) description() string {
	return "Show the unified diff between the latest checkpoint and the current workspace. " +
		"Use when the user asks to see what's changed, the diff, or recent edits. " +
		"Empty output means no checkpoints yet."
}

func (t *toolShowDiff) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *toolShowDiff) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	if t.rs == nil {
		return provider.ToolResult{Content: "diff unavailable: daemon has no run service wired", IsError: true}, nil
	}
	resp, err := t.rs.Diff(ctx, &gilv1.DiffRequest{SessionId: sessionID})
	if err != nil {
		return provider.ToolResult{Content: "diff failed: " + err.Error(), IsError: true}, nil
	}
	if resp.GetUnifiedDiff() == "" {
		note := resp.GetNote()
		if note == "" {
			note = "no changes since last checkpoint"
		}
		return provider.ToolResult{Content: note}, nil
	}
	body := resp.GetUnifiedDiff()
	if resp.GetTruncated() {
		body += fmt.Sprintf("\n... (%d bytes truncated)", resp.GetTruncatedBytes())
	}
	summary := fmt.Sprintf("%d files changed, +%d/-%d lines\n",
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
		"Returns 'no spec frozen yet' when the session hasn't completed an interview. " +
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
		fmt.Fprintf(&b, "%d. %s · %s · %s\n", i+1, sess.ID[:10], sess.Status, hint)
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
