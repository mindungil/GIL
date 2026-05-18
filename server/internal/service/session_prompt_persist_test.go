package service

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// P34 — chatHistory persistence regression tests. Mirrors the
// agent_tools_verbs_persist_test.go shape: in-memory store wired to
// a real *sql.DB, mutate, "restart" by allocating a fresh store
// pointing at the same DB, assert hydration returns identical state.

func openTestChatHistoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chat.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newChatHistoryWithDB(db *sql.DB) *chatHistory {
	h := newChatHistory()
	h.SetDB(db)
	return h
}

func TestChatHistory_AppendPersistsThroughDB(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-a", provider.Message{Role: provider.RoleUser, Content: "hello"})
	h.append("sess-a", provider.Message{Role: provider.RoleAssistant, Content: "hi back"})

	// Simulate daemon restart: fresh store pointing at the same DB.
	revived := newChatHistoryWithDB(db)
	got := revived.get("sess-a")
	require.Len(t, got, 2)
	require.Equal(t, provider.RoleUser, got[0].Role)
	require.Equal(t, "hello", got[0].Content)
	require.Equal(t, provider.RoleAssistant, got[1].Role)
	require.Equal(t, "hi back", got[1].Content)
}

func TestChatHistory_ToolCallsRoundTrip(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-tc", provider.Message{
		Role:    provider.RoleAssistant,
		Content: "calling read_file",
		ToolCalls: []provider.ToolCall{
			{ID: "tc-1", Name: "read_file", Input: []byte(`{"path":"main.go"}`)},
		},
	})

	revived := newChatHistoryWithDB(db)
	got := revived.get("sess-tc")
	require.Len(t, got, 1)
	require.Equal(t, "calling read_file", got[0].Content)
	require.Len(t, got[0].ToolCalls, 1)
	require.Equal(t, "tc-1", got[0].ToolCalls[0].ID)
	require.Equal(t, "read_file", got[0].ToolCalls[0].Name)
	require.JSONEq(t, `{"path":"main.go"}`, string(got[0].ToolCalls[0].Input))
}

func TestChatHistory_ToolResultsRoundTrip(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-tr", provider.Message{
		Role:    provider.RoleUser,
		Content: "",
		ToolResults: []provider.ToolResult{
			{ToolUseID: "tc-1", Content: "package main\n", IsError: false},
			{ToolUseID: "tc-2", Content: "permission denied", IsError: true},
		},
	})

	revived := newChatHistoryWithDB(db)
	got := revived.get("sess-tr")
	require.Len(t, got, 1)
	require.Len(t, got[0].ToolResults, 2)
	require.Equal(t, "tc-1", got[0].ToolResults[0].ToolUseID)
	require.Equal(t, "package main\n", got[0].ToolResults[0].Content)
	require.False(t, got[0].ToolResults[0].IsError)
	require.Equal(t, "tc-2", got[0].ToolResults[1].ToolUseID)
	require.True(t, got[0].ToolResults[1].IsError)
}

func TestChatHistory_ResetWipesRows(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-reset", provider.Message{Role: provider.RoleUser, Content: "throw me away"})
	h.append("sess-reset", provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	require.Len(t, h.get("sess-reset"), 2)

	h.reset("sess-reset")

	// Both in-memory and disk should be empty after reset.
	require.Empty(t, h.get("sess-reset"))
	revived := newChatHistoryWithDB(db)
	require.Empty(t, revived.get("sess-reset"), "reset must wipe persisted rows so restart doesn't resurrect them")
}

func TestChatHistory_NoDBStillWorks(t *testing.T) {
	// Pre-P34 contract: when no DB is wired, append/get/reset behave as
	// the in-memory-only version. This is what test setups that don't
	// build a Repo (NewSessionService(nil, nil)) rely on.
	h := newChatHistory()
	h.append("sess-mem", provider.Message{Role: provider.RoleUser, Content: "only mem"})
	require.Len(t, h.get("sess-mem"), 1)
	h.reset("sess-mem")
	require.Empty(t, h.get("sess-mem"))
}

func TestChatHistory_PerSessionIsolation(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-x", provider.Message{Role: provider.RoleUser, Content: "X"})
	h.append("sess-y", provider.Message{Role: provider.RoleUser, Content: "Y"})

	revived := newChatHistoryWithDB(db)
	gx := revived.get("sess-x")
	gy := revived.get("sess-y")
	require.Len(t, gx, 1)
	require.Equal(t, "X", gx[0].Content)
	require.Len(t, gy, 1)
	require.Equal(t, "Y", gy[0].Content)

	// Reset of one session leaves the other untouched.
	revived.reset("sess-x")
	require.Empty(t, revived.get("sess-x"))
	require.Len(t, revived.get("sess-y"), 1)
}

func TestChatHistory_SeqOrderPreservedAcrossRestart(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	// Append 10 messages with distinguishable content; assert revived
	// store returns them in exactly the same order.
	roles := []provider.Role{provider.RoleUser, provider.RoleAssistant}
	for i := 0; i < 10; i++ {
		h.append("sess-seq", provider.Message{
			Role:    roles[i%2],
			Content: string(rune('a' + i)),
		})
	}

	revived := newChatHistoryWithDB(db)
	got := revived.get("sess-seq")
	require.Len(t, got, 10)
	for i := 0; i < 10; i++ {
		require.Equal(t, roles[i%2], got[i].Role, "msg %d role drift", i)
		require.Equal(t, string(rune('a'+i)), got[i].Content, "msg %d content drift", i)
	}
}

func TestChatHistory_ConcurrentAppendsHaveDistinctSeqs(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			h.append("sess-concurrent", provider.Message{
				Role:    provider.RoleUser,
				Content: string(rune('a' + i)),
			})
		}(i)
	}
	wg.Wait()

	// All 16 appends must land — append holds h.mu so seq is monotonic
	// per session even under contention. If any append had collided on
	// the PK (session_id, seq), the row count below would be < N.
	revived := newChatHistoryWithDB(db)
	got := revived.get("sess-concurrent")
	require.Len(t, got, N, "concurrent appends lost messages; seq is not monotonic")
}

func TestChatHistory_HydrationIsOnceUntilSetDB(t *testing.T) {
	db := openTestChatHistoryDB(t)
	h := newChatHistoryWithDB(db)

	h.append("sess-hydrate", provider.Message{Role: provider.RoleUser, Content: "first"})

	revived := newChatHistoryWithDB(db)
	// First get hydrates from DB.
	require.Len(t, revived.get("sess-hydrate"), 1)
	// Second get returns the same in-memory slice; no SELECT issued.
	require.Len(t, revived.get("sess-hydrate"), 1)
	// Append after hydration should land at seq=1 (not 0); a duplicate
	// PK would panic, so success means seq computed off the hydrated len.
	revived.append("sess-hydrate", provider.Message{Role: provider.RoleAssistant, Content: "second"})
	require.Len(t, revived.get("sess-hydrate"), 2)
	// Re-revive and confirm both rows landed in order.
	revived2 := newChatHistoryWithDB(db)
	got := revived2.get("sess-hydrate")
	require.Len(t, got, 2)
	require.Equal(t, "first", got[0].Content)
	require.Equal(t, "second", got[1].Content)
}
