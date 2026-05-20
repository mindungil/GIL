package service

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/stuck"
)

// chatAdversaryBudgetCap is the per-session max number of
// AdversaryConsult LLM calls. 1st-pass constant — telemetry from
// integration sweeps will inform the next iteration (see A1b spec
// "Trigger budget" + adversary_skipped_budget event).
const chatAdversaryBudgetCap = 5

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

	// patternFires counts how many times each Pattern has been dispatched
	// in this session. Drives P67i escalation: 1st fire of a pattern goes
	// to the cheap nudge strategy (AltToolOrder); 2nd+ fire means the
	// nudge already failed, so the dispatcher skips it and invokes
	// AdversaryConsult instead.
	patternFires map[stuck.Pattern]int
}

func newChatEventBuffer(c int) *chatEventBuffer {
	if c <= 0 {
		c = 200
	}
	return &chatEventBuffer{
		cap:          c,
		events:       make([]event.Event, 0, c),
		seenThisTurn: make(map[stuck.Pattern]bool),
		patternFires: make(map[stuck.Pattern]int),
	}
}

// incrementPatternFire bumps the per-session fire count for `p` and
// returns the new count. Used by chatStuckDispatcher.tick to escalate
// from AltToolOrder (cheap nudge) to AdversaryConsult (LLM-driven
// concrete suggestion) on 2nd+ occurrence — the nudge clearly didn't
// work or the agent would have changed behavior.
func (b *chatEventBuffer) incrementPatternFire(p stuck.Pattern) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.patternFires[p]++
	return b.patternFires[p]
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

// truncateForDetector clips a string at 512 bytes. The Detector's
// pattern checks hash the `content` field, so we keep enough bytes
// to distinguish observations without ballooning the ring buffer.
// Matches the cap in core/runner.truncateString.
func truncateForDetector(s string) string {
	const cap = 512
	if len(s) <= cap {
		return s
	}
	return s[:cap]
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

// chatHistoryToProviderMessages returns the trailing tail of the chat
// history as []provider.Message. The dispatcher passes this slice to
// AdversaryConsultStrategy so the adversary LLM has fresh context for
// its one-line suggestion. tail≤0 is treated as "no tail".
func chatHistoryToProviderMessages(msgs []provider.Message, tail int) []provider.Message {
	if tail <= 0 || len(msgs) <= tail {
		out := make([]provider.Message, len(msgs))
		copy(out, msgs)
		return out
	}
	out := make([]provider.Message, tail)
	copy(out, msgs[len(msgs)-tail:])
	return out
}

// chatStuckDispatcher runs Detector against the chat-side ring buffer
// after each tool_result push and routes each new signal through the
// configured strategy chain. AdversaryConsult is gated by riskAdv
// (empty = off) and a per-session budget cap; other strategies fire
// unconditionally. Pure dispatcher — caller emits Parts/events from
// the returned []Decision.
type chatStuckDispatcher struct {
	detector   *stuck.Detector
	strategies []stuck.Strategy
	provider   provider.Provider
	model      string
	riskAdv    string // empty disables AdversaryConsultStrategy
}

// tick walks the current Detector signals and produces zero or more
// Decisions. Mutates buf.seenThisTurn (per-turn dedup) and
// buf.adversaryCalls / buf.lastAdversaryIter (budget+cooldown).
// Recovers from strategy panics so the chat loop never dies.
func (d *chatStuckDispatcher) tick(ctx context.Context, buf *chatEventBuffer, recent []provider.Message) []stuck.Decision {
	if d == nil || d.detector == nil || buf == nil {
		return nil
	}
	defer func() { _ = recover() }()
	signals := d.detector.Check(buf.snapshot())
	if len(signals) == 0 {
		return nil
	}
	var decisions []stuck.Decision
	for _, sig := range signals {
		if !buf.markSeen(sig.Pattern) {
			continue
		}
		// P67i escalation: count this pattern's fires across the session.
		// fireCount >= 2 means the cheap nudge already fired once and
		// didn't change behavior — skip AltToolOrder and invoke
		// AdversaryConsult directly so the agent gets a concrete next-step
		// suggestion. The 2026-05-19 chess sweep showed AltToolOrder
		// firing twice in a row with no effect (turns 9-10 of r1), which
		// is exactly the failure mode this gate fixes.
		fireCount := buf.incrementPatternFire(sig.Pattern)
		escalate := fireCount >= 2

		for _, st := range d.strategies {
			if _, isAlt := st.(stuck.AltToolOrderStrategy); isAlt && escalate {
				continue
			}
			if _, isAdv := st.(stuck.AdversaryConsultStrategy); isAdv {
				if d.riskAdv == "" {
					continue
				}
				// Cooldown: at most one adversary call per user turn.
				if buf.adversaryCalls > 0 && buf.iter <= buf.lastAdversaryIter {
					continue
				}
				// Budget cap: emit a sentinel Decision the caller
				// converts into an `adversary_skipped_budget` event.
				if buf.adversaryCalls >= chatAdversaryBudgetCap {
					decisions = append(decisions, stuck.Decision{
						Action:      stuck.ActionAdversaryConsult,
						Explanation: "ADVERSARY_SKIPPED_BUDGET",
					})
					continue
				}
				buf.adversaryCalls++
				buf.lastAdversaryIter = buf.iter
			}
			dec, err := st.Apply(ctx, stuck.ApplyRequest{
				Signal:         sig,
				Provider:       d.provider,
				CurrentModel:   d.model,
				AdversaryModel: d.riskAdv,
				RecentMessages: recent,
			})
			if err != nil {
				// P67l telemetry: when the AdversaryConsult strategy was
				// actually invoked (passed opt-in / cooldown / budget) but
				// returned no Decision — empty LLM response, provider
				// error, etc. — surface a sentinel so the caller emits an
				// `adversary_consult_empty` event. Without this, the
				// chess r2 turn 5 case (Monologue fireCount=2 but no
				// adversary Part) was invisible in event logs.
				if _, isAdv := st.(stuck.AdversaryConsultStrategy); isAdv {
					decisions = append(decisions, stuck.Decision{
						Action:      stuck.ActionAdversaryConsult,
						Explanation: "ADVERSARY_EMPTY: " + err.Error(),
					})
				}
				continue // ErrNoFallback or transient — try next strategy
			}
			decisions = append(decisions, dec)
			break // first non-nil Decision per signal
		}
	}
	return decisions
}
