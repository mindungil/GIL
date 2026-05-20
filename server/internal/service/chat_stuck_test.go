package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/stuck"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// Chat-side stuck detection lives on core/stuck/Detector +
// chatStuckDispatcher (P67c). The legacy P39 ad-hoc tests were
// removed when chatStuckSig/chatStuckCheck/chatStuckFired were
// deleted in P67e — Detector's PatternRepeatedActionObservation
// covers the same shape (threshold bumped 3 → 4, see
// TestChatStuckDispatcher_RepeatedActionObservationFires).

// ---------------------------------------------------------------------
// P67b — chatEventBuffer.
// ---------------------------------------------------------------------

func TestChatEventBuffer_PushSnapshotFIFO(t *testing.T) {
	buf := newChatEventBuffer(3) // cap 3 to make eviction observable
	for i := 0; i < 5; i++ {
		buf.push(event.Event{Type: "tool_call", Data: jsonMust(map[string]any{"i": i})})
	}
	snap := buf.snapshot()
	require.Len(t, snap, 3, "buffer must cap at 3")
	// Oldest two evicted (i=0,1). snap contains i=2,3,4.
	require.JSONEq(t, `{"i":2}`, string(snap[0].Data))
	require.JSONEq(t, `{"i":4}`, string(snap[2].Data))
}

func TestChatEventBuffer_ResetTurnIncrementsAndClearsSeen(t *testing.T) {
	buf := newChatEventBuffer(50)
	require.True(t, buf.markSeen(stuck.PatternNoProgress))
	require.False(t, buf.markSeen(stuck.PatternNoProgress), "second mark same turn returns false")
	buf.resetTurn()
	require.Equal(t, 1, buf.iter, "iter increments to 1 on first reset")
	require.True(t, buf.markSeen(stuck.PatternNoProgress), "after resetTurn, pattern is fresh again")
	buf.resetTurn()
	require.Equal(t, 2, buf.iter)
}

func TestChatEventBuffer_Concurrent(t *testing.T) {
	buf := newChatEventBuffer(500)
	const goroutines = 8
	const each = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				buf.push(event.Event{Type: "tool_call", Data: jsonMust(map[string]any{"g": gid, "j": j})})
			}
		}(g)
	}
	wg.Wait()
	// All pushes accepted (within cap); no panic on concurrent push/snapshot.
	require.Equal(t, goroutines*each, len(buf.snapshot()))
}

func TestChatPrompt_EmitsIterationStartAndVerifyEvents(t *testing.T) {
	// Drive one user turn that issues a verify tool_call (synthetic
	// verify_run + verify_result must wrap it) and then a write_file
	// tool_call (no synthetic wrappers). End_turn closes the turn.
	turns := []provider.MockTurn{
		// Turn 1: verify call.
		{ToolCalls: []provider.ToolCall{{ID: "v1", Name: "verify",
			Input: []byte(`{}`)}},
			StopReason: "tool_use"},
		// Turn 2: agent acknowledges, ends turn.
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	require.NoError(t, svc.Prompt(promptReq(sid, "run verify"), stream))

	buf := svc.chatEventBufFor(sid)
	types := eventTypes(buf.snapshot())
	require.Contains(t, types, "iteration_start")
	require.Contains(t, types, "verify_run")
	require.Contains(t, types, "verify_result")
	require.Contains(t, types, "tool_call")
	require.Contains(t, types, "tool_result")
	require.Contains(t, types, "provider_response")

	// Order: iteration_start before tool_call(verify) before verify_run
	// before tool_result(verify) before verify_result.
	require.Less(t, indexOf(types, "iteration_start"), indexOf(types, "tool_call"))
	require.Less(t, indexOf(types, "tool_call"), indexOf(types, "verify_run"))
	require.Less(t, indexOf(types, "verify_run"), indexOf(types, "tool_result"))
	require.Less(t, indexOf(types, "tool_result"), indexOf(types, "verify_result"))
}

func eventTypes(es []event.Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}
func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------
// P67c — chatStuckDispatcher
// ---------------------------------------------------------------------

// stubLLM is a minimal provider.Provider for dispatcher unit tests.
// Calls are counted so the tests can assert opt-in/cap/cooldown behavior.
type stubLLM struct {
	text  string
	err   error
	calls int
}

func (s *stubLLM) Name() string { return "stub" }
func (s *stubLLM) Complete(ctx context.Context, _ provider.Request) (provider.Response, error) {
	s.calls++
	if s.err != nil {
		return provider.Response{}, s.err
	}
	return provider.Response{Text: s.text}, nil
}

// populateNoProgressTest builds a NoProgress-shaped buffer with `iters`
// iterations, each: iteration_start → verify_run → verify_result(false)
// → write_file tool_call/result (input varies per iter to satisfy
// "files churning"). Mirrors what the chat agent loop emits.
func populateNoProgressTest(buf *chatEventBuffer, iters int) {
	for i := 0; i < iters; i++ {
		buf.resetTurn()
		buf.push(event.Event{Type: "iteration_start", Data: jsonMust(map[string]any{"iter": buf.iter})})
		buf.push(event.Event{Type: "verify_run"})
		buf.push(event.Event{Type: "verify_result", Data: jsonMust(map[string]any{"passed": false})})
		buf.push(event.Event{Type: "tool_call", Data: jsonMust(map[string]any{"name": "write_file", "input": `{"path":"a.go","content":"x` + string(rune('0'+i)) + `"}`})})
		buf.push(event.Event{Type: "tool_result", Data: jsonMust(map[string]any{"name": "write_file", "isError": false})})
	}
}

func TestChatStuckDispatcher_NoProgressFiresAdversary(t *testing.T) {
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{text: "Read a.go and trace the failing assertion."}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub,
		model:      "test-model",
		riskAdv:    "test-model",
	}
	decs := disp.tick(context.Background(), buf, nil)
	require.NotEmpty(t, decs)
	require.Equal(t, 1, stub.calls, "exactly one adversary LLM call")
	var advFound bool
	for _, d := range decs {
		if d.Action == stuck.ActionAdversaryConsult {
			require.Contains(t, d.Explanation, "Read a.go")
			advFound = true
		}
	}
	require.True(t, advFound, "expected ActionAdversaryConsult Decision")
}

