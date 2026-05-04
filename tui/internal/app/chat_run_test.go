package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// formatChatRunEvent decisions are user-visible; pin them.

func TestFormatChatRunEvent_Started_FlipsToRun(t *testing.T) {
	phase, lines, keep := formatChatRunEvent(&gilv1.Event{Type: "run.started"})
	if phase != ChatPhaseRun {
		t.Errorf("phase = %v; want ChatPhaseRun", phase)
	}
	if !keep {
		t.Error("keepDraining must be true on run.started")
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "agent run started") {
		t.Errorf("lines = %v; want one starting-note line", lines)
	}
}

func TestFormatChatRunEvent_Iter_RendersIterAndCost(t *testing.T) {
	ev := &gilv1.Event{
		Type:     "run.iter",
		DataJson: []byte(`{"iter":7}`),
		Metrics:  &gilv1.EventMetrics{CostUsd: 0.0421},
	}
	phase, lines, _ := formatChatRunEvent(ev)
	if phase != ChatPhaseRun {
		t.Errorf("phase = %v; want ChatPhaseRun", phase)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "iter 7") || !strings.Contains(lines[0], "0.0421") {
		t.Errorf("lines = %v; want one line with iter 7 and cost 0.0421", lines)
	}
}

func TestFormatChatRunEvent_StuckDetected_NamedPattern(t *testing.T) {
	ev := &gilv1.Event{
		Type:     "stuck_detected",
		DataJson: []byte(`{"pattern":"PatternMonologue","detail":"3 turns"}`),
	}
	phase, lines, _ := formatChatRunEvent(ev)
	if phase != ChatPhaseStuck {
		t.Errorf("phase = %v; want ChatPhaseStuck", phase)
	}
	if len(lines) == 0 ||
		!strings.Contains(lines[0], "talking without acting") ||
		!strings.Contains(lines[0], "3 turns") {
		t.Errorf("lines = %v; want pattern + detail", lines)
	}
	// "!" glyph signals user attention needed.
	if !strings.Contains(lines[0], "!") {
		t.Errorf("stuck line should use ! attention glyph; got %q", lines[0])
	}
}

func TestFormatChatRunEvent_Recovered_FlipsBackToRun(t *testing.T) {
	ev := &gilv1.Event{
		Type:     "stuck_recovered",
		DataJson: []byte(`{"explanation":"swapped tool order"}`),
	}
	phase, lines, _ := formatChatRunEvent(ev)
	if phase != ChatPhaseRun {
		t.Errorf("phase = %v; want ChatPhaseRun (recovered restores run)", phase)
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "swapped tool order") {
		t.Errorf("lines = %v; want explanation surfaced", lines)
	}
}

func TestFormatChatRunEvent_Done_FlipsToDoneAndStopsDraining(t *testing.T) {
	phase, lines, keep := formatChatRunEvent(&gilv1.Event{Type: "run.done"})
	if phase != ChatPhaseDone {
		t.Errorf("phase = %v; want ChatPhaseDone", phase)
	}
	if keep {
		t.Error("run.done must NOT keep draining (terminal)")
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "run complete") {
		t.Errorf("lines = %v; want completion line", lines)
	}
}

func TestFormatChatRunEvent_Unknown_PassesSilently(t *testing.T) {
	phase, lines, keep := formatChatRunEvent(&gilv1.Event{Type: "tool_call"})
	if phase != "" || len(lines) != 0 {
		t.Errorf("unknown event should not flip phase or emit lines; got phase=%v lines=%v", phase, lines)
	}
	if !keep {
		t.Error("keepDraining must remain true for unknown event types")
	}
}

func TestChatUpdate_RunStarted_OpensTailAndFlipsPhase(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatRunStartedMsg{sessionID: "01HQXY"})
	cm := updated.(*chatModel)
	if cm.phase != ChatPhaseRun {
		t.Errorf("phase = %v; want ChatPhaseRun", cm.phase)
	}
	if !transcriptContains(cm.transcript, "run started") {
		t.Errorf("expected run-started note; got %v", cm.transcript)
	}
}

func TestChatUpdate_RunStartFailed_AppendsErr(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatRunStartFailedMsg{err: "daemon down"})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "daemon down") {
		t.Errorf("expected error in transcript; got %v", cm.transcript)
	}
	if cm.phase == ChatPhaseRun {
		t.Error("failed start must not flip phase to run")
	}
}

