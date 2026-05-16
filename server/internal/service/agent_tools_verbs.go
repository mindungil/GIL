package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// agent_tools_verbs.go — §2.6 verb-tool wave. These tools fold the
// surface verbs the chat REPL used to dispatch as slash commands
// (/add /drop /ls /interrupt /undo /checkpoints /instructions /save
// /clear) into agent-callable tools. Per the chat-architecture spec
// the chat surface is 100% natural language: the user says "add
// these files" or "clear our conversation" in prose, the LLM picks
// the right tool, the daemon executes it. No client-side slash
// dispatch survives.
//
// Working-set state is per-session and lives on the SessionService
// receiver. P30 added durable backing via the workingset_entries
// table (schema v4): the in-memory bag writes through on add/drop and
// re-hydrates on first access after a daemon restart, so the user's
// curated context survives across runs.

// --- working set --------------------------------------------------

// workingSet holds the user-curated file paths the chat agent should
// consider in-scope for the current session. Per-session map keyed
// by session ID; entries are deduplicated and sorted on read so
// list_workingset is stable. Modifications hold the mutex.
type workingSet struct {
	mu      sync.Mutex
	entries map[string]map[string]struct{}
	// db is the optional durable backing. When nil the store behaves
	// as the pre-P30 in-memory version — tests that don't care about
	// persistence keep working untouched.
	db *sql.DB
	// loaded tracks which session IDs have been hydrated from DB so
	// add/drop/list skip the SELECT after the first hit. Presence-only.
	loaded map[string]struct{}
}

func newWorkingSet() *workingSet {
	return &workingSet{
		entries: map[string]map[string]struct{}{},
		loaded:  map[string]struct{}{},
	}
}

// SetDB attaches a *sql.DB to the store. Pass nil to detach (tests).
// Safe to call multiple times.
func (w *workingSet) SetDB(db *sql.DB) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.db = db
	// Reset loaded set so the next access rehydrates against the
	// new backing.
	w.loaded = map[string]struct{}{}
}

func (w *workingSet) add(sid string, paths []string) (added, alreadyPresent []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensureLoadedLocked(sid)
	bag, ok := w.entries[sid]
	if !ok {
		bag = map[string]struct{}{}
		w.entries[sid] = bag
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, exists := bag[p]; exists {
			alreadyPresent = append(alreadyPresent, p)
			continue
		}
		bag[p] = struct{}{}
		added = append(added, p)
	}
	if len(added) > 0 {
		w.persistAddLocked(sid, added)
	}
	return added, alreadyPresent
}

func (w *workingSet) drop(sid string, paths []string) (dropped, notPresent []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensureLoadedLocked(sid)
	bag := w.entries[sid]
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := bag[p]; ok {
			delete(bag, p)
			dropped = append(dropped, p)
		} else {
			notPresent = append(notPresent, p)
		}
	}
	if len(dropped) > 0 {
		w.persistDropLocked(sid, dropped)
	}
	return dropped, notPresent
}