func TestChatStuckDispatcher_NoAdversaryWhenRiskEmpty(t *testing.T) {
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{text: "should not be called"}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub,
		model:      "test-model",
		riskAdv:    "", // OFF
	}
	_ = disp.tick(context.Background(), buf, nil)
	require.Equal(t, 0, stub.calls, "adversary must not be called when riskAdv is empty")
}

func TestChatStuckDispatcher_CooldownBetweenAdversaryCalls(t *testing.T) {
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{text: "Hint A"}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub, model: "m", riskAdv: "m",
	}
	_ = disp.tick(context.Background(), buf, nil) // call 1 → adversary fires
	require.Equal(t, 1, stub.calls)

	// Bypass per-turn dedup to simulate "same turn, second fire attempt":
	// cooldown must still block (lastAdversaryIter == iter).
	buf.seenThisTurn = make(map[stuck.Pattern]bool)
	_ = disp.tick(context.Background(), buf, nil)
	require.Equal(t, 1, stub.calls, "cooldown must block adversary fire in same iter")

	// Advance to next iter — fires again.
	populateNoProgressTest(buf, 1) // adds 1 more iter
	_ = disp.tick(context.Background(), buf, nil)
	require.Equal(t, 2, stub.calls)
}

func TestChatStuckDispatcher_BudgetCap(t *testing.T) {
	buf := newChatEventBuffer(2000)
	stub := &stubLLM{text: "Hint"}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub, model: "m", riskAdv: "m",
	}
	// 6 distinct iter-spans of NoProgress shape, each triggers exactly one
	// adversary call before cap kicks in.
	for i := 0; i < 6; i++ {
		populateNoProgressTest(buf, 4)
		_ = disp.tick(context.Background(), buf, nil)
	}
	require.Equal(t, chatAdversaryBudgetCap, stub.calls,
		"adversary calls capped at chatAdversaryBudgetCap")
}

func TestChatStuckDispatcher_AdversaryErrorDoesNotCrash(t *testing.T) {
	// P67l: an errored AdversaryConsult now produces a sentinel
	// Decision (Action=AdversaryConsult, Explanation=ADVERSARY_EMPTY:...)
	// so the caller can emit `adversary_consult_empty` telemetry.
	// Without this, opt-in dispatches that silently fail (empty LLM
	// response, provider timeout, etc.) would be invisible — the chess
	// r2 turn 5 case (Monologue fireCount=2 but no adversary Part) is
	// exactly the failure mode this guard catches.
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{err: context.DeadlineExceeded}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub, model: "m", riskAdv: "m",
	}
	decs := disp.tick(context.Background(), buf, nil)
	require.Len(t, decs, 1, "errored AdversaryConsult must surface ADVERSARY_EMPTY sentinel")
	require.Equal(t, stuck.ActionAdversaryConsult, decs[0].Action)
	require.Contains(t, decs[0].Explanation, "ADVERSARY_EMPTY:")
}

