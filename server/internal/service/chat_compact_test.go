package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"database/sql"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// P35 — chat history compaction.
// These tests cover the trigger logic + behavior preservation;
// integration with the full Prompt path is exercised by the existing
// session_prompt_test.go (which still passes verbatim post-P35 wiring,
// since compactor.Compact on a short history is a no-op).

func TestCompactChat_NilProvider_NoOp(t *testing.T) {
	// Defensive: nil prov shouldn't panic. Pre-P35 contract for tests
	// that bypass the provider factory.
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	}
	out, didCompact, err := compactChatIfNeeded(context.Background(), "mock", "mock-1", nil, msgs)
	require.NoError(t, err)
	require.False(t, didCompact)
	require.Equal(t, msgs, out)
}

func TestCompactChat_EmptyMessages_NoOp(t *testing.T) {
	prov := provider.NewMock([]string{"unused"})
	out, didCompact, err := compactChatIfNeeded(context.Background(), "mock", "mock-1", prov, nil)
	require.NoError(t, err)
	require.False(t, didCompact)
	require.Empty(t, out)
}

func TestCompactChat_BelowThreshold_NoOp(t *testing.T) {
	// Three short messages — token estimate is tiny, far below 95% of
	// the fallback 200k context window. Mock provider has no responses
	// configured because we expect Complete to never be called.
	prov := provider.NewMock(nil)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "first turn"},
		{Role: provider.RoleAssistant, Content: "first reply"},
		{Role: provider.RoleUser, Content: "second turn"},
	}
	out, didCompact, err := compactChatIfNeeded(context.Background(), "mock", "mock-1", prov, msgs)
	require.NoError(t, err)
	require.False(t, didCompact)
	require.Equal(t, msgs, out)
}

// TestCompactChat_AboveThreshold_CompactsAndReplaces builds a synthetic
// long history that crosses the 95%-of-window threshold for a tiny
// fake model, then verifies the compactor fires and the returned
// slice has fewer messages (Hermes head+summary+tail shape).
func TestCompactChat_AboveThreshold_CompactsAndReplaces(t *testing.T) {
	// Use a model name that resolves through provider.ContextTokens to
	// a smaller window so the synthetic history can exceed 95% without
	// allocating gigabytes of test data.
	const model = "test-tiny-window"
	prov := provider.NewMock([]string{"<<summary of earlier turns>>"})

	// 20 messages with ~10k chars each → ~200k chars total. With
	// fallback context window 200k tokens and the 4-chars-per-token
	// heuristic, this lands around 50k estimated tokens — below
	// threshold for the fallback. So we use a much shorter window: we
	// shadow the threshold via a model name not in the table and rely
	// on the fact that we'll cross threshold with a LOT of messages.
	// Simpler: pump enough text that 4-chars/token-equivalent crosses
	// 0.95 * 200_000 = 190_000 tokens → ~760_000 chars total.
	bigChunk := strings.Repeat("x ", 5000) // 10000 chars
	var msgs []provider.Message
	for i := 0; i < 80; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		msgs = append(msgs, provider.Message{Role: role, Content: bigChunk})
	}

	out, didCompact, err := compactChatIfNeeded(context.Background(), "mock", model, prov, msgs)
	require.NoError(t, err)
	require.True(t, didCompact, "synthetic 800k-char history should cross 95% threshold")
	require.Less(t, len(out), len(msgs), "compaction must reduce message count")
	// Hermes layout: head (2) + summary (1) + tail (6) = 9 by default.
	require.Equal(t, 9, len(out))
	// The synthesized summary message uses the mock's scripted response.
	// Find it among the output messages — Compactor places it between
	// head and tail.
	foundSummary := false
	for _, m := range out {
		if m.Content == "<<summary of earlier turns>>" {
			foundSummary = true
			break
		}
	}
	require.True(t, foundSummary, "compacted output must include the LLM-generated summary")
}

