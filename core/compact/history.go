package compact

import (
	"sync"
	"time"
)

// CompactionEvent records one Compact invocation's outcome.
type CompactionEvent struct {
	OriginalTokens int64
	SavedTokens    int64
	Timestamp      time.Time
}

// History tracks recent compactions for anti-thrashing decisions.
// Concurrent-safe via internal mutex.
type History struct {
	mu     sync.Mutex
	Recent []CompactionEvent // newest last; trimmed to MaxRecent
}

// MaxRecent is the cap on retained events.
const MaxRecent = 5

// Record appends an event, trimming to MaxRecent.
func (h *History) Record(e CompactionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Recent = append(h.Recent, e)
	if len(h.Recent) > MaxRecent {
		h.Recent = h.Recent[len(h.Recent)-MaxRecent:]
	}
}

// ShouldSkip returns true when anti-thrashing predicts low value:
// the LAST TWO recorded events both saved less than 10% of their respective
// OriginalTokens. With fewer than 2 events, returns false (no signal).
//
// Thrashing example: last two compactions saved 5% and 8% → ShouldSkip=true.
// Healthy example:   last two saved 30% and 45%             → ShouldSkip=false.
func (h *History) ShouldSkip() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.Recent)
	if n < 2 {
		return false
	}
	last := h.Recent[n-1]
	prev := h.Recent[n-2]
	return savedPct(last) < 10 && savedPct(prev) < 10
}

// Snapshot returns a copy of the recent events for inspection (e.g., events
// emitted by the runner). Safe for concurrent reads.
func (h *History) Snapshot() []CompactionEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]CompactionEvent, len(h.Recent))
	copy(out, h.Recent)
	return out
}

func savedPct(e CompactionEvent) float64 {
	if e.OriginalTokens <= 0 {
		return 0
	}
	return float64(e.SavedTokens) * 100.0 / float64(e.OriginalTokens)
}
