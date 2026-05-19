package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/cost"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	"github.com/mindungil/gil/core/stuck"
	"github.com/mindungil/gil/core/workspace"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// session_prompt.go owns SessionService.Prompt — the single chat-surface
// entry point (docs/design/chat-architecture.md). Every natural-language
// input from cli REPL or TUI flows through here, the daemon runs an
// agent loop with the chat tool registry, and Parts stream back: TextDelta
// chunks, ToolCallPart / ToolResultPart pairs, SessionAllocatedPart on
// the first auto-create call, PromptMetrics snapshots, ReasoningDelta
// when the upstream emits it (P33), and DonePart.
//
// What lives here:
//   - chatHistory: per-session provider.Message log. P34 made it durable
//     via the chat_messages SQLite table (see chatHistory.SetDB and
//     ensureLoadedLocked below). Write-through on every append; hydrates
//     on first access after a daemon restart.
//   - defaultChatSystemPrompt: the base system prompt sent to the chat
//     agent. Lists tool families and workflow guidance.
//   - redact{KnownSecrets,InlineSecretShapes}: secret-scrubbing applied
//     to user text before it reaches the LLM (iter36a/93a/156).
//   - Prompt RPC implementation itself, further down.

// chatHistory holds the running message log per session for the chat
// agent loop. Keyed by session ID. P34 added optional durable backing
// via the chat_messages table — when SetDB has been called, append
// writes through and get hydrates on first access after a daemon
// restart. When db is nil the store behaves as the pre-P34 in-memory
// version (existing tests that don't wire a Repo keep working
// untouched).
type chatHistory struct {
	mu  sync.Mutex
	all map[string][]provider.Message
	// db is the optional durable backing (P34). When nil, append/get/reset
	// behave as the pre-P34 in-memory-only chatHistory. Set via SetDB.
	db *sql.DB
	// loaded tracks which session IDs have been hydrated from DB so the
	// next access skips the SELECT. Presence-only set; same pattern as
	// workingSet (P30) — see agent_tools_verbs.go.
	loaded map[string]struct{}
}

func newChatHistory() *chatHistory {
	return &chatHistory{
		all:    make(map[string][]provider.Message),
		loaded: make(map[string]struct{}),
	}
}

// SetDB attaches a *sql.DB to the store. Pass nil to detach (tests).
// Safe to call multiple times; the loaded set is reset so the next
// access rehydrates against the new backing.
func (h *chatHistory) SetDB(db *sql.DB) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.db = db
	h.loaded = make(map[string]struct{})
}

func (h *chatHistory) get(sid string) []provider.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureLoadedLocked(sid)
	src := h.all[sid]
	out := make([]provider.Message, len(src))
	copy(out, src)
	return out
}

func (h *chatHistory) append(sid string, msg provider.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureLoadedLocked(sid)
	seq := h.nextSeqLocked(sid)
	h.all[sid] = append(h.all[sid], msg)
	h.persistAppendLocked(sid, seq, msg)
}

// nextSeqLocked computes the seq the next append should use. With a
// DB wired, it reads `MAX(seq) + 1` so the value is correct even when
// the in-memory list has been shrunk by ReplaceInMemory (P35 chat
// compaction). Without a DB, falls back to len — pre-P34 contract.
// Caller holds h.mu.
func (h *chatHistory) nextSeqLocked(sid string) int {
	if h.db == nil {
		return len(h.all[sid])
	}
	var maxSeq sql.NullInt64
	if err := h.db.QueryRow(`SELECT MAX(seq) FROM chat_messages WHERE session_id = ?`, sid).Scan(&maxSeq); err != nil {
		// Silent fallback — same rationale as persistAppendLocked. Use
		// in-memory len so an append still lands at a sensible seq even
		// when the DB query failed.
		return len(h.all[sid])
	}
	if !maxSeq.Valid {
		return 0
	}
	return int(maxSeq.Int64) + 1
}

// ReplaceInMemory swaps the in-memory slice for sid without touching
// DB rows. Used by P35 chat-history compaction: the compactor returns
// a shorter, summary-prefixed message slice that becomes the chat
// agent's working context, while the chat_messages table retains the
// full log (export_session, post-restart re-hydration). The loaded
// flag stays set so the next get() returns the compacted slice
// instead of re-loading the full log from DB.
func (h *chatHistory) ReplaceInMemory(sid string, msgs []provider.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]provider.Message, len(msgs))
	copy(cp, msgs)
	h.all[sid] = cp
	if h.loaded == nil {
		h.loaded = make(map[string]struct{})
	}
	h.loaded[sid] = struct{}{}
}

// reset clears the message log for sid so a subsequent Prompt starts
// the agent loop with no prior context. Used by the reset_session
// verb tool (§2.6) when the user asks to "start over" or "clear chat".
// P34: also wipes the per-session chat_messages rows so a restart
// after reset doesn't resurrect the cleared history.
func (h *chatHistory) reset(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.all, sid)
	delete(h.loaded, sid)
	h.persistResetLocked(sid)
}

// ensureLoadedLocked hydrates h.all[sid] from DB on first hit after a
// SetDB call. Caller holds h.mu. No-op when db is nil. Hydration
// preserves seq order via ORDER BY seq ASC — the table PK guarantees
// uniqueness so the loaded slice ends up byte-identical to what append
// wrote turn-by-turn. Silent failure: pre-restart state is
// unrecoverable for this session but the in-memory store stays
// consistent.
func (h *chatHistory) ensureLoadedLocked(sid string) {
	if h.loaded == nil {
		h.loaded = make(map[string]struct{})
	}
	if h.db == nil {
		return
	}
	if _, done := h.loaded[sid]; done {
		return
	}
	rows, err := h.db.Query(`SELECT role, content, tool_calls, tool_results
        FROM chat_messages WHERE session_id = ? ORDER BY seq ASC`, sid)
	if err != nil {
		return
	}
	defer rows.Close()
	var hydrated []provider.Message
	for rows.Next() {
		var role, content, toolCallsJSON, toolResultsJSON string
		if err := rows.Scan(&role, &content, &toolCallsJSON, &toolResultsJSON); err != nil {
			return
		}
		msg := provider.Message{
			Role:    provider.Role(role),
			Content: content,
		}
		if toolCallsJSON != "" {
			var calls []provider.ToolCall
			if err := json.Unmarshal([]byte(toolCallsJSON), &calls); err == nil {
				msg.ToolCalls = calls
			}
		}
		if toolResultsJSON != "" {
			var results []provider.ToolResult
			if err := json.Unmarshal([]byte(toolResultsJSON), &results); err == nil {
				msg.ToolResults = results
			}
		}
		hydrated = append(hydrated, msg)
	}
	if len(hydrated) > 0 {
		h.all[sid] = hydrated
	}
	h.loaded[sid] = struct{}{}
}