func TestChatUpdate_RunEvent_AppendsAndKeepsDraining(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	ev := &gilv1.Event{Type: "run.iter", DataJson: []byte(`{"iter":3}`)}
	updated, cmd := m.Update(chatRunEventMsg{ev: ev})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "iter 3") {
		t.Errorf("expected iter 3 in transcript; got %v", cm.transcript)
	}
	// Without an active runTail.handle we can't pump again; cmd
	// must be nil so the test doesn't deadlock waiting on Recv.
	if cmd != nil {
		t.Error("Update must not return a pump cmd when no tail handle is set")
	}
}

func TestChatUpdate_RunTailErr_AppendsAndClearsState(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatRunTailErrMsg{err: "broken pipe"})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "broken pipe") {
		t.Errorf("expected error in transcript; got %v", cm.transcript)
	}
	if cm.runTail.handle != nil {
		t.Error("runTail handle must be cleared on err")
	}
}

func TestChatUpdate_AgentTurn_CoalescesRunChunks(t *testing.T) {
	// Two consecutive agent_turn events should fold onto the same
	// transcript line, matching the interview-side coalescing logic.
	m := newChatModel("/tmp/test.sock")
	first := &gilv1.Event{Type: "agent_turn", DataJson: []byte(`{"content":"hello "}`)}
	second := &gilv1.Event{Type: "agent_turn", DataJson: []byte(`{"content":"world"}`)}
	updated, _ := m.Update(chatRunEventMsg{ev: first})
	cm := updated.(*chatModel)
	updated, _ = cm.Update(chatRunEventMsg{ev: second})
	cm = updated.(*chatModel)
	if len(cm.transcript) == 0 {
		t.Fatal("expected transcript to have a coalesced agent line")
	}
	last := cm.transcript[len(cm.transcript)-1]
	if !strings.Contains(last, "hello world") {
		t.Errorf("expected coalesced line to contain 'hello world'; got %q", last)
	}
}

// Ensure the run-started msg doesn't accidentally swallow the
// chatVerbResultMsg path used by other verbs.
func TestChatUpdate_RunStarted_DoesNotEatVerbResult(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	_, _ = m.Update(chatRunStartedMsg{sessionID: "X"})
	updated, _ := m.Update(chatVerbResultMsg{kind: "ok", text: "ping"})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "ping") {
		t.Errorf("verb result was lost after run-started; got %v", cm.transcript)
	}
}

// --- chat_stream interview-event handlers (parity with cli REPL) ---

func TestChatUpdate_StageReason_FlipsPhaseAndAppendsNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatStageReasonMsg{
		phase: ChatPhaseInterview, reason: "domain=cli-tooling confidence=0.85",
	})
	cm := updated.(*chatModel)
	if cm.phase != ChatPhaseInterview {
		t.Errorf("phase = %v; want ChatPhaseInterview", cm.phase)
	}
	if !transcriptContains(cm.transcript, "interview started", "domain=cli-tooling") {
		t.Errorf("expected interview-started note with reason; got %v", cm.transcript)
	}
}

func TestChatUpdate_StageReason_AwaitingConfirm_HintsNextStep(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatStageReasonMsg{
		phase: ChatPhaseAwaitingConfirm, reason: "all 7 slots saturated",
	})
	cm := updated.(*chatModel)
	if cm.phase != ChatPhaseAwaitingConfirm {
		t.Errorf("phase = %v; want ChatPhaseAwaitingConfirm", cm.phase)
	}
	// Affordance hint: tell the user that 'run' is the natural next step.
	if !transcriptContains(cm.transcript, "ready to freeze", "run") {
		t.Errorf("expected ready-to-freeze + run hint; got %v", cm.transcript)
	}
}

func TestChatUpdate_Saturation_AppendsProgress(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatSaturationMsg{filled: 3, total: 7, saturation: 0.43})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "3/7", "43%") {
		t.Errorf("expected slot-fill progress with percent; got %v", cm.transcript)
	}
}

func TestChatUpdate_Adversary_AppendsCount(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatAdversaryMsg{count: 2})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "adversary", "2 finding") {
		t.Errorf("expected adversary count line; got %v", cm.transcript)
	}
}

func TestChatUpdate_Adversary_ZeroCountStaysSilent(t *testing.T) {
	// Zero findings is a clean check; no need to add a transcript
	// line that says "0 findings" — that's noise, not signal.
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatAdversaryMsg{count: 0})
	cm := updated.(*chatModel)
	if len(cm.transcript) != 0 {
		t.Errorf("zero adversary findings should produce no transcript line; got %v", cm.transcript)
	}
}

// silence unused import when none of the helpers above pull tea.
var _ = tea.Quit
