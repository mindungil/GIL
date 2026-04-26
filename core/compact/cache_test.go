package compact

import (
	"testing"

	"github.com/jedutools/gil/core/provider"
	"github.com/stretchr/testify/require"
)

func mkMsgs(n int) []provider.Message {
	out := make([]provider.Message, n)
	for i := range out {
		out[i] = provider.Message{Role: provider.RoleUser, Content: "x"}
	}
	return out
}

func TestMarkCacheBreakpoints_LastThree(t *testing.T) {
	msgs := mkMsgs(8)
	MarkCacheBreakpoints(msgs)
	for i := 0; i < 5; i++ {
		require.False(t, msgs[i].CacheControl, "msg %d should not be marked", i)
	}
	for i := 5; i < 8; i++ {
		require.True(t, msgs[i].CacheControl, "msg %d should be marked", i)
	}
}

func TestMarkCacheBreakpoints_FewerThanThree_MarksAll(t *testing.T) {
	msgs := mkMsgs(2)
	MarkCacheBreakpoints(msgs)
	require.True(t, msgs[0].CacheControl)
	require.True(t, msgs[1].CacheControl)
}

func TestMarkCacheBreakpoints_ExactlyThree_MarksAll(t *testing.T) {
	msgs := mkMsgs(3)
	MarkCacheBreakpoints(msgs)
	for i := range msgs {
		require.True(t, msgs[i].CacheControl)
	}
}

func TestMarkCacheBreakpoints_Empty_NoCrash(t *testing.T) {
	msgs := []provider.Message{}
	out := MarkCacheBreakpoints(msgs)
	require.Empty(t, out)
}

func TestMarkCacheBreakpoints_Idempotent(t *testing.T) {
	msgs := mkMsgs(5)
	MarkCacheBreakpoints(msgs)
	MarkCacheBreakpoints(msgs) // call twice
	// Still only last 3 marked
	for i := 0; i < 2; i++ {
		require.False(t, msgs[i].CacheControl)
	}
	for i := 2; i < 5; i++ {
		require.True(t, msgs[i].CacheControl)
	}
}

func TestMarkCacheBreakpoints_PreviouslyMarkedAreCleared(t *testing.T) {
	msgs := mkMsgs(5)
	msgs[0].CacheControl = true // stale
	msgs[1].CacheControl = true // stale
	MarkCacheBreakpoints(msgs)
	require.False(t, msgs[0].CacheControl)
	require.False(t, msgs[1].CacheControl)
	require.True(t, msgs[3].CacheControl)
}