func (w *workingSet) list(sid string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensureLoadedLocked(sid)
	bag := w.entries[sid]
	out := make([]string, 0, len(bag))
	for p := range bag {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ensureLoadedLocked hydrates w.entries[sid] from DB on first hit
// after a SetDB call. Caller holds w.mu. When db is nil this is a
// no-op.
func (w *workingSet) ensureLoadedLocked(sid string) {
	if w.loaded == nil {
		w.loaded = map[string]struct{}{}
	}
	if w.db == nil {
		return
	}
	if _, done := w.loaded[sid]; done {
		return
	}
	rows, err := w.db.Query(`SELECT path FROM workingset_entries
        WHERE session_id = ? ORDER BY path ASC`, sid)
	if err != nil {
		// Silent failure — pre-restart state is unrecoverable for
		// this session, but the in-memory store stays consistent.
		return
	}
	defer rows.Close()
	bag, ok := w.entries[sid]
	if !ok {
		bag = map[string]struct{}{}
		w.entries[sid] = bag
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return
		}
		bag[p] = struct{}{}
	}
	w.loaded[sid] = struct{}{}
}

// persistAddLocked inserts the new paths. Caller holds w.mu. Failures
// are silent — durability is best-effort and the in-memory store
// remains authoritative within the daemon's lifetime. Uses INSERT OR
// IGNORE so a stale duplicate row can't fail the whole batch.
func (w *workingSet) persistAddLocked(sid string, paths []string) {
	if w.db == nil {
		return
	}
	tx, err := w.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range paths {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO workingset_entries
            (session_id, path) VALUES (?, ?)`, sid, p); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// persistDropLocked deletes the given paths for sid. Caller holds w.mu.
// Silent failure (same rationale as persistAddLocked).
func (w *workingSet) persistDropLocked(sid string, paths []string) {
	if w.db == nil {
		return
	}
	tx, err := w.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range paths {
		if _, err := tx.Exec(`DELETE FROM workingset_entries
            WHERE session_id = ? AND path = ?`, sid, p); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// chatWorkingSet returns the per-service working-set store, allocating
// on first access so existing constructors (NewSessionService) keep
// compiling without churn. When the SessionService has a Repo, the
// store is auto-wired with the underlying *sql.DB on first allocation
// so add/drop/list survive a daemon restart (P30).
func (s *SessionService) chatWorkingSet() *workingSet {
	s.workingSetMu.Lock()
	defer s.workingSetMu.Unlock()
	if s.workingSet == nil {
		s.workingSet = newWorkingSet()
		if s.repo != nil {
			s.workingSet.SetDB(s.repo.DB())
		}
	}
	return s.workingSet
}

// --- tools --------------------------------------------------------

type toolAddToWorkingSet struct{ sess *SessionService }

func (t *toolAddToWorkingSet) name() string { return "add_to_workingset" }
func (t *toolAddToWorkingSet) description() string {
	return "Add file paths to the current session's working set. " +
		"Use when the user explicitly names files to focus on, share, " +
		"or asks you to look at specific files. Accepts an array of paths."
}
func (t *toolAddToWorkingSet) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"]}`)
}
func (t *toolAddToWorkingSet) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if len(args.Paths) == 0 {
		return provider.ToolResult{Content: "no paths supplied"}, nil
	}
	// iter56a: workingset is scope-bound to the session's working dir,
	// matching read_file / write_file / edit_file behavior. Without
	// this gate, the agent (or a prompt-injection chain) could pollute
	// the workingset DB with absolute paths or `../` escapes — the
	// list itself is harmless, but future tools that READ from the
	// workingset would inherit the escape.
	wd, err := sessionWD(ctx, t.sess.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	cleaned := make([]string, 0, len(args.Paths))
	rejected := make([]string, 0)
	for _, p := range args.Paths {
		if _, err := resolveInWD(wd, p); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s (%s)", p, err.Error()))
			continue
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return provider.ToolResult{
			Content: "no paths added; all rejected:\n  " + strings.Join(rejected, "\n  "),
			IsError: true,
		}, nil
	}
	added, dup := t.sess.chatWorkingSet().add(sessionID, cleaned)
	out := fmt.Sprintf("added %d file(s) to workingset", len(added))
	if len(added) > 0 {
		out += ":\n  " + strings.Join(added, "\n  ")
	}
	if len(dup) > 0 {
		out += fmt.Sprintf("\nalready present (%d): %s", len(dup), strings.Join(dup, ", "))
	}
	if len(rejected) > 0 {
		out += fmt.Sprintf("\nrejected (%d):\n  %s", len(rejected), strings.Join(rejected, "\n  "))
	}
	return provider.ToolResult{Content: out}, nil
}

type toolDropFromWorkingSet struct{ sess *SessionService }

func (t *toolDropFromWorkingSet) name() string { return "drop_from_workingset" }
func (t *toolDropFromWorkingSet) description() string {
	return "Remove file paths from the working set. Use when the user " +
		"says to forget, drop, ignore, or stop tracking specific files."
}
func (t *toolDropFromWorkingSet) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"]}`)
}
func (t *toolDropFromWorkingSet) run(_ context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if len(args.Paths) == 0 {
		return provider.ToolResult{Content: "no paths supplied"}, nil
	}
	dropped, missing := t.sess.chatWorkingSet().drop(sessionID, args.Paths)
	out := fmt.Sprintf("dropped %d file(s) from workingset", len(dropped))
	if len(dropped) > 0 {
		out += ":\n  " + strings.Join(dropped, "\n  ")
	}
	if len(missing) > 0 {
		out += fmt.Sprintf("\nnot in workingset (%d): %s", len(missing), strings.Join(missing, ", "))
	}
	return provider.ToolResult{Content: out}, nil
}

type toolListWorkingSet struct{ sess *SessionService }

func (t *toolListWorkingSet) name() string { return "list_workingset" }
func (t *toolListWorkingSet) description() string {
	return "List the file paths currently in the session's working set. " +
		"Use when the user asks what files are in scope, what you're focused on."
}
func (t *toolListWorkingSet) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolListWorkingSet) run(_ context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	entries := t.sess.chatWorkingSet().list(sessionID)
	if len(entries) == 0 {
		return provider.ToolResult{Content: "workingset is empty"}, nil
	}
	out := fmt.Sprintf("%d file(s) in workingset:\n  %s",
		len(entries), strings.Join(entries, "\n  "))
	return provider.ToolResult{Content: out}, nil
}