// persistAppendLocked writes a single message row. Caller holds h.mu.
// Silent failure: durability is best-effort and the in-memory store
// remains authoritative within the daemon's lifetime. tool_calls /
// tool_results columns are JSON-encoded slices; nil/empty slices
// serialize to "" so the columns stay readable for sessions that
// never call tools.
func (h *chatHistory) persistAppendLocked(sid string, seq int, msg provider.Message) {
	if h.db == nil {
		return
	}
	var toolCallsJSON, toolResultsJSON string
	if len(msg.ToolCalls) > 0 {
		if b, err := json.Marshal(msg.ToolCalls); err == nil {
			toolCallsJSON = string(b)
		}
	}
	if len(msg.ToolResults) > 0 {
		if b, err := json.Marshal(msg.ToolResults); err == nil {
			toolResultsJSON = string(b)
		}
	}
	_, _ = h.db.Exec(`INSERT INTO chat_messages
        (session_id, seq, role, content, tool_calls, tool_results)
        VALUES (?, ?, ?, ?, ?, ?)`,
		sid, seq, string(msg.Role), msg.Content, toolCallsJSON, toolResultsJSON)
}

// persistResetLocked deletes every chat_messages row for sid. Caller
// holds h.mu. Silent failure (same rationale as persistAppendLocked).
func (h *chatHistory) persistResetLocked(sid string) {
	if h.db == nil {
		return
	}
	_, _ = h.db.Exec(`DELETE FROM chat_messages WHERE session_id = ?`, sid)
}

// WithProviderFactory wires the same ProviderFactory used by Run /
// InterviewService so SessionService.Prompt can resolve a provider
// for the agent loop. Chaining-style for symmetry with WithBudgetGetter.
func (s *SessionService) WithProviderFactory(f ProviderFactory) *SessionService {
	s.providerFactory = f
	return s
}

// defaultChatSystemPrompt is the V1 system prompt for the "default"
// agent. It frames gil as an autonomous coding harness assistant
// without enumerating slash commands (there are none — see
// docs/design/chat-architecture.md §1).
//
// The prompt instructs the agent to call tools (show_diff /
// show_spec / show_status / list_sessions / request_compact) when
// the user asks about workspace state, rather than describing what
// it would do.
const defaultChatSystemPrompt = `You are gil, an autonomous coding harness assistant.

The user types in natural language. There are no slash commands.
Respond conversationally and call tools when they map to what the
user is asking for — don't describe what you would do, just do it.

You can actually read, edit, and execute code in the user's working
directory. When the user describes a coding task, do the work — don't
just talk about it. Use grep / glob to find relevant files, read_file
to inspect them, write_file to make edits, and run_bash to compile,
test, and verify.

Tools — workspace state (read-only):
- show_diff: see changes vs the last checkpoint.
- show_spec: see the frozen spec, if any.
- show_status: terse session status (phase, iter, cost).
- list_sessions: recent sessions (use to recall past work).
- request_compact: ask the runner to compact context next turn.

Tools — code I/O (scoped to the session working dir):
- read_file: read a file's contents.
- write_file: overwrite/create a file (atomic). Use for new files or
  full rewrites.
- edit_file: replace an exact text snippet in a file. Prefer this over
  write_file for small edits to large files — it's cheaper on tokens.
- apply_patch: apply a multi-file, multi-hunk patch atomically. Format:
  '*** Begin Patch' / '*** End Patch' envelope with '*** Add File: <p>',
  '*** Delete File: <p>', or '*** Update File: <p>' (followed by '@@'
  hunks of space/-/+ lines). All hunks must match exactly once or NO
  file is touched. Prefer over edit_file when you have multiple edits
  in the same call — saves round-trips and keeps changes coherent.
- run_bash: run a shell command (default 30s, max 60s).
- grep: regex search across the tree (uses ripgrep when present).
- glob: list files matching a pattern (** supported for recursion).
- todowrite: persist a session todo list (statuses pending /
  in_progress / completed). Use when a task has 3+ steps and no
  verification gate. For verification-gated work prefer plan_steps.
- plan_steps: declare a verification-gated plan. Each step has a
  description and an acceptance_check command. Statuses (pending /
  verified / failed) are SYSTEM-MANAGED — only the verify tool can
  transition a step to verified or failed by running its
  acceptance_check.
- verify: run a verification command. When called with step_id, it
  transitions the matching plan_step on success/failure. After every
  code-changing tool call (write_file, edit_file, apply_patch) you
  MUST run verify before progressing or declaring the work done.
  verify commands must exercise behavior, not just inspect state.
  Prefer build, test, lint, type-check, or assertion scripts. Standalone
  cat / ls / echo / pwd are not valid verify checks — chain them to a
  real check (e.g. cat foo.go && go build) if you must inspect first.
- webfetch: GET an http(s) URL, capped at 256 KB / 15s. Use for docs,
  issue links, public web content.

Tools — session lifecycle (call when the user wants autonomous run):
- freeze_spec: persist a frozen spec (goal + optional
  constraints/verification/budget/autonomy) onto the session. Required
  before start_run. Call ONCE per session; a frozen spec is immutable.
  Pass only the slots you've extracted from conversation —
  goal.one_liner is the only hard requirement, everything else is
  optional. Don't ask the user to re-state things you already know.
- start_run: kick the autonomous run loop on a frozen spec. Detached;
  use show_status / list_sessions to observe progress. Refuses
  unfrozen sessions.
- apply_diff: confirm what the agent's edits landed this turn. In chat
  mode edits write directly to the working directory; this is for the
  "apply it / 적용해" moment after show_diff.

Tools — subagent delegation (call to split work in parallel):
- spawn_agent: create a child agent running on a sliced copy of this
  session's frozen spec. Pass a short label (lowercase) and a task
  string the child receives as its first user message. Optional
  agent_type (default / explore / plan) and spec_override (narrows
  workspace / tools / max_iterations). Subject to V1 caps: max 8
  active children per root, depth 2 (children CAN spawn one
  further). Returns the child's agent_id + label.
- wait_agent: block until a spawned child reaches terminal state
  (done / failed / stopped / budget_exceeded). Identify by agent_id
  (from spawn_agent) OR label. Default 600s timeout.
- agent_status: non-blocking list of this session's children with
  their current status / iter / tokens / cost.

Tools — runner control:
- stop_run: signal a detached run to stop at its next cancellation
  checkpoint. Use when the user asks to stop, halt, interrupt,
  abort, or kill. No-op when no run is in flight.

Tools — context steering (user-curated file scope):
- add_to_workingset: pin specific file paths as in-scope for this
  session. Use when the user explicitly names files to focus on or
  asks you to look at specific files. Pass a paths array.
- drop_from_workingset: remove file paths from the working set.
  Use when the user says to forget, drop, ignore, or stop tracking
  specific files.
- list_workingset: show the paths currently in scope. Use when the
  user asks what files are in focus.

Tools — workspace rollback:
- list_checkpoints: list shadow-git checkpoints newest first with
  1-based step numbers. Use when the user asks for history, undo
  points, or wants to roll back.
- restore_checkpoint: roll the workspace back to a prior checkpoint
  (step=1 oldest, step=-1 newest). Use when the user asks to undo,
  revert, restore, or go back. Refuses while a run is active —
  call stop_run first.

Tools — session ops:
- show_instructions: print this agent's tool families + the natural-
  language surface contract. Use when the user asks "what can you
  do", "who are you", or "what are your tools".
- export_session: return a turn-by-turn transcript of this
  conversation. Use when the user asks to export, save, share, or
  copy the chat.
- reset_session: clear the conversation history so the next prompt
  starts fresh. Does NOT touch the workspace, frozen spec, or
  checkpoints. Confirm intent (it cannot be undone) before calling.
- remember: persist a short note (≤500 chars) into cross-session
  memory. Recent memories auto-surface in future sessions' system
  prompt. Use for: project facts, failed approaches, user
  preferences, gotchas. Do NOT use for: session-local state.
- request_user_input: pause the autonomous loop and ask the user
  ONE focused question. Use when the task is genuinely ambiguous
  (multiple acceptable interpretations / destructive op needing
  confirmation / missing user-only knowledge). After calling, END
  THE TURN with no further tool calls so the user can answer in
  their next prompt. Do NOT use for things you can figure out by
  reading the code.

Additional tools — MCP servers (dynamic):
- If the frozen spec lists MCP servers under tools.mcp_servers,
  those servers' tools appear alongside the built-ins in this
  turn's tool list. Their names are server-specific (e.g. "fs.read",
  "github.create_issue"); inspect the tool list provided by the
  runtime to see what's available.

Workflow guidance:
- For non-trivial coding tasks: declare a plan_steps plan first (each
  step with an acceptance_check command), then for each step: do the
  edit (write_file/edit_file/apply_patch) and IMMEDIATELY call verify
  with the step_id. Do not move on to the next step until the current
  step is verified (or you've decided to revise the plan because the
  acceptance_check itself was wrong). Do not tell the user the work is
  done until the final step is verified.
- For trivial tasks (one-line edits, exploration): plan_steps is
  overhead; just do the edit and call verify once at the end.
- Show the user a short summary at the end with what changed and the
  final verify result.
- For an ambiguous task: call request_user_input with ONE focused
  question (goal, scope, success criteria) BEFORE doing destructive
  work. For obvious tasks just proceed — don't ask permission for
  things you can do safely. The agent has up to 30 turns per
  prompt to drive a task to completion; use them. Each verify
  failure can be retried up to 8 times before the C1 backstop
  fires. Iterate fast, fix actual root causes, don't give up.
- For a question about workspace state: call the matching read-only
  tool (show_diff, list_sessions, …) instead of describing what you'd
  show.
- Don't enumerate available commands to the user.
- If asked "what model are you" / "어떤 모델이야", answer plainly with
  the configured provider and model from the system context line below.
- Match the user's language (English or Korean).

System context: provider=%s model=%s session=%s
`