func TestCompactChat_QwenVLLMUsesLocalContextWindow(t *testing.T) {
	prov := provider.NewMock([]string{"<<qwen summary>>"})
	bigChunk := strings.Repeat("q", 10_000)
	var msgs []provider.Message
	for i := 0; i < 20; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		msgs = append(msgs, provider.Message{Role: role, Content: bigChunk})
	}

	out, didCompact, err := compactChatIfNeeded(context.Background(), "vllm", "qwen3.6-27b", prov, msgs)
	require.NoError(t, err)
	require.True(t, didCompact, "vllm/qwen3.6-27b must compact near the local 32k window, not the 200k cloud fallback")
	require.Equal(t, 9, len(out))
}

func TestCompactChat_ProviderError_PreservesOriginal(t *testing.T) {
	// errProvider always errors; compactor surfaces it. The caller in
	// session_prompt.go reads err and keeps msgs unchanged.
	prov := &errProviderForCompact{err: errors.New("simulated rate limit")}
	bigChunk := strings.Repeat("y ", 5000)
	var msgs []provider.Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: bigChunk})
	}
	out, didCompact, err := compactChatIfNeeded(context.Background(), "mock", "test-tiny", prov, msgs)
	require.Error(t, err, "compactor error must surface to caller")
	require.False(t, didCompact)
	require.Equal(t, len(msgs), len(out), "msgs must be unchanged on error")
}

type errProviderForCompact struct{ err error }

func (e *errProviderForCompact) Name() string { return "err-mock" }
func (e *errProviderForCompact) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{}, e.err
}

func (e *errProviderForCompact) StreamComplete(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	return e.Complete(ctx, req)
}

// TestChatHistory_NextSeq_AvoidsCollisionAfterCompaction is the seq-counter
// regression check the P35 design called out: ReplaceInMemory shortens the
// in-memory slice; the next append must NOT use len() as the seq, because
// that would collide with an existing PK on disk. nextSeqLocked reads
// MAX(seq) from DB instead.
func TestChatHistory_NextSeq_AvoidsCollisionAfterCompaction(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "compact.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	defer db.Close()

	h := newChatHistory()
	h.SetDB(db)

	// Stuff 20 messages so the DB has seq 0..19.
	for i := 0; i < 20; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		h.append("sess-compact", provider.Message{Role: role, Content: "msg"})
	}
	require.Len(t, h.get("sess-compact"), 20)

	// Simulate compaction: replace in-memory list with 3 messages (head
	// + summary + tail mockup).
	compacted := []provider.Message{
		{Role: provider.RoleUser, Content: "head"},
		{Role: provider.RoleUser, Content: "<summary>"},
		{Role: provider.RoleAssistant, Content: "tail"},
	}
	h.ReplaceInMemory("sess-compact", compacted)
	require.Len(t, h.get("sess-compact"), 3)

	// Append a new message. If nextSeqLocked used len(in-memory)=3,
	// the INSERT would collide on PK (3 already exists). The DB-aware
	// path returns 20 (MAX(seq)+1), so the new row lands at seq=20.
	h.append("sess-compact", provider.Message{Role: provider.RoleUser, Content: "post-compaction"})

	// In-memory length grew by 1.
	require.Len(t, h.get("sess-compact"), 4)

	// DB now has 21 rows (0..19 original + 20 new); no PK collision.
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE session_id = ?`, "sess-compact").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 21, count, "append after compaction must land at seq=20, not collide at seq=3")
}

func TestChatHistory_ReplaceInMemory_SuppressesReHydrate(t *testing.T) {
	// After ReplaceInMemory, the loaded flag stays set so a subsequent
	// get() returns the compacted slice — does NOT re-hydrate the full
	// log from DB (which would silently undo the compaction).
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "noreload.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	defer db.Close()

	h := newChatHistory()
	h.SetDB(db)
	h.append("sess-noreload", provider.Message{Role: provider.RoleUser, Content: "one"})
	h.append("sess-noreload", provider.Message{Role: provider.RoleUser, Content: "two"})
	h.append("sess-noreload", provider.Message{Role: provider.RoleUser, Content: "three"})

	h.ReplaceInMemory("sess-noreload", []provider.Message{
		{Role: provider.RoleAssistant, Content: "compacted summary"},
	})

	got := h.get("sess-noreload")
	require.Len(t, got, 1)
	require.Equal(t, "compacted summary", got[0].Content)
}
