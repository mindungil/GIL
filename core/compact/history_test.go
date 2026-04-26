package compact

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistory_ShouldSkip_FalseWhenLessThanTwoEvents(t *testing.T) {
	h := &History{}
	require.False(t, h.ShouldSkip())
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 50})
	require.False(t, h.ShouldSkip(), "1 event isn't enough signal")
}

func TestHistory_ShouldSkip_FalseWhenLastTwoSavedOver10Pct(t *testing.T) {
	h := &History{}
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 200}) // 20%
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 300}) // 30%
	require.False(t, h.ShouldSkip())
}

func TestHistory_ShouldSkip_TrueWhenLastTwoBothUnder10Pct(t *testing.T) {
	h := &History{}
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 50}) // 5%
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 80}) // 8%
	require.True(t, h.ShouldSkip())
}

func TestHistory_ShouldSkip_FalseWhenOnlyLastIsLow(t *testing.T) {
	h := &History{}
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 500}) // 50%
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 50})  // 5%
	require.False(t, h.ShouldSkip(), "only one of the last two below threshold")
}

func TestHistory_ShouldSkip_OnlyLooksAtMostRecentTwo(t *testing.T) {
	h := &History{}
	// Old high-savings events
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 500}) // 50%
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 500}) // 50%
	// Recent low-savings — should still trigger
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 30}) // 3%
	h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: 70}) // 7%
	require.True(t, h.ShouldSkip())
}

func TestHistory_Record_TrimsToMaxRecent(t *testing.T) {
	h := &History{}
	for i := 0; i < MaxRecent+3; i++ {
		h.Record(CompactionEvent{OriginalTokens: 1000, SavedTokens: int64(i)})
	}
	snap := h.Snapshot()
	require.Equal(t, MaxRecent, len(snap))
	// Newest preserved
	require.Equal(t, int64(MaxRecent+2), snap[len(snap)-1].SavedTokens)
}

func TestHistory_ZeroOriginalTokens_TreatedAsZeroPct(t *testing.T) {
	h := &History{}
	h.Record(CompactionEvent{OriginalTokens: 0, SavedTokens: 100})
	h.Record(CompactionEvent{OriginalTokens: 0, SavedTokens: 100})
	require.True(t, h.ShouldSkip(), "0 original ÷ anything = 0% which is below threshold")
}

func TestHistory_ConcurrentAccess(t *testing.T) {
	h := &History{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.Record(CompactionEvent{OriginalTokens: 100, SavedTokens: int64(i), Timestamp: time.Now()})
			_ = h.ShouldSkip()
			_ = h.Snapshot()
		}(i)
	}
	wg.Wait()
	require.Len(t, h.Snapshot(), MaxRecent)
}
