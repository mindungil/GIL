package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// In test mode (m.client == nil) every verb that needs network must
// still emit the router's arrow note so the user sees the dispatch.
// The async cmd is suppressed so unit tests don't reach the wire.

func TestChatVerb_Sessions_AppendsArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("list my sessions")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→", "session") {
		t.Fatalf("expected → sessions arrow note, got %v", cm.transcript)
	}
}

func TestChatVerb_New_AppendsArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("start a new session")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→", "new") {
		t.Fatalf("expected → new arrow note, got %v", cm.transcript)
	}
}

func TestChatVerb_Status_AppendsArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("how's it going?")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→", "status") {
		t.Fatalf("expected → status arrow note, got %v", cm.transcript)
	}
}

func TestChatVerb_Diff_AppendsArrowNote(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("show me the diff")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "→", "diff") {
		t.Fatalf("expected → diff arrow note, got %v", cm.transcript)
	}
}

func TestChatVerb_Help_AppendsHelpLine(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("what can you do")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if !transcriptContains(cm.transcript, "slash escape-hatch") {
		t.Fatalf("expected help line in transcript, got %v", cm.transcript)
	}
}

func TestChatVerb_Switch_ChangesActiveID(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("switch to 01KQEPABCXYZ12345")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	if cm.activeID != "01KQEPABCXYZ12345" {
		t.Fatalf("expected activeID to flip, got %q", cm.activeID)
	}
	if !transcriptContains(cm.transcript, "switched to") {
		t.Fatalf("expected switch confirmation in transcript, got %v", cm.transcript)
	}
}

func TestChatVerb_Merge_ShowsConfirmHint(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ta.SetValue("merge it")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// Merge needs a confirmation prompt that the TUI doesn't yet host;
	// dispatchVerb must surface that via a "?" guidance line, not
	// silently no-op or send a destructive call to the daemon.
	if !transcriptContains(cm.transcript, "merge needs a confirmation") {
		t.Fatalf("expected merge guidance line, got %v", cm.transcript)
	}
}

func TestChatVerb_Run_GuardsBeforeAwaitingConfirm(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.activeID = "01KQEPABCXYZ"
	m.phase = ChatPhaseInterview // not yet AwaitingConfirm
	m.input.ta.SetValue("run it")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// The phase guard mirrors the cli REPL — start a run only when the
	// spec is ready to freeze.
	if !transcriptContains(cm.transcript, "spec is not ready to freeze") {
		t.Fatalf("expected run phase guard, got %v", cm.transcript)
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
