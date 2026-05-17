package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mindungil/gil/core/provider"
)

// agent_tools_memory.go — P55 cross-session memory bank surface.
// The chat agent can persist short notes that survive session
// boundaries (and daemon restart) so it learns from prior work.
// Backed by the session_memories table (schema v6).

// memoryMaxContent caps a single remember() write. Keeps the
// long-term memory section in the system prompt bounded.
const memoryMaxContent = 500

// memoryRecentN is the number of most-recent memories surfaced
// into the chat agent's system prompt on each Prompt entry.
const memoryRecentN = 10

// memoryEntry is one row from session_memories. Matches the
// schema's columns; created_at is the wall time used for sort + display.
type memoryEntry struct {
	ID        int64
	SessionID string
	Content   string
	CreatedAt time.Time
}

// loadRecentMemories returns the most-recent memoryRecentN memories
// across all sessions, ordered newest first. Best-effort: a missing
// db or query error returns nil + nil so the chat path continues
// without long-term memory rather than failing the whole turn.
func loadRecentMemories(ctx context.Context, db *sql.DB) []memoryEntry {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, content, created_at
        FROM session_memories ORDER BY created_at DESC LIMIT ?`, memoryRecentN)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []memoryEntry
	for rows.Next() {
		var m memoryEntry
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Content, &m.CreatedAt); err != nil {
			return nil
		}
		out = append(out, m)
	}
	return out
}

// renderMemoriesForPrompt formats memories as a compact block for
// inclusion in the chat agent's system prompt. Empty when no
// memories yet; the caller skips the section entirely in that case.
func renderMemoriesForPrompt(memories []memoryEntry) string {
	if len(memories) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Long-term memory (recent first, across prior sessions):\n")
	for _, m := range memories {
		// One line per memory. Truncate aggressively if any single
		// memory snuck past the write-time cap (e.g. inserted via
		// direct SQL).
		content := m.Content
		if len(content) > memoryMaxContent {
			content = content[:memoryMaxContent] + "…"
		}
		content = strings.ReplaceAll(content, "\n", " ")
		sb.WriteString(fmt.Sprintf("- [%s] %s\n",
			m.CreatedAt.UTC().Format("2006-01-02 15:04"), content))
	}
	sb.WriteString("\n")
	return sb.String()
}

// toolRemember is the chat-agent-callable tool that writes a single
// memory row. Length is enforced; over-limit content errors with a
// clear message so the agent can retry with a shorter version.
type toolRemember struct {
	db *sql.DB
}

func (t *toolRemember) name() string { return "remember" }

func (t *toolRemember) description() string {
	return "Persist a short note (≤500 chars) into the cross-session " +
		"memory bank. Recent memories are surfaced to the next chat " +
		"session's system prompt automatically. Use for: project facts " +
		"you've learned (this codebase uses X not Y), failed approaches " +
		"(tried Z, broke because W), user preferences (user prefers " +
		"tabs to spaces), or non-obvious gotchas. Do NOT use for: " +
		"session-specific state (the chat history covers that), large " +
		"content (errors over 500 chars; the agent should distill)."
}

func (t *toolRemember) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {"type": "string", "description": "Short note to remember (≤500 chars)"}
		},
		"required": ["content"]
	}`)
}

func (t *toolRemember) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "remember: invalid args: " + err.Error(), IsError: true}, nil
	}
	args.Content = strings.TrimSpace(args.Content)
	if args.Content == "" {
		return provider.ToolResult{Content: "remember: content cannot be empty", IsError: true}, nil
	}
	if len(args.Content) > memoryMaxContent {
		return provider.ToolResult{
			Content: fmt.Sprintf("remember: content too long (%d > %d chars). Distill to the key fact.", len(args.Content), memoryMaxContent),
			IsError: true,
		}, nil
	}
	if t.db == nil {
		// In-memory test setups without a DB: silently accept so
		// existing tests don't crash. The memory is "remembered" only
		// in the LLM's current context, not persisted.
		return provider.ToolResult{Content: "remember: noted (no durable storage wired)"}, nil
	}
	_, err := t.db.ExecContext(ctx, `INSERT INTO session_memories
        (session_id, content) VALUES (?, ?)`, sessionID, args.Content)
	if err != nil {
		return provider.ToolResult{Content: "remember: write failed: " + err.Error(), IsError: true}, nil
	}
	return provider.ToolResult{Content: "remembered."}, nil
}
