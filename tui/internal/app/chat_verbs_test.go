package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// The router was gutted (see core/intent/router.go header) so
// natural-language phrases like "show me the diff" no longer
// classify client-side — they forward to the daemon. The slash
// escape hatch still dispatches verbs, and these tests pin that
// path: typing `/<verb> [args]` triggers dispatchVerb the same way
// the old NL verb-pattern path used to.

func TestChatVerb_Sessions_SlashDispatchesArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("/sessions")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→") {
		t.Fatalf("expected → arrow note for /sessions, got %v", cm.transcript)
	}
}

func TestChatVerb_New_SlashDispatchesArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("/new")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→") {
		t.Fatalf("expected → arrow note for /new, got %v", cm.transcript)
	}
}

func TestChatVerb_Status_SlashDispatchesArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.activeID = "01KQEP000001"
	m.input.ta.SetValue("/status")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→") {
		t.Fatalf("expected → arrow note for /status, got %v", cm.transcript)
	}
}

func TestChatVerb_Diff_SlashDispatchesArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.activeID = "01KQEP000001"
	m.input.ta.SetValue("/diff")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→") {
		t.Fatalf("expected → arrow note for /diff, got %v", cm.transcript)
	}
}

func TestChatVerb_Help_SlashAppendsHelpLine(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("/help")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "slash escape-hatch") {
		t.Fatalf("expected help line in transcript, got %v", cm.transcript)
	}
}

func TestChatVerb_Switch_SlashChangesActiveID(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("/switch 01KQEPABCXYZ12345")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if cm.activeID != "01KQEPABCXYZ12345" {
		t.Fatalf("expected activeID to flip via /switch, got %q", cm.activeID)
	}
	if !transcriptContains(cm.transcript, "switched to") {
		t.Fatalf("expected switch confirmation in transcript, got %v", cm.transcript)
	}
}

func TestChatVerb_Merge_SlashShowsConfirmHint(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("/merge")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// Merge needs a confirmation prompt that the TUI doesn't yet host;
	// dispatchVerb must surface that via a "?" guidance line, not
	// silently no-op or send a destructive call to the daemon.
	if !transcriptContains(cm.transcript, "merge needs a confirmation") {
		t.Fatalf("expected merge guidance line, got %v", cm.transcript)
	}
}

func TestChatVerb_Run_SlashGuardsBeforeAwaitingConfirm(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.activeID = "01KQEPABCXYZ"
	m.phase = ChatPhaseInterview // not yet AwaitingConfirm
	m.input.ta.SetValue("/run")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// The phase guard mirrors the cli REPL — start a run only when the
	// spec is ready to freeze.
	if !transcriptContains(cm.transcript, "spec is not ready to freeze") {
		t.Fatalf("expected run phase guard, got %v", cm.transcript)
	}
}

// Natural-language phrases that USED to dispatch verbs client-side
// now forward to the daemon. The pin: chatModel doesn't try to
// resolve "show me the diff" as a verb, it just records the user
// echo and (would) call SendPrompt. Active session is required for
// SendPrompt; without one the model just records the echo and the
// daemon dispatch path is skipped — that's enough to verify the
// router doesn't intercept.
func TestChatVerb_NL_PhrasesForwardNotDispatch(t *testing.T) {
	for _, phrase := range []string{
		"show me the diff",
		"how's it going?",
		"what can you do",
		"merge it",
		"switch to the dark one",
		"start a new session",
	} {
		m := newChatModel("/tmp/test.sock")
		m.input.ta.SetValue(phrase)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		cm := updated.(*chatModel)
		// The user echo is appended unconditionally; the router does
		// NOT add an arrow note (which would indicate verb dispatch).
		if transcriptContains(cm.transcript, "→") {
			t.Errorf("phrase %q should forward, not dispatch a verb (got transcript %v)", phrase, cm.transcript)
		}
	}
}

func TestChatVerb_VerbResultMsg_AppendsLines(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatVerbResultMsg{kind: "ok", text: "line1\nline2"})
	cm := updated.(*chatModel)
	// Each line of the result becomes a separate transcript entry so
	// the renderer can wrap each independently.
	var l1, l2 bool
	for _, line := range cm.transcript {
		if strings.HasSuffix(line, "line1") {
			l1 = true
		}
		if strings.HasSuffix(line, "line2") {
			l2 = true
		}
	}
	if !l1 || !l2 {
		t.Fatalf("expected both lines as separate transcript entries, got %v", cm.transcript)
	}
}

func TestChatVerb_VerbResultMsg_ErrUsesBangGlyph(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatVerbResultMsg{kind: "err", text: "boom"})
	cm := updated.(*chatModel)
	if len(cm.transcript) == 0 || !strings.Contains(cm.transcript[0], "!") {
		t.Fatalf("expected ! glyph for err result, got %v", cm.transcript)
	}
}

func TestChatVerb_NewSessionMsg_FlipsActiveAndPrependsList(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	prev := &sdk.Session{ID: "01OLD0000000"}
	m.sessions = []*sdk.Session{prev}
	fresh := &sdk.Session{ID: "01NEW0000000"}
	updated, _ := m.Update(chatNewSessionMsg{session: fresh})
	cm := updated.(*chatModel)
	if cm.activeID != "01NEW0000000" {
		t.Fatalf("activeID should flip to new session, got %q", cm.activeID)
	}
	if len(cm.sessions) != 2 || cm.sessions[0] != fresh {
		t.Fatalf("expected new session prepended, got %v", cm.sessions)
	}
}

func TestChatVerb_NewSessionMsg_ErrorPath(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatNewSessionMsg{err: "daemon down"})
	cm := updated.(*chatModel)
	if cm.activeID != "" {
		t.Fatalf("activeID must remain unset on error, got %q", cm.activeID)
	}
	if !transcriptContains(cm.transcript, "daemon down") {
		t.Fatalf("expected error in transcript, got %v", cm.transcript)
	}
}

func transcriptContains(transcript []string, needles ...string) bool {
	joined := strings.Join(transcript, "\n")
	for _, n := range needles {
		if !strings.Contains(joined, n) {
			return false
		}
	}
	return true
}
