package service

import (
	"encoding/json"
	"sync"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/stuck"
)

// chatEventBuffer is a per-session ring of recent chat-loop events,
// fed into core/stuck/Detector. Bounded and in-memory only — daemon
// restart loses history (acceptable; see A1b spec Non-goals). Cap
// defaults to 200 (~10 turns at chess tool density).
type chatEventBuffer struct {
	mu                sync.Mutex
	cap               int
	events            []event.Event
	iter              int
	seenThisTurn      map[stuck.Pattern]bool
	adversaryCalls    int
	lastAdversaryIter int
}

func newChatEventBuffer(c int) *chatEventBuffer {
	if c <= 0 {
		c = 200
	}
	return &chatEventBuffer{
		cap:          c,
		events:       make([]event.Event, 0, c),
		seenThisTurn: make(map[stuck.Pattern]bool),
	}
}

func (b *chatEventBuffer) push(e event.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) >= b.cap {
		copy(b.events, b.events[1:])
		b.events = b.events[:len(b.events)-1]
	}
	b.events = append(b.events, e)
}

func (b *chatEventBuffer) snapshot() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]event.Event, len(b.events))
	copy(out, b.events)
	return out
}

// resetTurn increments the iter counter and clears per-turn dedup
// state. Called at the top of each chat Prompt() handler.
func (b *chatEventBuffer) resetTurn() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.iter++
	for k := range b.seenThisTurn {
		delete(b.seenThisTurn, k)
	}
}

// markSeen records that the given pattern has been processed within
// the current turn. Returns true if this is the first time this turn
// (caller should proceed with strategy dispatch); false if duplicate.
func (b *chatEventBuffer) markSeen(p stuck.Pattern) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seenThisTurn[p] {
		return false
	}
	b.seenThisTurn[p] = true
	return true
}

// jsonMust marshals v; on error returns "{}" so emit sites never
// panic. Pure helper.
func jsonMust(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