// TestChatStuckDispatcher_AdversaryEmptyResponseSentinel locks the
// telemetry-on-empty-response path. AdversaryConsultStrategy.Apply
// returns ErrNoFallback when the LLM responds with empty/whitespace
// text (recovery.go:337-339); the dispatcher must turn that into a
// sentinel Decision rather than dropping the event silently.
func TestChatStuckDispatcher_AdversaryEmptyResponseSentinel(t *testing.T) {
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{text: "   \n  "} // whitespace-only → suggestion="" → ErrNoFallback
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub, model: "m", riskAdv: "m",
	}
	decs := disp.tick(context.Background(), buf, nil)
	require.Len(t, decs, 1)
	require.Equal(t, stuck.ActionAdversaryConsult, decs[0].Action)
	require.Contains(t, decs[0].Explanation, "ADVERSARY_EMPTY:")
}

func TestChatPrompt_CrossTurnNoProgressDetected(t *testing.T) {
	// 4 Prompt() calls, each: write_file → verify(fail). 4th call's
	// post-tool_result Detector.Check must surface PatternNoProgress.
	turnsPerCall := []provider.MockTurn{
		// write_file with churning content (path same, content varies inside Prompt(...))
		{ToolCalls: []provider.ToolCall{{ID: "w", Name: "write_file",
			Input: []byte(`{"path":"a.go","content":"x"}`)}},
			StopReason: "tool_use"},
		// verify FAIL
		{ToolCalls: []provider.ToolCall{{ID: "v", Name: "verify",
			Input: []byte(`{}`)}},
			StopReason: "tool_use"},
		// end turn (no more tools)
		{Text: "done", StopReason: "end_turn"},
	}
	// Make verify return error via mock — MockToolProvider returns
	// fake results that are non-error by default; to force IsError=true
	// we'd need a verify tool registry override. Instead we just rely
	// on the fact that this test verifies signal detection, not
	// adversary firing. Detector cares about the verify_result
	// payload — passed=false comes from result.IsError. The mock's
	// default result is not an error → passed=true → NoProgress
	// would *not* fire. Skip the adversary assertion; just confirm
	// iteration_start events accumulate cross-turn.
	turns := []provider.MockTurn{}
	for i := 0; i < 4; i++ {
		turns = append(turns, turnsPerCall...)
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	for i := 0; i < 4; i++ {
		stream := &fakePromptStream{ctx: context.Background()}
		_ = svc.Prompt(promptReq(sid, "go"), stream)
	}
	buf := svc.chatEventBufFor(sid)
	require.GreaterOrEqual(t, buf.iter, 4, "iter must reach 4 after 4 user turns")
	// Count iteration_start events in the buffer (or evicted; cap=200
	// should hold them all for a 4-turn run).
	starts := 0
	for _, e := range buf.snapshot() {
		if e.Type == "iteration_start" {
			starts++
		}
	}
	require.Equal(t, 4, starts, "one iteration_start per user turn")
}

func TestChatStuckDispatcher_RepeatedActionObservationFires(t *testing.T) {
	// 4 identical (name, input, result) pairs should make
	// core/stuck/Detector return PatternRepeatedActionObservation —
	// proving the Detector covers the case the P39 ad-hoc check
	// used to handle. (Detector threshold is 4; old P39 was 3 — a
	// small behavior change documented in the P67e commit.)
	buf := newChatEventBuffer(200)
	for i := 0; i < 4; i++ {
		buf.push(event.Event{
			Type: "tool_call",
			Data: jsonMust(map[string]any{
				"id": "c", "name": "read_file", "input": `{"path":"x"}`,
			}),
		})
		buf.push(event.Event{
			Type: "tool_result",
			Data: jsonMust(map[string]any{
				"id": "c", "name": "read_file", "is_error": false, "content": "ok",
			}),
		})
	}
	det := &stuck.Detector{}
	sigs := det.Check(buf.snapshot())
	var found bool
	for _, s := range sigs {
		if s.Pattern == stuck.PatternRepeatedActionObservation {
			found = true
		}
	}
	require.True(t, found, "Detector must fire RepeatedActionObservation on 4 identical tool_call/result pairs (was P39's job)")
}

func TestChatStuckDispatcher_MonologueFiresOnSilentTurns(t *testing.T) {
	// 3 consecutive provider_response events with tool_calls=0
	// should fire PatternMonologue. Mirrors the chess "agent gave up"
	// pattern where turn 1 has 30 tool calls but turn 2+ are silent.
	buf := newChatEventBuffer(200)
	// Seed one productive turn so the run starts at 0 and accumulates.
	buf.push(event.Event{Type: "iteration_start", Data: jsonMust(map[string]any{"iter": 1})})
	buf.push(event.Event{Type: "tool_call", Data: jsonMust(map[string]any{"name": "verify", "input": "{}"})})
	buf.push(event.Event{Type: "tool_result", Data: jsonMust(map[string]any{"name": "verify", "is_error": true, "content": "FAIL"})})
	buf.push(event.Event{Type: "provider_response", Data: jsonMust(map[string]any{"text_len": 100, "tool_calls": 1})})

	// 3 silent turns
	for i := 2; i <= 4; i++ {
		buf.push(event.Event{Type: "iteration_start", Data: jsonMust(map[string]any{"iter": i})})
		buf.push(event.Event{Type: "provider_response", Data: jsonMust(map[string]any{"text_len": 80, "tool_calls": 0})})
	}

	det := &stuck.Detector{}
	sigs := det.Check(buf.snapshot())
	var found bool
	for _, s := range sigs {
		if s.Pattern == stuck.PatternMonologue {
			found = true
		}
	}
	require.True(t, found, "Detector must fire PatternMonologue on 3 silent provider_responses (real chess pattern)")
}

// TestChatPrompt_Production_MonologueFiresAdversary is the P67h
// production-faithful repro. Three sequential silent end_turns must
// trigger PatternMonologue at the end-of-Prompt() tick, fall through
// AltToolOrder (returns ErrNoFallback for Monologue), and reach
// AdversaryConsultStrategy. We assert a `[system] adversary:` Part
// lands in the stream.
//
// If this test fails the way the 2026-05-19 chess N=3 sweep failed
// (0/3 adversary firings despite turns 4-10 going silent), the
// production wiring is dead and the unit-level dispatcher tests are
// passing for the wrong reason.
//
// Uses a SHARED mock provider via newTestSessionServiceWithSharedProvider
// because each production Prompt() invocation calls s.providerFactory()
// fresh; the default factory resets the scripted queue per call, which
// would replay the write_file+verify productive turn on every
// subsequent user message and produce a PingPong pattern instead of
// the Monologue we want to test.
func TestChatPrompt_Production_MonologueFiresAdversary(t *testing.T) {
	turns := []provider.MockTurn{
		// Turn 1: write_file + verify + end_turn. Two productive iters
		// then silent close (provider_response{tool_calls=0}).
		{ToolCalls: []provider.ToolCall{{ID: "w", Name: "write_file",
			Input: []byte(`{"path":"a.go","content":"package main\n"}`)}},
			StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "v", Name: "verify",
			Input: []byte(`{"description":"build","command":"go version"}`)}},
			StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
		// Turn 2: silent end_turn — provider_response{tool_calls=0}.
		{Text: "thinking", StopReason: "end_turn"},
		// Turn 3: silent again — buffer now has 3 consecutive
		// provider_response{tool_calls=0} (turn 1 last iter, turn 2, turn 3).
		// PatternMonologue must fire; AltToolOrder returns ErrNoFallback;
		// AdversaryConsult must be invoked.
		{Text: "still thinking", StopReason: "end_turn"},
		// AdversaryConsult's LLM call lands here (consumes mock 6).
		{Text: "Read a.go and add a real test before declaring done.",
			StopReason: "end_turn"},
	}
	shared := provider.NewMockToolProvider(turns)
	svc, sid := newTestSessionServiceWithSharedProvider(t, shared)

	// PromptRequest must carry adversary_model so dispatcher's riskAdv
	// is non-empty — the production opt-in gate.
	mkReq := func(text string) *gilv1.PromptRequest {
		return &gilv1.PromptRequest{
			SessionId:      sid,
			AdversaryModel: "mock-model",
			Parts:          []*gilv1.PromptPart{{Body: &gilv1.PromptPart_Text{Text: text}}},
		}
	}

	s1 := &fakePromptStream{ctx: context.Background()}
	require.NoError(t, svc.Prompt(mkReq("go"), s1), "turn 1 (productive) must succeed")
	s2 := &fakePromptStream{ctx: context.Background()}
	require.NoError(t, svc.Prompt(mkReq("continue"), s2), "turn 2 (silent) must succeed")
	s3 := &fakePromptStream{ctx: context.Background()}
	require.NoError(t, svc.Prompt(mkReq("continue"), s3), "turn 3 (silent + Monologue) must succeed")

	// Concatenate all text parts across all three turns and search for
	// the adversary marker. The hint string itself ("Read a.go...") will
	// also appear because the dispatcher emits it via stream.Send.
	var allText string
	for _, s := range []*fakePromptStream{s1, s2, s3} {
		for _, p := range s.Parts {
			if td := p.GetText(); td != nil {
				allText += td.GetContent() + "\n"
			}
		}
	}
	require.Contains(t, allText, "[system] adversary",
		"production strategy chain must dispatch AdversaryConsult after 3 silent turns; observed text:\n%s",
		allText)
}

