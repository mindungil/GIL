package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// P55 — cross-session memory bank tests.

func openTestMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mem.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestToolRemember_StoresContent(t *testing.T) {
	db := openTestMemoryDB(t)
	tool := &toolRemember{db: db}
	res, err := tool.run(context.Background(), "sess-mem-1",
		json.RawMessage(`{"content":"user prefers tabs over spaces"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "remembered.", res.Content)

	// Verify the row landed.
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM session_memories WHERE session_id = ?`, "sess-mem-1").Scan(&count))
	require.Equal(t, 1, count)
}

func TestToolRemember_RejectsEmpty(t *testing.T) {
	db := openTestMemoryDB(t)
	tool := &toolRemember{db: db}
	res, _ := tool.run(context.Background(), "sess", json.RawMessage(`{"content":"   "}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "cannot be empty")
}

func TestToolRemember_RejectsTooLong(t *testing.T) {
	db := openTestMemoryDB(t)
	tool := &toolRemember{db: db}
	tooLong := strings.Repeat("x", memoryMaxContent+1)
	res, _ := tool.run(context.Background(), "sess",
		json.RawMessage(`{"content":"`+tooLong+`"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "too long")
	require.Contains(t, res.Content, "501") // length number echoes
}

func TestToolRemember_NoDBSilentlyAccepts(t *testing.T) {
	// Test setups without a wired DB should not panic. The tool
	// returns a "noted (no durable storage wired)" success message so
	// the chat loop doesn't error.
	tool := &toolRemember{db: nil}
	res, err := tool.run(context.Background(), "sess",
		json.RawMessage(`{"content":"keep it"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "noted")
}

func TestLoadRecentMemories_OrdersNewestFirst(t *testing.T) {
	db := openTestMemoryDB(t)
	// Insert 3 memories with explicit timestamps so order is deterministic.
	_, err := db.Exec(`INSERT INTO session_memories (session_id, content, created_at) VALUES
		('s1', 'oldest', '2026-05-01 00:00:00'),
		('s2', 'middle', '2026-05-10 00:00:00'),
		('s3', 'newest', '2026-05-17 00:00:00')`)
	require.NoError(t, err)

	got := loadRecentMemories(context.Background(), db)
	require.Len(t, got, 3)
	require.Equal(t, "newest", got[0].Content, "first must be newest")
	require.Equal(t, "middle", got[1].Content)
	require.Equal(t, "oldest", got[2].Content)
}

func TestLoadRecentMemories_CapsAtMemoryRecentN(t *testing.T) {
	db := openTestMemoryDB(t)
	for i := 0; i < memoryRecentN+5; i++ {
		_, err := db.Exec(`INSERT INTO session_memories (session_id, content) VALUES (?, ?)`,
			"s", "memory-"+string(rune('A'+i)))
		require.NoError(t, err)
	}
	got := loadRecentMemories(context.Background(), db)
	require.Len(t, got, memoryRecentN,
		"loadRecentMemories must cap at memoryRecentN, not return all rows")
}

func TestRenderMemoriesForPrompt_EmptyReturnsEmpty(t *testing.T) {
	require.Equal(t, "", renderMemoriesForPrompt(nil))
	require.Equal(t, "", renderMemoriesForPrompt([]memoryEntry{}))
}

func TestRenderMemoriesForPrompt_FormatHasLongTermHeaderAndLines(t *testing.T) {
	mems := []memoryEntry{
		{ID: 2, SessionID: "s2", Content: "newer note"},
		{ID: 1, SessionID: "s1", Content: "older note"},
	}
	out := renderMemoriesForPrompt(mems)
	require.Contains(t, out, "Long-term memory (recent first")
	require.Contains(t, out, "newer note")
	require.Contains(t, out, "older note")
	// Each memory line starts with "- [".
	require.Equal(t, 2, strings.Count(out, "\n- ["),
		"each memory must render as one '- [...]' line")
}

func TestRenderMemoriesForPrompt_TruncatesOverlongContent(t *testing.T) {
	// Direct DB writes can sneak past the write-time 500 char cap;
	// the renderer truncates aggressively to keep the system prompt
	// bounded.
	long := strings.Repeat("y", memoryMaxContent+200)
	out := renderMemoriesForPrompt([]memoryEntry{{Content: long}})
	require.Contains(t, out, "…", "overlong content must be ellipsized")
	// The "y" count after the truncation marker should be at most
	// memoryMaxContent + a few format chars. Asserts that the renderer
	// CUT the content (not that it passed through verbatim — 700+ y's
	// would fail this hard).
	require.Less(t, strings.Count(out, "y"), memoryMaxContent+50,
		"renderer must truncate overlong content; saw %d y's", strings.Count(out, "y"))
}

func TestRenderMemoriesForPrompt_NewlinesCollapsed(t *testing.T) {
	out := renderMemoriesForPrompt([]memoryEntry{
		{Content: "line one\nline two"},
	})
	// Inside the memory line, the embedded \n must be replaced with " "
	// so the line stays on one row (the surrounding format is line-based).
	// The trailing "\n" terminator on each rendered line is fine.
	require.NotContains(t, out, "line one\nline two")
	require.Contains(t, out, "line one line two")
}

// TestPromptSystemPromptInclusion is the end-to-end check: a Prompt
// with a recently-stored memory must see that memory in the agent's
// system prompt. Hard to assert directly without a mock provider that
// captures Request.System, so we use the in-process service + a fake
// provider that records what it received.
func TestPromptSystemPromptInclusion_MemoryAppears(t *testing.T) {
	// Pre-seed a memory directly via DB; then call Prompt and capture
	// the system prompt the provider saw.
	repo := newTestRepo(t)
	wd := t.TempDir()
	sess, err := repo.Create(context.Background(), session.CreateInput{WorkingDir: wd})
	require.NoError(t, err)

	_, err = repo.DB().Exec(`INSERT INTO session_memories (session_id, content) VALUES
		(?, 'TEST_MEMORY_TOKEN_XYZ123')`, "any-session")
	require.NoError(t, err)

	capturing := &capturingProvider{}
	factory := func(name string) (provider.Provider, string, error) {
		return capturing, "mock-model", nil
	}
	svc := NewSessionService(repo, nil).WithProviderFactory(factory)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sess.ID, "hello"), stream)

	require.NotEmpty(t, capturing.systemSeen)
	require.Contains(t, capturing.systemSeen, "TEST_MEMORY_TOKEN_XYZ123",
		"system prompt must include the pre-seeded memory; got prefix: %q", capturing.systemSeen[:min(200, len(capturing.systemSeen))])
}

func TestPromptSystemPromptInclusion_GoalHintAppears(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sess, err := repo.Create(context.Background(), session.CreateInput{
		WorkingDir: wd,
		GoalHint:   "add a pinned goal reminder",
	})
	require.NoError(t, err)

	capturing := &capturingProvider{}
	factory := func(name string) (provider.Provider, string, error) {
		return capturing, "mock-model", nil
	}
	svc := NewSessionService(repo, nil).WithProviderFactory(factory)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sess.ID, "hello"), stream)

	require.NotEmpty(t, capturing.systemSeen)
	require.Contains(t, capturing.systemSeen, "## Session Goal")
	require.Contains(t, capturing.systemSeen, "add a pinned goal reminder")
	require.Contains(t, capturing.systemSeen, "compare the current diff and behavior against this pinned goal")
}

// capturingProvider records the System string from each Request so
// the test can assert on what the agent saw. Returns a trivial
// end_turn response.
type capturingProvider struct {
	systemSeen string
}

func (p *capturingProvider) Name() string { return "capturing" }
func (p *capturingProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	p.systemSeen = req.System
	return provider.Response{Text: "ok", StopReason: "end_turn"}, nil
}

// StreamComplete satisfies the P68c streaming Provider interface;
// memory-block tests only assert system-prompt capture, so the
// streaming callback is a no-op.
func (p *capturingProvider) StreamComplete(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	return p.Complete(ctx, req)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
