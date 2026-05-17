# Phase 55 — Cross-session memory bank

## Why

Today every chat session starts blind. The user might have spent
30 turns yesterday teaching the agent about "the auth module uses
session_token, not access_token" — but today's session has no
knowledge of that. The user has to re-explain.

For the autonomous coding harness goal, this is a real ceiling.
Truly autonomous coding means the agent learns from PRIOR sessions:
"last time I tried X on this repo, it broke Y; this time I'll try
W instead."

## Goal

A simple cross-session memory bank that:
1. The agent can write to via a `remember(text)` tool — short notes
   the agent thinks are worth keeping.
2. The agent automatically reads on chat-session entry — recent
   memories appear in the system prompt context.
3. Persists across daemon restart (uses the SQLite schema like
   P30/P34).

## Non-goals (v1)

- Semantic retrieval (just chronological, recent-first).
- Per-project filtering (all memories are global to the daemon).
- Memory editing/deletion by the agent (only writes accumulate;
  user can clear via CLI).
- Embedding-based similarity (no vector store).
- Auto-summarization of past sessions (the agent writes what it
  wants to keep; nothing auto-mined).

These can all come later. v1 is the simplest credible thing.

## Design

### Schema migration v6

```sql
CREATE TABLE IF NOT EXISTS session_memories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL,
    content     TEXT NOT NULL,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_session_memories_created
    ON session_memories(created_at);
CREATE INDEX IF NOT EXISTS idx_session_memories_session
    ON session_memories(session_id);
```

Why id PRIMARY KEY (not session_id+seq): a single session can write
many memories; ordering across sessions matters more than per-session
ordering; auto-increment id gives a natural recent-first sort key.

### `remember` tool

```go
type toolRemember struct{ ... }

func (t *toolRemember) name() string { return "remember" }

func (t *toolRemember) description() string {
    return "Persist a short note (≤500 chars) into the cross-session " +
        "memory bank. Recent memories are surfaced to the next chat " +
        "session's system prompt automatically. Use for: project " +
        "facts you've learned (this codebase uses X not Y), failed " +
        "approaches (tried Z, broke because W), user preferences " +
        "(user prefers tabs to spaces), or non-obvious gotchas. " +
        "Do NOT use for: session-specific state (use the chat " +
        "history for that), large content (truncated at 500 chars)."
}
```

### Recall on session entry

In `session_prompt.go` Prompt(), BEFORE building the system prompt,
load the most recent N=10 memories from the table. Inject as a
"Long-term memory (recent first):" block in the system prompt.

Render shape:
```
Long-term memory (recent first):
- [2026-05-18 03:14] User prefers Go tabs over spaces.
- [2026-05-18 02:50] auth module uses session_token, not access_token.
- ...
```

10 most-recent capped at the start so cache prefix stays stable.

### Defaults + caps

- Per-write: 500 char limit (enforced by tool).
- Per-prompt-render: 10 most-recent (configurable later via spec).
- No automatic expiration (table grows monotonically; CLI can prune).

### CLI

`gil memory list` — show all memories newest-first.
`gil memory rm <id>` — delete one.
`gil memory clear` — delete all (with --force).

Not in v1 if the schema + tool ships clean.

## Acceptance criteria

1. Schema migration v6 applies cleanly; older sessions unaffected.
2. `remember(text)` tool persists a row + returns success.
3. Long content (>500 chars) errors with a clear message.
4. Next chat Prompt loads 10 most-recent memories and includes
   them in the system prompt block.
5. Daemon restart preserves all memories.
6. Test coverage: schema, write, read, system-prompt inclusion,
   restart-survival, length-cap enforcement.

## Implementation steps

1. core/session/schema.go: add migration v6, bump currentSchemaVersion.
2. server/internal/service/session_prompt.go: add `recentMemories(ctx, db)`
   helper that returns []memory.
3. server/internal/service/agent_tools_memory.go (new): `toolRemember`
   implementation.
4. Wire toolRemember into the chat tool registry (buildChatToolRegistry).
5. session_prompt.go: in Prompt(), load recentMemories and prepend
   to systemPrompt construction.
6. session_prompt.go: update defaultChatSystemPrompt to mention the
   "Long-term memory" section + the remember tool.
7. Tests: schema, remember tool roundtrip, prompt-inclusion shape,
   restart hydration.

## Estimated scope

~250 LOC source + ~200 LOC tests. Single phase, single commit.
