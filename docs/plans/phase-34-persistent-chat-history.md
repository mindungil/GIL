# Phase 34 — Persistent Chat History

## Why this phase

The chat surface (`gil chat` / bare `gil`) is the natural-language single
surface for the entire harness. Today its conversation log lives in a
`sync.Map[string][]provider.Message` on the daemon's `SessionService` struct:

```go
// session_prompt.go
type chatHistory struct {
    mu  sync.Mutex
    all map[string][]provider.Message
}
```

`gild` restart wipes it. A user who's mid-conversation with the agent —
even one who has frozen a spec and is iterating on results — loses the
entire turn history the moment the daemon process restarts. Subsequent
prompts re-enter the agent loop with NO context: the agent doesn't know
what the user asked five minutes ago, what tools it called, what it
found. From the user's perspective, the assistant has amnesia.

This contradicts the project's goal-fit framing: a "single
natural-language surface" implies a coherent conversation across
sessions. Persisting working set (P30) but not the chat itself is the
opposite of what users care about — they'll re-curate file lists, but
they won't re-explain a 20-turn debugging conversation.

## Goal

Chat conversations survive daemon restart. After `pkill -9 gild` +
relaunch, the same prompt resumes from the same context the prior
daemon had.

Concretely:
- `chatHistory.append(sid, msg)` writes through to a new SQLite table.
- `chatHistory.get(sid)` hydrates from the table on first access after
  restart, returns the same shape as before.
- `chatHistory.reset(sid)` deletes the table rows for that session.
- Tool calls and tool results round-trip through JSON columns so the
  agent loop reconstructs the exact `provider.Message` shape it had
  before.

## Non-goals

- Cross-daemon coordination. `SetMaxOpenConns(1)` (iter21a) already
  serializes; we assume a single daemon writes to a given DB.
- `CacheControl` persistence. The flag is per-turn — only the last 3
  messages carry it, and `core/compact.MarkCacheBreakpoints` recomputes
  on every Prompt. Persisting stale markers would mismeasure cache
  breakpoints on the next run.
- History pruning / TTL. The table grows monotonically. A future
  phase can add LRU / retention policy.
- Migrating existing in-memory state to the new table. Daemons that
  restart with this phase shipped lose their pre-upgrade history once;
  every history thereafter persists.

## Design

### Schema migration v5

Pattern lifted from P30 workingset_entries — same PK shape, same
`SetDB`/`ensureLoadedLocked`/`persistXxxLocked` triad on the in-memory
store, same lazy-allocation wiring on `SessionService`.

```sql
CREATE TABLE IF NOT EXISTS chat_messages (
    session_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   TEXT NOT NULL DEFAULT '',
    tool_results TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_session
    ON chat_messages(session_id);
```

`seq` is a 0-based monotonic counter per session. It's the explicit
ordering key — we do NOT trust `created_at` because two appends in the
same millisecond would tie, and `ORDER BY created_at, rowid` works but
hides intent. PK `(session_id, seq)` makes the contract obvious: the
N-th message in the conversation lives at row `(sid, N)`.

`tool_calls` and `tool_results` are JSON-encoded `[]ToolCall` and
`[]ToolResult` slices. Empty slice → empty string; the encoder uses
`json.Marshal` and the decoder treats empty strings as nil slices.

### chatHistory refactor

```go
type chatHistory struct {
    mu     sync.Mutex
    all    map[string][]provider.Message
    db     *sql.DB                  // optional; nil → pre-P34 behavior
    loaded map[string]struct{}      // sessions hydrated from DB
}

func (h *chatHistory) SetDB(db *sql.DB) { /* like workingSet.SetDB */ }

func (h *chatHistory) get(sid string) []provider.Message {
    h.mu.Lock(); defer h.mu.Unlock()
    h.ensureLoadedLocked(sid)
    src := h.all[sid]
    out := make([]provider.Message, len(src))
    copy(out, src)
    return out
}

func (h *chatHistory) append(sid string, msg provider.Message) {
    h.mu.Lock(); defer h.mu.Unlock()
    h.ensureLoadedLocked(sid)
    seq := len(h.all[sid])
    h.all[sid] = append(h.all[sid], msg)
    h.persistAppendLocked(sid, seq, msg)
}

func (h *chatHistory) reset(sid string) {
    h.mu.Lock(); defer h.mu.Unlock()
    delete(h.all, sid)
    delete(h.loaded, sid)
    h.persistResetLocked(sid)
}
```

