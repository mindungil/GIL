package compact

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jedutools/gil/core/provider"
	"github.com/stretchr/testify/require"
)

func TestCompactor_NoOp_WhenMiddleTooSmall(t *testing.T) {
	// 10 msgs; headKeep=2 + tailKeep=6 leaves middle=2 < minMiddle=8
	msgs := makeMessages(10)
	c := &Compactor{Provider: provider.NewMock(nil), Model: "m"}
	out, res, err := c.Compact(context.Background(), msgs)
	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.Equal(t, 10, len(out))
	require.Equal(t, 10, res.OriginalCount)
	require.Equal(t, 10, res.CompactedCount)
}

func TestCompactor_PreservesHeadAndTail_Verbatim(t *testing.T) {
	// middle = 12 — will compact
	msgs := makeMessages(20)
	mock := provider.NewMock([]string{"## Goal\n- summary"})
	c := &Compactor{Provider: mock, Model: "m"}
	out, res, err := c.Compact(context.Background(), msgs)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	// head = first 2 messages
	require.Equal(t, msgs[0].Content, out[0].Content)
	require.Equal(t, msgs[1].Content, out[1].Content)
	// synthetic summary at index 2
	require.Equal(t, "## Goal\n- summary", out[2].Content)
	require.Equal(t, provider.RoleUser, out[2].Role)
	// tail = last 6 messages of original
	for i := 0; i < 6; i++ {
		require.Equal(t, msgs[20-6+i].Content, out[3+i].Content)
	}
	require.Equal(t, 9, res.CompactedCount) // 2 + 1 + 6
	require.Equal(t, 20, res.OriginalCount)
	require.Greater(t, res.SavedTokens, int64(0))
}

func TestCompactor_OriginalSliceUnmutated(t *testing.T) {
	msgs := makeMessages(20)
	snapshotContent := append([]string(nil), contentsOf(msgs)...)
	mock := provider.NewMock([]string{"summary"})
	c := &Compactor{Provider: mock, Model: "m"}
	_, _, err := c.Compact(context.Background(), msgs)
	require.NoError(t, err)
	// Original slice unchanged
	for i, want := range snapshotContent {
		require.Equal(t, want, msgs[i].Content)
	}
}

func TestCompactor_CustomKeeps(t *testing.T) {
	msgs := makeMessages(15)
	mock := provider.NewMock([]string{"summary"})
	c := &Compactor{Provider: mock, Model: "m", HeadKeep: 3, TailKeep: 4, MinMiddle: 4}
	// middle = 15 - 3 - 4 = 8 >= 4 -> compacts
	out, res, err := c.Compact(context.Background(), msgs)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Equal(t, 8, len(out)) // 3 + 1 + 4
}

func TestCompactor_ProviderError_PropagatesWithContext(t *testing.T) {
	msgs := makeMessages(20)
	failProv := &errProvider{err: errors.New("boom")}
	c := &Compactor{Provider: failProv, Model: "m"}
	_, _, err := c.Compact(context.Background(), msgs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "compact summary")
	require.Contains(t, err.Error(), "boom")
}

// makeMessages returns N messages alternating user/assistant roles with distinct content.
func makeMessages(n int) []provider.Message {
	out := make([]provider.Message, n)
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		out[i] = provider.Message{Role: role, Content: fmt.Sprintf("msg-%d", i)}
	}
	return out
}

func contentsOf(msgs []provider.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

// errProvider returns the configured error from Complete.
type errProvider struct{ err error }

func (e *errProvider) Name() string { return "err" }
func (e *errProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	return provider.Response{}, e.err
}