// Prompt is the streaming agent-loop RPC. See file header for the
// V1 scope. Errors that should terminate the stream return a gRPC
// status; recoverable errors (provider blip on a single turn) emit
// a DonePart with stop_reason="error" and let the caller decide.
func (s *SessionService) Prompt(req *gilv1.PromptRequest, stream gilv1.SessionService_PromptServer) error {
	ctx := stream.Context()

	// AdversaryModel for this Prompt — empty disables AdversaryConsult
	// (P67c hands this to the chatStuckDispatcher; AltToolOrder still
	// fires regardless). Stashed early so the value survives auto-
	// create + history hydration below.
	adversaryModel := req.GetAdversaryModel()
	_ = adversaryModel // wired into dispatcher in P67c

	// providerTextLen accumulates streamed assistant text length for
	// the provider_response event emitted at turn close. Detector's
	// Monologue check uses length thresholds only — we never store
	// the actual text in the ring buffer to avoid context bloat.
	var providerTextLen int
	_ = providerTextLen // wired in P67b emit sites below

	// 1. Resolve / auto-create the session.
	sessionID := req.GetSessionId()
	if sessionID == "" {
		hint := firstTextPart(req.GetParts())
		hint = truncateGoalHint(hint, 80)
		// PromptRequest.working_dir seeds the auto-created session's
		// working directory so the agent's run_bash / write_file
		// tools have a place to operate. Empty value leaves the
		// session unrooted; tools then refuse with a clear error
		// rather than silently writing to /tmp.
		sess, err := s.repo.Create(ctx, session.CreateInput{
			GoalHint:   hint,
			WorkingDir: req.GetWorkingDir(),
		})
		if err != nil {
			return status.Errorf(codes.Internal, "auto-create session: %v", err)
		}
		sessionID = sess.ID
		if err := stream.Send(&gilv1.Part{
			Body: &gilv1.Part_SessionAllocated{
				SessionAllocated: &gilv1.SessionAllocatedPart{SessionId: sessionID},
			},
		}); err != nil {
			return err
		}
	} else {
		// Verify the session exists; surface NotFound if it doesn't.
		if _, err := s.repo.Get(ctx, sessionID); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return status.Errorf(codes.NotFound, "session %q not found", sessionID)
			}
			return status.Errorf(codes.Internal, "session lookup: %v", err)
		}
	}

	// 2. Resolve provider + model. PromptRequest.Model overrides; empty
	//    falls through to the workspace config layer (global + project).
	if s.providerFactory == nil {
		return status.Error(codes.FailedPrecondition,
			"chat agent loop requires a provider factory; gild was started without one")
	}
	provName := req.GetModel().GetProvider()
	modelID := req.GetModel().GetModelId()
	if provName == "" || modelID == "" {
		if cfgProv, cfgModel := resolveWorkspaceLLM(); cfgProv != "" || cfgModel != "" {
			if provName == "" {
				provName = cfgProv
			}
			if modelID == "" {
				modelID = cfgModel
			}
		}
	}
	prov, factoryModel, ferr := s.providerFactory(provName)
	if ferr != nil {
		return status.Errorf(codes.InvalidArgument, "provider: %v", ferr)
	}
	if modelID == "" {
		modelID = factoryModel
	}
	// P43: wrap with retry so the chat agent transparently survives
	// transient upstream blips (429 rate limit, 5xx transient,
	// connection drops). Same pattern run.go already uses for the
	// run-time agent loop. Non-retryable errors (auth, 4xx other than
	// 429) still surface immediately. The wrapper changes prov.Name()
	// to add a "+retry" suffix; downstream code that needs the un-
	// wrapped factory key uses provName (the original variable).
	prov = provider.NewRetry(prov)

	// 3. Build messages: prior history + new user message.
	hist := s.chatHistory().get(sessionID)
	userText := firstTextPart(req.GetParts())
	if userText == "" {
		return status.Error(codes.InvalidArgument, "prompt requires a non-empty text part")
	}
	// Persist the user turn upfront so the history reflects the
	// real conversation even if the agent loop terminates early
	// (provider error, max iterations).
	s.chatHistory().append(sessionID, provider.Message{Role: provider.RoleUser, Content: userText})
	msgs := append(hist, provider.Message{
		Role:    provider.RoleUser,
		Content: userText,
	})

	// P35: compact chat history when it crosses 95% of the model's
	// context window. Reuses the same Hermes pattern the runner uses
	// in-loop (core/compact.Compactor). System-driven safety net — the
	// chat agent never sees compaction happen; it just receives a
	// shorter messages slice with a synthesized middle summary. On
	// error or skip, we keep the original msgs so the user's turn
	// isn't blocked by a compaction blip. DB chat_messages stays
	// authoritative; ReplaceInMemory only swaps the in-memory list.
	if compacted, didCompact, cerr := compactChatIfNeeded(ctx, provName, modelID, prov, msgs); cerr != nil {
		// Soft-fail. Continue with msgs as-is.
		_ = cerr // intentionally unlogged at this level; the next provider call surfaces any actual context-overflow
	} else if didCompact {
		msgs = compacted
		s.chatHistory().ReplaceInMemory(sessionID, compacted)
	}

	// 4. Resolve the agent profile (system prompt + tool whitelist).
	//    PromptRequest.agent picks; empty → "default".
	agent, agentErr := resolveAgent(req.GetAgent())
	if agentErr != nil {
		return status.Errorf(codes.InvalidArgument, "%v", agentErr)
	}

	// Reset the turn-scoped diff tracker so show_diff only ever returns
	// what *this* invocation of the agent did, not history. Wired tools
	// (write_file, edit_file, apply_patch, run_bash) populate it as
	// they fire.
	if s.diffTracker != nil {
		s.diffTracker.reset(sessionID)
	}
	// 5. Build the tool registry for this turn, filtered by the
	//    agent's tool whitelist (empty whitelist = full registry).
	registry := s.buildChatToolRegistry(s.runService(), provName, modelID).filterByName(agent.Tools)

	// MCP surface in chat-mode (chat-mode parity with run-mode).
	// When the session has a frozen spec naming MCP servers, lazy-
	// launch them via the per-session cache so chat → run → chat
	// reuses one subprocess set. Tools get appended after the
	// whitelist filter — the agent's whitelist concerns built-in
	// tools, not the user's explicitly-pinned MCP servers.
	registry = appendChatMCPTools(ctx, registry, s.runService(), sessionID, s.sessionsBase)
	toolDefs := registry.defs()

	// M6 Option A bridge: emit tool_call / tool_result / done events
	// on the per-session event stream so giltui's existing Tail
	// subscription sees chat agent activity (not just run-mode). The
	// stream is allocated lazily by ensureSessionStream and persists
	// across the chat → run handoff. evtStream may be nil in tests
	// that bypass RunService (s.runService() returns nil); the helper
	// closures below no-op in that case.
	var evtStream *event.Stream
	if rs := s.runService(); rs != nil {
		evtStream = rs.ensureSessionStream(sessionID)
	}
	emitChatEvent := func(typ string, source event.Source, kind event.Kind, payload map[string]any) {
		if evtStream == nil {
			return
		}
		data, _ := json.Marshal(payload)
		_, _ = evtStream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    source,
			Kind:      kind,
			Type:      typ,
			Data:      data,
		})
	}
	// P44: wire OnRetry on the retry-wrapped chat provider so the user
	// sees backoff happening instead of a silent 30s gap during
	// exponential retry. Mirrors run.go's same hookup. The callback
	// fires AFTER each retryable failure, BEFORE the sleep.
	if rp, ok := prov.(*provider.Retry); ok {
		rp.OnRetry = func(attempt, maxAttempts int, err error, wait time.Duration) {
			emitChatEvent("provider.retry_attempt", event.SourceSystem, event.KindNote, map[string]any{
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"wait_ms":      wait.Milliseconds(),
				"err":          err.Error(),
			})
			// Also stream as a visible system Part so the chat user sees
			// the backoff in the transcript. The text matches the
			// run-side narration shape ("[retrying 2/4 · 1.0s]") so
			// both surfaces feel consistent.
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{
					Content: fmt.Sprintf("[system] provider.retry_attempt: %d/%d · waiting %dms", attempt, maxAttempts, wait.Milliseconds()),
				}},
			})
		}
	}
	// 6. Multi-turn agent loop. Each iteration calls the LLM; if it
	//    emits tool_calls, we dispatch them, append the results, and
	//    re-call. The loop terminates when the LLM returns no tool
	//    calls (StopReason="end_turn") or we hit the iteration cap.
	// P63: lifted from 8 → 30. Data from P57 chess engine dogfood
	// showed the agent was actively debugging at turn 8 (manually
	// tracing piece moves to find a Kiwipete perft bug) and would
	// have likely converged with another 2-5 turns. md2html and
	// most bench tasks converge in 2-4 turns, so lifting has no
	// downside there. P38 heartbeat + P49 cost surfacing bound the
	// worst-case runaway. Trust agent autonomy within this larger
	// envelope.
	const maxAgentTurns = 30
	systemPrompt := fmt.Sprintf(agent.SystemPrompt, provName, modelID, sessionID)
	// P55: prepend recent cross-session memories so the agent has
	// continuity across sessions. Best-effort: nil DB or query
	// failures just skip the block; the agent runs with the base
	// system prompt unchanged.
	if s.repo != nil {
		if memBlock := renderMemoriesForPrompt(loadRecentMemories(ctx, s.repo.DB())); memBlock != "" {
			systemPrompt = memBlock + systemPrompt
		}
	}
	var totalTokensIn, totalTokensOut int64
	var totalLatency time.Duration

	// C1 verify-enforcement tracker. `needsVerify` flips true on each
	// code-changing tool call (write_file / edit_file / apply_patch)
	// and back to false on a successful verify call (IsError=false).
	// If true at the "model ended turn" boundary, the system injects a
	// reminder and loops the agent for up to maxVerifyRetries more
	// iterations of this Prompt RPC. This correctly handles multi-phase
	// turns where the agent writes, verifies, then writes again — the
	// second write re-arms the gate even if a prior verify passed.
	codeChangingTools := map[string]bool{
		"write_file": true, "edit_file": true, "apply_patch": true,
	}
	needsVerify := false
	verifyRetries := 0
	// P63: lifted from 2 → 8. Real bug-fix cycles often need more than
	// 2 verify retries — compile error → fix → tests fail → fix → tests
	// pass is already 4 verifies. 2 was set defensively when chat was
	// a single-prompt-single-response surface; now it just blocks real
	// agent driving.
	const maxVerifyRetries = 8
	// P50: track the last verify call's error content so the turn-cap
	// C1 backstop can surface "which check failed" in the
	// verify_missing message instead of a generic hint. Empty when
	// verify was never called (agent did write_file but never
	// attempted verify).
	var lastVerifyErr string

	// P39: per-Prompt chat stuck tracker. Records each tool-call
	// signature (sha256 of name+input) as it fires. When three
	// consecutive identical signatures appear, we emit a single
	// stuck_detected event so the chat surface can surface "agent is
	// looping" to the user. We don't break the loop — maxAgentTurns
	// already caps it — and we don't recover (the run-time agent
	// loop's stuck strategies require a freezable spec; chat may not
	// have one). Observability only. Reset to nil after one signal
	// per turn so a real productive call sequence after the stuck
	// run isn't double-flagged.
	// P67b — per-session event ring buffer fed into core/stuck/Detector.
	// resetTurn bumps iter and clears per-turn dedup state. Push
	// iteration_start so the Detector's NoProgress check can mark this
	// turn's iteration boundary (cross-turn accumulation is what catches
	// the chess "varied-but-futile" pattern P39 ad-hoc misses).
	chatBuf := s.chatEventBufFor(sessionID)
	chatBuf.resetTurn()
	chatBuf.push(event.Event{
		Type: "iteration_start",
		Data: jsonMust(map[string]any{"iter": chatBuf.iter}),
	})

	// P67c — Detector dispatcher. AltToolOrder fires always (chat-only
	// strategies that don't need an LLM); AdversaryConsult only when
	// adversaryModel is set (PromptRequest.adversary_model). Provider
	// and model are already resolved at this point.
	chatDispatch := &chatStuckDispatcher{
		detector: &stuck.Detector{},
		strategies: []stuck.Strategy{
			stuck.AltToolOrderStrategy{},
			stuck.AdversaryConsultStrategy{},
		},
		provider: prov,
		model:    modelID,
		riskAdv:  adversaryModel,
	}

	// P66: consecutive-tool-timeout abort. Eval-loop task17 SPMC
	// burned 32 minutes in a single turn when the agent's lock-free
	// SPMC livelocked and `go test -race` hung. Each bash/verify
	// tool call timed out at its internal 30-60s limit, but the
	// agent kept retrying — 30+ sequential timeouts in one turn.
	// maxAgentTurns doesn't help because the whole problem is
	// inside one turn. Track timeouts across calls (any non-timeout
	// result resets); abort when we hit a threshold.
	const maxConsecutiveTimeouts = 3
	consecutiveTimeouts := 0

	for turn := 0; turn < maxAgentTurns; turn++ {
		// P63c: removed "every 5 iters compaction" — v4 chess dogfood
		// data showed this aggressively wiped middle context (failing
		// test details), and the agent then looped emitting empty
		// end_turn replies because it didn't remember what was broken.
		// P35's entry-time + threshold-driven check is enough — long
		// loops still get compacted if they cross the 95% threshold,
		// but only when actually under pressure, not on a cadence.
		// Compaction is pressure-driven, not cadence-driven.
		t0 := time.Now()
		// Temperature: PromptRequest.temperature overrides the default
		// when > 0 (Finding #6, 2026-05-18 — dogfood probes benefit
		// from 0.2–0.3). Chat surface stays at 0.7 by default.
		temperature := 0.7
		if t := req.GetTemperature(); t > 0 {
			temperature = t
		}
		resp, err := prov.Complete(ctx, provider.Request{
			Model:       modelID,
			Messages:    msgs,
			System:      systemPrompt,
			Tools:       toolDefs,
			MaxTokens:   2048,
			Temperature: temperature,
		})
		totalLatency += time.Since(t0)
		if err != nil {
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Done{
					Done: &gilv1.DonePart{StopReason: "error", ErrorMessage: err.Error()},
				},
			})
			return status.Errorf(codes.Internal, "provider.Complete: %v", err)
		}
		totalTokensIn += resp.InputTokens
		totalTokensOut += resp.OutputTokens

		// P33: stream the upstream-separated chain-of-thought BEFORE the
		// final answer so the user sees what the model was working
		// through. Not persisted to chat history (the model regenerates
		// fresh reasoning each turn — replaying old reasoning wastes
		// tokens and confuses the next-turn context).
		if resp.Reasoning != "" {
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Reasoning{Reasoning: &gilv1.ReasoningDelta{Content: resp.Reasoning}},
			}); err != nil {
				return err
			}
		}

		// Stream any text the LLM emitted on this turn.
		if resp.Text != "" {
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: resp.Text}},
			}); err != nil {
				return err
			}
		}
		// P67b — push provider_response with length only (no text body)
		// so Detector's Monologue check has the per-response signal it
		// needs without ballooning the ring buffer.
		providerTextLen += len(resp.Text)
		chatBuf.push(event.Event{
			Type: "provider_response",
			Data: jsonMust(map[string]any{"text_len": len(resp.Text)}),
		})

		// If no tool calls, the LLM thinks it's done. Apply the C1
		// verify-gate before letting the turn close.
		if len(resp.ToolCalls) == 0 {
			s.chatHistory().append(sessionID,
				provider.Message{Role: provider.RoleAssistant, Content: resp.Text})

			if !needsVerify {
				break
			}
			if verifyRetries >= maxVerifyRetries {
				// Stubborn agent. Surface an error done so callers know
				// the work isn't actually verified. Emit as system text
				// first so chat clients render it visibly (the Done
				// part itself is silent unless stop_reason="error").
				msg := "code-changing tools were called but verify was never run; turn aborted"
				_ = stream.Send(&gilv1.Part{
					Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: "[system] verify_missing: " + msg}},
				})
				_ = stream.Send(&gilv1.Part{
					Body: &gilv1.Part_Done{
						Done: &gilv1.DonePart{StopReason: "verify_missing", ErrorMessage: msg},
					},
				})
				return status.Errorf(codes.FailedPrecondition, "%s", msg)
			}

			// Inject a synthetic user message reminding the agent. Covers
			// both "verify not called" and "verify called but failed/rejected"
			// — toolVerify sets IsError=true on !pass (real failure) and on
			// weak-command schema reject. The agent must either fix the
			// underlying issue and re-run verify, or call a real verify if
			// none was made.
			reminder := "Reminder: you called write_file/edit_file/apply_patch " +
				"but verify has not reported success this turn. Call verify " +
				"with a real behavior check (build, test, lint) — and if a " +
				"prior verify failed, fix the underlying issue first."
			reminderMsg := provider.Message{Role: provider.RoleUser, Content: reminder}
			msgs = append(msgs, reminderMsg)
			s.chatHistory().append(sessionID, reminderMsg)
			// Echo the reminder to the stream so observers see the gate fire.
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: "[system] " + reminder}},
			})
			verifyRetries++
			continue
		}

		// Append the assistant turn (with tool calls) to messages so
		// the LLM sees the call→result correlation on the next turn.
		assistantTurn := provider.Message{
			Role:      provider.RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		}
		msgs = append(msgs, assistantTurn)
		s.chatHistory().append(sessionID, assistantTurn)

		// Dispatch each tool call, stream Parts, collect results.
		toolResults := make([]provider.ToolResult, 0, len(resp.ToolCalls))
		abortedTimeoutLoop := false
		for _, call := range resp.ToolCalls {
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_ToolCall{ToolCall: &gilv1.ToolCallPart{
					Id:        call.ID,
					Name:      call.Name,
					InputJson: string(call.Input),
				}},
			}); err != nil {
				return err
			}
			// Bridge to per-session event stream (M6 Option A) so giltui's
			// Tail subscription mirrors the call exactly the same way it
			// mirrors run-mode tool_calls.
			emitChatEvent("tool_call", event.SourceAgent, event.KindAction, map[string]any{
				"id":    call.ID,
				"name":  call.Name,
				"input": string(call.Input),
			})
			// P67b — mirror tool_call into the chat Detector ring buffer.
			// Synthetic verify_run is emitted *before* dispatch when the
			// agent invokes the verify tool, so Detector's NoProgress
			// check has the iteration-boundary signal it needs.
			chatBuf.push(event.Event{
				Type: "tool_call",
				Data: jsonMust(map[string]any{
					"id":    call.ID,
					"name":  call.Name,
					"input": string(call.Input),
				}),
			})
			if call.Name == "verify" {
				chatBuf.push(event.Event{Type: "verify_run"})
			}
			// (P39 ad-hoc stuck detector removed in P67e — Detector's
			// PatternRepeatedActionObservation covers the same case
			// via chatStuckDispatcher.tick after the tool_result push
			// below.)
			result, runErr := dispatchTool(ctx, registry, sessionID, call)
			if runErr != nil {
				result = provider.ToolResult{
					ToolUseID: call.ID,
					Content:   "tool dispatch failed: " + runErr.Error(),
					IsError:   true,
				}
			}
			toolResults = append(toolResults, result)
			// C1: track which tool categories fired this turn.
			// iter86a: only arm needsVerify on SUCCESSFUL code-changing
			// calls. A failed write_file/edit_file/apply_patch (path
			// escape, readonly target, hunk not found, etc.) didn't
			// actually change any files, so requiring a verify after
			// it surfaces verify_missing on a turn that did nothing —
			// false-positive that wastes turns and confuses the agent.
			if codeChangingTools[call.Name] && !result.IsError {
				needsVerify = true
			}
			if call.Name == "verify" && !result.IsError {
				needsVerify = false
			}
			// P50: capture the last failed verify output so the C1
			// backstop's verify_missing message can surface it. Users
			// hitting the turn cap with broken code (P48 dogfood
			// pattern) get an actionable hint about WHICH check
			// failed instead of a generic "you never verified" line.
			if call.Name == "verify" && result.IsError {
				lastVerifyErr = result.Content
			}
			if err := stream.Send(&gilv1.Part{
				Body: &gilv1.Part_ToolResult{ToolResult: &gilv1.ToolResultPart{
					CallId:  call.ID,
					Content: result.Content,
					IsError: result.IsError,
				}},
			}); err != nil {
				return err
			}
			emitChatEvent("tool_result", event.SourceEnvironment, event.KindObservation, map[string]any{
				"id":       call.ID,
				"name":     call.Name,
				"content":  result.Content,
				"is_error": result.IsError,
			})
			// P67b — mirror tool_result into the chat Detector buffer.
			// Synthetic verify_result with passed=(!IsError) follows when
			// the tool name is verify; Detector's NoProgress uses that to
			// count "verifier still failing" iterations. Schema must
			// match core/runner's emit shape (snake_case `is_error`,
			// `content` field) — Detector's pattern checks use these
			// exact field names.
			chatBuf.push(event.Event{
				Type: "tool_result",
				Data: jsonMust(map[string]any{
					"id":       call.ID,
					"name":     call.Name,
					"is_error": result.IsError,
					"content":  truncateForDetector(result.Content),
				}),
			})
			if call.Name == "verify" {
				chatBuf.push(event.Event{
					Type: "verify_result",
					Data: jsonMust(map[string]any{"passed": !result.IsError}),
				})
			}

			// P67c — Detector tick. Each Decision surfaces as a visible
			// system Part so the agent's next inference sees it inline;
			// `adversary_consulted` event mirrors for giltui Tail + audit.
			// Adversary skip-budget sentinel (Explanation prefix) becomes
			// the `adversary_skipped_budget` event (no Part) — P67d.
			for _, dec := range chatDispatch.tick(ctx, chatBuf, chatHistoryToProviderMessages(s.chatHistory().get(sessionID), 10)) {
				if dec.Action == stuck.ActionAdversaryConsult && strings.HasPrefix(dec.Explanation, "ADVERSARY_SKIPPED_BUDGET") {
					emitChatEvent("adversary_skipped_budget", event.SourceSystem, event.KindNote, map[string]any{
						"session": sessionID,
					})
					continue
				}
				prefix := "[system] stuck-recover (" + dec.Action.String() + ")"
				if dec.Action == stuck.ActionAdversaryConsult {
					prefix = "[system] adversary"
				}
				_ = stream.Send(&gilv1.Part{
					Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{
						Content: prefix + ": " + dec.Explanation,
					}},
				})
				emitChatEvent("adversary_consulted", event.SourceSystem, event.KindNote, map[string]any{
					"action":      dec.Action.String(),
					"explanation": dec.Explanation,
				})
			}

			// P66: track consecutive timeout results. Bash returns
			// `timeout after Ns\n...`; verify returns `[TIMEOUT] ...`.
			// Any non-timeout result resets the streak (success OR
			// other errors — the agent did something different).
			if result.IsError && isToolTimeoutResult(result.Content) {
				consecutiveTimeouts++
				if consecutiveTimeouts >= maxConsecutiveTimeouts {
					abortedTimeoutLoop = true
					break
				}
			} else {
				consecutiveTimeouts = 0
			}
		}

		// Feed the tool results back as a synthetic user turn (per
		// Anthropic's tool_result block convention) so the LLM can
		// see them on the next iteration.
		toolFeedback := provider.Message{
			Role:        provider.RoleUser,
			ToolResults: toolResults,
		}
		msgs = append(msgs, toolFeedback)
		s.chatHistory().append(sessionID, toolFeedback)

		// P66: if we hit the consecutive-timeout cap, surface a clear
		// signal to the client and end this Prompt RPC. The agent
		// can't recover within this turn (every tool call is hanging);
		// the user/dogfood runner is the right escalation level.
		if abortedTimeoutLoop {
			msg := fmt.Sprintf("agent issued %d consecutive tool calls that timed out — likely a hung subprocess (deadlock, infinite test loop, etc.). Aborting before more budget is wasted.", maxConsecutiveTimeouts)
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: "[system] tool_timeout_loop: " + msg}},
			})
			_ = stream.Send(&gilv1.Part{
				Body: &gilv1.Part_Done{
					Done: &gilv1.DonePart{StopReason: "tool_timeout_loop", ErrorMessage: msg},
				},
			})
			emitChatEvent("tool_timeout_loop", event.SourceSystem, event.KindNote, map[string]any{
				"consecutive_timeouts": consecutiveTimeouts,
				"max":                  maxConsecutiveTimeouts,
			})
			return nil
		}
	}

	// P32 iter6: post-loop C1 backstop. The for loop can exit by hitting
	// `maxAgentTurns` while still iterating with tool calls (i.e.
	// before the agent ever stopped to let the no-tool-calls C1 gate
	// run). Eval-loop iter6/L7 surfaced this — agent did 9+ tool calls
	// including write_file → verify (PASS) → write_file → write_file
	// without a final verify, but the loop hit turn=8 and exited
	// silently. needsVerify was true but the gate never fired.
	//
	// If we exited the for loop via the turn cap and a code-changing
	// tool call is still un-verified, surface the same
	// FailedPrecondition the no-tool-calls path would have.
	if needsVerify {
		// P50: more actionable message. When verify was attempted at
		// least once and failed, surface a truncated tail of the
		// failure so the user re-prompts with concrete context. When
		// verify was never attempted, keep the existing generic line.
		msg := "agent turn cap reached but a code-changing tool call (write_file/edit_file/apply_patch) " +
			"was never followed by a successful verify; turn aborted"
		if lastVerifyErr != "" {
			tail := lastVerifyErr
			if len(tail) > 400 {
				tail = "…" + tail[len(tail)-400:]
			}
			tail = strings.ReplaceAll(tail, "\n", " · ")
			msg = "agent turn cap reached with a failing verify. Last verify output: " + tail
		}
		_ = stream.Send(&gilv1.Part{
			Body: &gilv1.Part_Text{Text: &gilv1.TextDelta{Content: "[system] verify_missing: " + msg}},
		})
		_ = stream.Send(&gilv1.Part{
			Body: &gilv1.Part_Done{
				Done: &gilv1.DonePart{StopReason: "verify_missing", ErrorMessage: msg},
			},
		})
		return status.Errorf(codes.FailedPrecondition, "%s", msg)
	}

	// 6. Metrics + Done. P49: include cost_usd so the chat surface
	// can show running spend instead of just token counts. Best-effort
	// — unknown models (not in the embedded catalog) just return 0
	// and the surface degrades gracefully to tokens-only.
	costCalc := cost.NewCalculator()
	turnCost, _ := costCalc.Estimate(modelID, cost.Usage{
		InputTokens:  totalTokensIn,
		OutputTokens: totalTokensOut,
	})
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Metrics{Metrics: &gilv1.PromptMetrics{
			TokensIn:  totalTokensIn,
			TokensOut: totalTokensOut,
			CostUsd:   turnCost,
			LatencyMs: totalLatency.Milliseconds(),
		}},
	}); err != nil {
		return err
	}
	if err := stream.Send(&gilv1.Part{
		Body: &gilv1.Part_Done{Done: &gilv1.DonePart{StopReason: "end_turn"}},
	}); err != nil {
		return err
	}
	return nil
}

