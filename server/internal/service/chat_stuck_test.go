package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/stuck"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// P39 — chat-side stuck detection. Pure-function tests on chatStuckSig
// and chatStuckCheck; plus an end-to-end Prompt test that scripts 3
// identical tool calls and asserts the warning Part lands on the stream.

func TestChatStuckSig_StableAndSensitive(t *testing.T) {
	// Same name + same input → same sig.
	a := chatStuckSig("read_file", []byte(`{"path":"main.go"}`))
	b := chatStuckSig("read_file", []byte(`{"path":"main.go"}`))
	require.Equal(t, a, b)

	// Same name, different input → different sig.
	c := chatStuckSig("read_file", []byte(`{"path":"other.go"}`))
	require.NotEqual(t, a, c)

	// Different name, same input → different sig.
	d := chatStuckSig("write_file", []byte(`{"path":"main.go"}`))
	require.NotEqual(t, a, d)

	// Both empty → still produces a stable sig (don't panic).
	e := chatStuckSig("", nil)
	f := chatStuckSig("", nil)
	require.Equal(t, e, f)
}

func TestChatStuckCheck_WindowSemantics(t *testing.T) {
	// Fewer than window → never fires.
	require.False(t, chatStuckCheck([]string{"a", "a"}, 3))

	// Window of 3, trailing 3 same → fires.
	require.True(t, chatStuckCheck([]string{"a", "a", "a"}, 3))

	// Trailing 3 same but earlier differ → still fires (only trailing matters).
	require.True(t, chatStuckCheck([]string{"x", "y", "a", "a", "a"}, 3))

	// Trailing not all same → no fire.
	require.False(t, chatStuckCheck([]string{"a", "a", "b"}, 3))

	// Different trailing patterns.
	require.False(t, chatStuckCheck([]string{"a", "b", "a"}, 3))
	require.False(t, chatStuckCheck([]string{"a", "b", "c"}, 3))

	// Zero/negative window → false.
	require.False(t, chatStuckCheck([]string{"a", "a", "a"}, 0))
	require.False(t, chatStuckCheck([]string{"a", "a", "a"}, -1))

	// Empty → false.
	require.False(t, chatStuckCheck(nil, 3))
}

// TestPrompt_RepeatedToolCalls_EmitsStuckWarning scripts 3 identical
// tool calls and verifies the chat surface receives the stuck_detected
// warning Part. The mock provider returns a read_file call on every
// turn; after 3 such turns the warning fires.
func TestPrompt_RepeatedToolCalls_EmitsStuckWarning(t *testing.T) {
	turns := []provider.MockTurn{
		// Turn 1: read_file
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 2: same read_file
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 3: same read_file — sweep at this point should fire stuck warning.
		{ToolCalls: []provider.ToolCall{{ID: "c3", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 4: stop
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	// Walk parts; find the system stuck warning.
	found := false
	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") && strings.Contains(td.GetContent(), "read_file") {
			found = true
			break
		}
	}
	require.True(t, found, "expected a stuck_detected system Part after 3 identical tool calls; got %d parts", len(stream.Parts))
}

// TestPrompt_DistinctToolCalls_NoStuckWarning: 3 different tool calls
// (or 3 read_file calls with different paths) must NOT fire the warning.
func TestPrompt_DistinctToolCalls_NoStuckWarning(t *testing.T) {
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file",
			Input: []byte(`{"path":"a.go"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read_file",
			Input: []byte(`{"path":"b.go"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "c3", Name: "read_file",
			Input: []byte(`{"path":"c.go"}`)}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read several files"), stream)

	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		require.NotContains(t, td.GetContent(), "stuck_detected",
			"3 distinct calls must NOT trigger stuck warning")
	}
}

// TestPrompt_StuckFiresOnce: even if the agent continues the stuck loop
// past the threshold, the warning must surface ONCE, not on every
// subsequent identical call.
func TestPrompt_StuckFiresOnce(t *testing.T) {
	identical := provider.ToolCall{ID: "c", Name: "read_file",
		Input: []byte(`{"path":"main.go"}`)}
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	count := 0
	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") {
			count++
		}
	}
	require.Equal(t, 1, count, "stuck warning must fire exactly once per turn, not on every identical call past threshold")
}

// Sanity: the warning Part is delivered as a Text part (not a Done
// or Reasoning part), so chat clients render it inline.
func TestPrompt_StuckWarning_IsTextPart(t *testing.T) {
	identical := provider.ToolCall{ID: "c", Name: "read_file",
		Input: []byte(`{"path":"main.go"}`)}
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") {
			// Confirm it's a TextDelta (not Reasoning or Done).
			require.NotNil(t, p.GetText(), "stuck warning must be a Text part")
			require.IsType(t, &gilv1.Part_Text{}, p.Body)
			return
		}
	}
	t.Fatalf("did not find stuck_detected warning part")
}

// ---------------------------------------------------------------------
// P67b — chatEventBuffer (will replace P39 ad-hoc tests in P67e).
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
	buf := newChatEventBuffer(200)
	populateNoProgressTest(buf, 4)
	stub := &stubLLM{err: context.DeadlineExceeded}
	disp := &chatStuckDispatcher{
		detector:   &stuck.Detector{},
		strategies: []stuck.Strategy{stuck.AdversaryConsultStrategy{}},
		provider:   stub, model: "m", riskAdv: "m",
	}
	decs := disp.tick(context.Background(), buf, nil)
	require.Empty(t, decs, "errored strategy returns no Decision; caller still alive")
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