// --- stop_run -----------------------------------------------------

type toolStopRun struct{ rs *RunService }

func (t *toolStopRun) name() string { return "stop_run" }
func (t *toolStopRun) description() string {
	return "Stop the detached run for this session at its next " +
		"cancellation checkpoint. Use when the user asks to stop, " +
		"halt, interrupt, abort, or kill the running agent. No-op " +
		"when no run is in flight."
}
func (t *toolStopRun) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolStopRun) run(_ context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	if t.rs == nil {
		return provider.ToolResult{Content: "stop unavailable: no run service", IsError: true}, nil
	}
	if t.rs.RequestStop(sessionID) {
		return provider.ToolResult{Content: "stop signal sent — the loop will exit at its next cancellation checkpoint"}, nil
	}
	return provider.ToolResult{Content: "no run in flight for this session"}, nil
}

// --- list_checkpoints / restore_checkpoint ------------------------

type toolListCheckpoints struct {
	repo *session.Repo
	base string
}

func (t *toolListCheckpoints) name() string { return "list_checkpoints" }
func (t *toolListCheckpoints) description() string {
	return "List shadow-git checkpoints for this session, newest first. " +
		"Use when the user asks for the history, undo points, or wants " +
		"to roll back. Each entry shows a 1-based step number for use " +
		"with restore_checkpoint."
}
func (t *toolListCheckpoints) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolListCheckpoints) run(ctx context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	sess, err := t.repo.Get(ctx, sessionID)
	if err != nil {
		return provider.ToolResult{Content: "session lookup failed: " + err.Error(), IsError: true}, nil
	}
	workspaceDir := sess.WorkingDir
	specDir := filepath.Join(t.base, sessionID)
	if spec, err := specstore.NewStore(specDir).Load(); err == nil && spec != nil && spec.Workspace != nil && spec.Workspace.Path != "" {
		workspaceDir = spec.Workspace.Path
	}
	shadowDir := filepath.Join(specDir, "shadow")
	// iter31a: shadow git isn't initialized until the first checkpoint
	// is taken (typically when a run starts). If the shadow dir doesn't
	// exist yet, ListCommits returns a raw "fatal: not a git repository"
	// error that both leaks the daemon's internal storage path and
	// surfaces as IsError to the agent. Treat the pre-init state as
	// "no checkpoints" — same response as a freshly-initialized but
	// empty shadow.
	if _, err := os.Stat(filepath.Join(shadowDir, ".git")); os.IsNotExist(err) {
		return provider.ToolResult{Content: "no checkpoints yet for this session"}, nil
	}
	sg := checkpoint.New(workspaceDir, shadowDir)
	commits, err := sg.ListCommits(ctx)
	if err != nil {
		return provider.ToolResult{Content: "list checkpoints failed: " + err.Error(), IsError: true}, nil
	}
	if len(commits) == 0 {
		return provider.ToolResult{Content: "no checkpoints yet for this session"}, nil
	}
	// Newest-first. Step numbering: step=1 → oldest; matches Restore.
	var b strings.Builder
	for i, c := range commits {
		step := len(commits) - i // oldest = 1
		fmt.Fprintf(&b, "%d. %s · %s\n",
			step, c.SHA[:min(len(c.SHA), 8)],
			strings.TrimSpace(c.Message))
	}
	return provider.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

type toolRestoreCheckpoint struct{ rs *RunService }

func (t *toolRestoreCheckpoint) name() string { return "restore_checkpoint" }
func (t *toolRestoreCheckpoint) description() string {
	return "Roll the workspace back to a prior checkpoint. step=1 is the " +
		"oldest checkpoint; step=-1 is the most recent. Refuses while a " +
		"run is active — stop_run first. Use when the user asks to undo, " +
		"revert, restore, or go back to a prior state."
}
func (t *toolRestoreCheckpoint) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"step":{"type":"integer"}},"required":["step"]}`)
}
func (t *toolRestoreCheckpoint) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	if t.rs == nil {
		return provider.ToolResult{Content: "restore unavailable: no run service", IsError: true}, nil
	}
	var args struct {
		Step int32 `json:"step"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	resp, err := t.rs.Restore(ctx, &gilv1.RestoreRequest{SessionId: sessionID, Step: args.Step})
	if err != nil {
		return provider.ToolResult{Content: "restore failed: " + err.Error(), IsError: true}, nil
	}
	return provider.ToolResult{Content: fmt.Sprintf(
		"restored to %s · %q (%d total checkpoints)",
		resp.GetCommitSha()[:min(len(resp.GetCommitSha()), 8)],
		strings.TrimSpace(resp.GetCommitMessage()),
		resp.GetTotalCheckpoints(),
	)}, nil
}