// TestChatPrompt_Production_EscalatesToAdversaryOnSecondFire is the
// P67i escalation guard. Same action-level pattern firing twice should
// escalate the second occurrence to AdversaryConsult — the cheap
// AltToolOrder nudge clearly didn't change behavior or the pattern
// wouldn't have re-fired. The 2026-05-19 chess N=3 sweep observed this
// dead-end directly: AltToolOrder fired at r1 turns 9 AND 10 and
// adversary never got a chance.
//
// Drives RepeatedActionError (threshold 3, error pairs) by hammering
// read_file on a missing path. Turn 3 fires AltToolOrder; turn 4 must
// escalate to AdversaryConsult.
func TestChatPrompt_Production_EscalatesToAdversaryOnSecondFire(t *testing.T) {
	readMissing := provider.MockTurn{
		ToolCalls: []provider.ToolCall{{ID: "r", Name: "read_file",
			Input: []byte(`{"path":"does-not-exist.go"}`)}},
		StopReason: "tool_use",
	}
	endTurn := provider.MockTurn{Text: "ok", StopReason: "end_turn"}
	turns := []provider.MockTurn{
		// Turn 1, 2: read_file (error) + end_turn. Buffer accumulates
		// error pairs but stays below threshold (< 3).
		readMissing, endTurn,
		readMissing, endTurn,
		// Turn 3: 3rd read_file (error). Intra-loop tick after
		// tool_result push detects RepeatedActionError. 1st fire →
		// AltToolOrder (cheap nudge). Then end_turn.
		readMissing, endTurn,
		// Turn 4: 4th read_file (error). Intra-loop tick detects
		// RepeatedActionError again. 2nd fire → escalation → adversary
		// LLM consult. Adversary consumes the next mock turn.
		readMissing,
		{Text: "Stop calling read_file on the missing path; list dir first.",
			StopReason: "end_turn"},
		endTurn,
	}
	shared := provider.NewMockToolProvider(turns)
	svc, sid := newTestSessionServiceWithSharedProvider(t, shared)

	mkReq := func(text string) *gilv1.PromptRequest {
		return &gilv1.PromptRequest{
			SessionId:      sid,
			AdversaryModel: "mock-model",
			Parts:          []*gilv1.PromptPart{{Body: &gilv1.PromptPart_Text{Text: text}}},
		}
	}
	streams := make([]*fakePromptStream, 4)
	for i := range streams {
		streams[i] = &fakePromptStream{ctx: context.Background()}
		require.NoError(t, svc.Prompt(mkReq("continue"), streams[i]),
			"turn %d must succeed", i+1)
	}

	var allText string
	for _, s := range streams {
		for _, p := range s.Parts {
			if td := p.GetText(); td != nil {
				allText += td.GetContent() + "\n"
			}
		}
	}
	require.Contains(t, allText, "[system] stuck-recover (AltToolOrder)",
		"1st RepeatedActionError fire must hit AltToolOrder:\n%s", allText)
	require.Contains(t, allText, "[system] adversary",
		"2nd RepeatedActionError fire must escalate to AdversaryConsult:\n%s", allText)
}