// dispatchTool looks up a tool by name and invokes it with the
// LLM-provided input. Returns a typed ToolResult ready to feed back
// to the LLM. Unknown tool names produce an IsError result so the
// LLM can self-correct without crashing the stream.
func dispatchTool(ctx context.Context, registry *chatToolRegistry, sessionID string, call provider.ToolCall) (provider.ToolResult, error) {
	tool, ok := registry.lookup(call.Name)
	if !ok {
		return provider.ToolResult{
			ToolUseID: call.ID,
			Content:   "unknown tool: " + call.Name,
			IsError:   true,
		}, nil
	}
	r, err := tool.run(ctx, sessionID, call.Input)
	r.ToolUseID = call.ID
	// iter18a (L18): tool Content goes onto the gRPC stream as a proto
	// `string` field, which protobuf rejects with "marshaling: string
	// field contains invalid UTF-8" if any tool returned bytes that
	// aren't valid UTF-8 (read_file on a file truncated mid-multibyte,
	// run_bash on a binary). Sanitize once at the dispatch boundary so
	// every tool inherits the protection. Replacement chars preserve
	// the agent's ability to reason about the rest of the content.
	if !utf8.ValidString(r.Content) {
		r.Content = strings.ToValidUTF8(r.Content, "�")
	}
	// iter36a: redact known credential values from tool output so a
	// run_bash `cat ~/.config/gil/auth.json` (or any equivalent indirect
	// read) doesn't leak the api_key into chat history → next provider
	// turn → user-visible response. read_file is sandboxed to the
	// session working dir, but run_bash isn't — and the threat model
	// includes prompt-injection via webfetched content (eval-loop
	// iter34/iter36). Single chokepoint protection rather than
	// trying to constrain run_bash, which preserves agent freedom.
	r.Content = redactKnownSecrets(r.Content)
	return r, err
}