// --- show_instructions -------------------------------------------

type toolShowInstructions struct{ sess *SessionService }

func (t *toolShowInstructions) name() string { return "show_instructions" }
func (t *toolShowInstructions) description() string {
	return "Show the agent's own current operating instructions (the system " +
		"prompt + tool list for this turn). Use when the user asks what " +
		"the agent knows, what its constraints are, or 'who are you'."
}
func (t *toolShowInstructions) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolShowInstructions) run(_ context.Context, _ string, _ json.RawMessage) (provider.ToolResult, error) {
	// V1: emit a stable summary. The full system prompt is rendered
	// per-turn against the live session state by assembleSystemPrompt,
	// which we can't introspect here without taking a dependency on
	// the loop construction. A textual summary is honest about scope.
	body := strings.Join([]string{
		"gil agent — chat surface (M3+).",
		"",
		"You operate via SessionService.Prompt. Every natural-language input from",
		"the user flows into your tool-using agent loop. You decide whether to",
		"call a tool or reply directly.",
		"",
		"Tool families currently registered:",
		"  - read/write/edit/apply_patch — codebase mutation",
		"  - run_bash — shell execution scoped to the session workspace",
		"  - grep / glob — codebase search",
		"  - show_diff / show_spec / show_status — read-only meta",
		"  - plan_steps / verify — verify-loop discipline (M5)",
		"  - freeze_spec / start_run / apply_diff — session lifecycle (G1)",
		"  - spawn_agent / wait_agent / agent_status — subagent delegation (G5)",
		"  - request_compact / stop_run — runner control (§2.6)",
		"  - add_to_workingset / drop_from_workingset / list_workingset — context steering (§2.6)",
		"  - list_checkpoints / restore_checkpoint — workspace rollback (§2.6)",
		"  - export_session / reset_session / show_instructions — session ops (§2.6)",
		"",
		"The user steers via natural language only — slash commands are gone.",
	}, "\n")
	return provider.ToolResult{Content: body}, nil
}

// --- export_session -----------------------------------------------

type toolExportSession struct{ sess *SessionService }

func (t *toolExportSession) name() string { return "export_session" }
func (t *toolExportSession) description() string {
	return "Return a portable transcript of this chat session — turn-by-turn " +
		"user / assistant / tool_call summary. Use when the user asks to " +
		"export, save, share, or copy the conversation."
}
func (t *toolExportSession) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolExportSession) run(_ context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	hist := t.sess.chatHistory().get(sessionID)
	if len(hist) == 0 {
		return provider.ToolResult{Content: "no conversation history yet for this session"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session: %s · exported: %s\n",
		sessionID, time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "turns: %d\n\n", len(hist))
	for i, msg := range hist {
		role := string(msg.Role)
		body := msg.Content
		// Tool calls / results pass through as compact summaries to
		// keep the transcript readable. Full payloads remain in the
		// per-session event store; the user can pull them via the
		// raw events file if needed.
		if len(msg.ToolCalls) > 0 {
			calls := make([]string, 0, len(msg.ToolCalls))
			for _, c := range msg.ToolCalls {
				calls = append(calls, c.Name)
			}
			body += "  [tool_calls: " + strings.Join(calls, ", ") + "]"
		}
		if len(msg.ToolResults) > 0 {
			body += fmt.Sprintf("  [%d tool_results]", len(msg.ToolResults))
		}
		fmt.Fprintf(&b, "%02d %-9s · %s\n", i+1, role, body)
	}
	return provider.ToolResult{Content: b.String()}, nil
}

// --- reset_session ------------------------------------------------

type toolResetSession struct{ sess *SessionService }

func (t *toolResetSession) name() string { return "reset_session" }
func (t *toolResetSession) description() string {
	return "Clear this session's conversation history so the next prompt " +
		"starts fresh. Does NOT touch the workspace, frozen spec, or " +
		"checkpoints. Use when the user asks to clear, reset, start over, " +
		"or wipe the chat. Confirm intent before calling — this cannot be " +
		"undone within the daemon process."
}
func (t *toolResetSession) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *toolResetSession) run(_ context.Context, sessionID string, _ json.RawMessage) (provider.ToolResult, error) {
	t.sess.chatHistory().reset(sessionID)
	return provider.ToolResult{Content: "conversation history cleared for this session"}, nil
}