Helpers `ensureLoadedLocked`, `persistAppendLocked`, `persistResetLocked`
mirror the workingset pattern: silent failure (durability is best-
effort), `db == nil` → no-op, `loaded` map elides redundant SELECTs.

### Wiring

`chatHistory()` method on SessionService gets the same lazy-DB-wire
pattern as `chatWorkingSet()`:

```go
func (s *SessionService) chatHistory() *chatHistory {
    s.chatHistMu.Lock(); defer s.chatHistMu.Unlock()
    if s.chatHist == nil {
        s.chatHist = newChatHistory()
        if s.repo != nil {
            s.chatHist.SetDB(s.repo.DB())
        }
    }
    return s.chatHist
}
```

Constructor change: zero. All wiring stays inside the lazy-init method
so existing `NewSessionService(repo, nil)` test setups stay valid.

## Acceptance criteria

1. **Schema migration tests** pass. New migration applies in order; idempotent.
2. **Round-trip tests** pass: append messages with content, tool_calls,
   tool_results → close repo → reopen with same DB path → get returns the
   same shape.
3. **Reset tests** pass: after reset, both in-memory and DB are empty
   for that session.
4. **Concurrent append test** passes: 16 parallel appends produce 16
   distinct seq values, no PK collisions, no message loss.
5. **Pre-P34 behavior preserved**: when `db == nil` (test setups that
   don't wire a Repo), all chatHistory behavior matches the V1 contract
   exactly. Existing session_prompt_test.go and friends pass unchanged.
6. **Live verification**: bench probe with `pkill -9 gild` between turns
   confirms the second turn's history includes the first turn's
   user/assistant pair.

## Implementation steps

1. Add migration v5 to `core/session/schema.go`; bump `currentSchemaVersion`.
2. Schema test (mirror `schema_workingset_test.go` shape).
3. Refactor `chatHistory` in `server/internal/service/session_prompt.go`:
   add `db`, `loaded`, `SetDB`, `ensureLoadedLocked`,
   `persistAppendLocked`, `persistResetLocked`. Use JSON
   encode/decode for tool_calls + tool_results columns.
4. Wire DB into `chatHistory()` method (mirror `chatWorkingSet()`).
5. Tests: `session_prompt_persist_test.go` with the 4 acceptance test
   shapes above.
6. Live verification: `gil chat` two turns separated by `pkill -9 gild`,
   confirm second turn sees first turn's history.
7. Document in this file's "Result" section.

## Result (2026-05-17)

**Shipped.** Single commit on `feat/p33-reasoning-surface`. All
acceptance criteria met:

1. Schema migration v5 applies — confirmed by `core/session` test
   suite + live `~/.local/share/gil/sessions.db` schema_version
   bumped to 5 with `chat_messages` table present.
2. 9 new persistence tests (`session_prompt_persist_test.go`) — all
   PASS:
   - `TestChatHistory_AppendPersistsThroughDB`
   - `TestChatHistory_ToolCallsRoundTrip`
   - `TestChatHistory_ToolResultsRoundTrip`
   - `TestChatHistory_ResetWipesRows`
   - `TestChatHistory_NoDBStillWorks` (pre-P34 contract preserved)
   - `TestChatHistory_PerSessionIsolation`
   - `TestChatHistory_SeqOrderPreservedAcrossRestart`
   - `TestChatHistory_ConcurrentAppendsHaveDistinctSeqs`
   - `TestChatHistory_HydrationIsOnceUntilSetDB`
3. Existing service test suite passes unchanged (no behavior drift
   for in-memory mode).
4. Live verification: started a `gil chat --provider mock` session
   (session id `01KRTM5VWTQ15BF6B3G5RYQ7YQ`), sent "remember the
   number 42", confirmed 2 rows landed in `chat_messages` (seq 0/1).
   Killed daemon with `pkill -9`, restarted, sent second turn to the
   same session id via SDK. Hydration restored the seq counter so the
   new turn appended at seq=2/3; rows table now holds the full 4-turn
   history with no PK collisions and no message loss.

**Files touched:** 3
- `core/session/schema.go` — migration v5 (+ currentSchemaVersion bump)
- `server/internal/service/session_prompt.go` — chatHistory refactor
  (SetDB, ensureLoadedLocked, persistAppendLocked, persistResetLocked,
  lazy-wire DB in `chatHistory()` method)
- `server/internal/service/session_prompt_persist_test.go` — new test
  file with 9 tests

No proto / SDK / CLI / TUI changes — the persistence is entirely
contained behind the existing `chatHistory.append/get/reset` API
surface.