// knownSecrets is the daemon-wide set of credential values to redact
// from tool output. Loaded lazily on first dispatchTool call from the
// process env (~/.config/gil/auth.json, ~/.env, GIL_*_KEY env vars).
var (
	knownSecretsOnce sync.Once
	knownSecretsList []string
)

func redactKnownSecrets(s string) string {
	knownSecretsOnce.Do(loadKnownSecrets)
	if len(knownSecretsList) > 0 {
		for _, secret := range knownSecretsList {
			if secret == "" {
				continue
			}
			s = strings.ReplaceAll(s, secret, "[REDACTED-SECRET]")
		}
	}
	// iter156: also catch secret-shape values that aren't in the
	// daemon-wide registry — projects regularly ship config files with
	// their own keys, and the registry only sees ~/.env + auth.json.
	// Same prefix set as iter93a's value-shape fallback. Run the inline
	// scan after the registry pass so legitimate registry values that
	// happen to also match a prefix are still replaced with the same
	// [REDACTED-SECRET] sentinel and don't get a duplicate replacement.
	return redactInlineSecretShapes(s)
}

// redactInlineSecretShapes scans the string for tokens matching known
// provider-key prefixes and replaces them with [REDACTED-SECRET]. Tight
// shape so the function doesn't gnaw on prose: a "token" is a maximal
// run of [A-Za-z0-9._-] starting with one of the known prefixes and
// at least 16 chars long. iter156.
func redactInlineSecretShapes(s string) string {
	const minLen = 16
	const sentinel = "[REDACTED-SECRET]"
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		// Find the next prefix match.
		matched := -1
		var matchPrefix string
		for _, p := range secretValuePrefixes {
			if strings.HasPrefix(s[i:], p) {
				matched = i
				matchPrefix = p
				break
			}
		}
		if matched < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Extend through the secret-token charset.
		end := matched + len(matchPrefix)
		for end < len(s) && isSecretChar(s[end]) {
			end++
		}
		token := s[matched:end]
		if len(token) >= minLen {
			b.WriteString(sentinel)
			i = end
			continue
		}
		// Not long enough — keep verbatim.
		b.WriteString(token)
		i = end
	}
	return b.String()
}

func isSecretChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || c == '-' || c == '.':
		return true
	}
	return false
}

func loadKnownSecrets() {
	// Best-effort. Failures here mean less redaction, not a daemon
	// crash — secrets just stay on the wire as before this fix.
	addSecretsFromAuthJSON("/home/ubuntu/.config/gil/auth.json")
	if home, err := os.UserHomeDir(); err == nil {
		addSecretsFromAuthJSON(filepath.Join(home, ".config", "gil", "auth.json"))
		addSecretsFromAuthJSON(filepath.Join(home, ".codex", "auth.json"))
		addSecretsFromDotenv(filepath.Join(home, ".env"))
	}
	for _, k := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	} {
		if v := os.Getenv(k); len(v) >= 8 {
			knownSecretsList = append(knownSecretsList, v)
		}
	}
}

func addSecretsFromAuthJSON(path string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Parse loosely — auth.json schema is "providers": {name: {api_key, ...}}.
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return
	}
	walkSecrets(doc, []string{"api_key", "apiKey", "token", "secret", "password"})
}

func walkSecrets(node any, secretKeys []string) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			for _, sk := range secretKeys {
				if strings.EqualFold(k, sk) {
					if vs, ok := v.(string); ok && len(vs) >= 8 {
						knownSecretsList = append(knownSecretsList, vs)
					}
				}
			}
			walkSecrets(v, secretKeys)
		}
	case []any:
		for _, item := range n {
			walkSecrets(item, secretKeys)
		}
	}
}

// secretValuePrefixes catches credential values whose dotenv KEY does
// not name a token/secret (e.g. provider-named vars like POLLINATIONS,
// OPENROUTER). Add real-world prefixes as new providers ship.
var secretValuePrefixes = []string{
	"sk-",    // OpenAI, Anthropic, OpenRouter (sk-or-), etc.
	"sk_",    // Stripe-style, Pollinations
	"gho_",   // GitHub OAuth
	"ghp_",   // GitHub personal
	"ghs_",   // GitHub server
	"ghu_",   // GitHub user-to-server
	"glpat-", // GitLab PAT
	"AIza",   // Google API key
	"xoxb-",  // Slack bot
	"xoxp-",  // Slack user
	"xoxa-",  // Slack app
	"ya29.",  // Google OAuth
	"eyJ",    // JWT
}

func looksLikeSecretValue(val string) bool {
	if len(val) < 16 {
		return false
	}
	for _, p := range secretValuePrefixes {
		if strings.HasPrefix(val, p) {
			return true
		}
	}
	return false
}

func addSecretsFromDotenv(path string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, "\"'")
		// Two-pass: credential-shaped key OR value with a known
		// secret-prefix. Either match qualifies for redaction.
		k := strings.ToLower(key)
		keyMatches := strings.Contains(k, "key") || strings.Contains(k, "token") ||
			strings.Contains(k, "secret") || strings.Contains(k, "password") ||
			strings.Contains(k, "credential")
		if !keyMatches && !looksLikeSecretValue(val) {
			continue
		}
		if len(val) >= 8 {
			knownSecretsList = append(knownSecretsList, val)
		}
	}
}

// isToolTimeoutResult reports whether a tool-result Content body
// indicates a tool-internal timeout (vs a regular error). The two
// shapes in use (P66):
//   - bash via agent_tools_write.go run_bash:
//     `timeout after 30s\n--- partial output ---\n…`
//   - verify via agent_tools_plan_verify.go:
//     `[TIMEOUT] description — exit=124, duration=…`
//
// Pure function — kept exported-by-test-pattern (lowercase but in
// the same package) and pinned by tests so the prefix check survives
// formatter tweaks.
func isToolTimeoutResult(content string) bool {
	if strings.HasPrefix(content, "timeout after ") {
		return true
	}
	if strings.HasPrefix(content, "[TIMEOUT] ") {
		return true
	}
	return false
}

// chatHistory lazily allocates the message log map. Stored on the
// service struct via a method-level singleton instead of constructor
// wiring so existing test setups (NewSessionService(repo, nil)) keep
// compiling without churn. P34: when the SessionService has a Repo,
// the store is auto-wired with the underlying *sql.DB on first
// allocation so append/get/reset survive a daemon restart.
func (s *SessionService) chatHistory() *chatHistory {
	s.chatHistMu.Lock()
	defer s.chatHistMu.Unlock()
	if s.chatHist == nil {
		s.chatHist = newChatHistory()
		if s.repo != nil {
			s.chatHist.SetDB(s.repo.DB())
		}
	}
	return s.chatHist
}

// appendChatMCPTools surfaces the session's MCP-advertised tools
// into the chat agent's registry. No-op when:
//   - the run service isn't available (tests that bypass it)
//   - no spec is frozen (chat-only / pre-freeze conversations)
//   - the frozen spec's Tools.McpServers allowlist is empty
//
// Cache + launch live on RunService.ensureSessionMCPTools so chat
// and run share one subprocess set. Adapter is necessary because
// chat tools take a sessionID at run-time while core/tool.Tool
// (which MCP RemoteTool implements) doesn't.
func appendChatMCPTools(ctx context.Context, registry *chatToolRegistry, rs *RunService, sessionID, sessionsBase string) *chatToolRegistry {
	if rs == nil || sessionsBase == "" {
		return registry
	}
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	spec, err := store.Load()
	if err != nil || spec == nil {
		return registry
	}
	workspaceDir := ""
	if spec.Workspace != nil {
		workspaceDir = spec.Workspace.Path
	}
	mcpTools := rs.ensureSessionMCPTools(ctx, sessionID, spec, workspaceDir, nil)
	if len(mcpTools) == 0 {
		return registry
	}
	for _, t := range mcpTools {
		registry.tools = append(registry.tools, &coreToolAdapter{t: t})
	}
	return registry
}

// firstTextPart pulls the text body off the first PromptPart that
// has one. V1 only supports text, so this is the canonical reader;
// future image/file parts will need their own extractors.
func firstTextPart(parts []*gilv1.PromptPart) string {
	for _, p := range parts {
		if t := p.GetText(); t != "" {
			return t
		}
	}
	return ""
}

// truncateGoalHint clips a free-form prompt for use as the auto-
// created session's goal_hint column. Same shape as the cli REPL's
// truncateHint helper but lives here so the daemon's session row
// stays compact regardless of whether the request came from cli or
// tui.
//
// iter71a: strip control characters (anything below 0x20 except
// printable whitespace) so a prompt containing ANSI escape sequences
// (`\x1b[2J\x1b[H`) can't poison the welcome-banner display in the
// next `gil chat` session. Without this, an attacker who can drop a
// prompt (or a teammate sharing a daemon, or a script writing past
// session names) could takeover the user's terminal via injected
// escape codes.
func truncateGoalHint(s string, max int) string {
	s = sanitizeHintControlChars(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func sanitizeHintControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Keep printable + standard whitespace (space + tab + newline);
		// collapse newlines to space so the hint stays one line.
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Drop control chars (ESC = 0x1b, etc.).
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// resolveWorkspaceLLM reads the layered workspace config (global +
// the daemon's cwd as a stand-in for project-local) and returns the
// provider/model pair. Best-effort — any error returns ("", "") and
// the caller falls back to the providerFactory's defaults.
func resolveWorkspaceLLM() (string, string) {
	layout, err := paths.FromEnv()
	if err != nil {
		return "", ""
	}
	cfg, err := workspace.Resolve(layout.ConfigFile(), "")
	if err != nil {
		return "", ""
	}
	return cfg.Provider, cfg.Model
}

// _ stops the linter complaining about the unused fmt import for
// follow-up commits that will reach for fmt for tool-call rendering.
var _ = fmt.Sprintf
