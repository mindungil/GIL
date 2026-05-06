package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// M2: the TUI chat surface no longer dispatches slashes client-side.
// Every input forwards to the daemon's agent loop via
// SessionService.Prompt; the agent calls the matching tool. The only
// client-side check left is terminal exit ("quit"/"exit"/"bye").
//
// dispatchVerb still exists in chat_verbs.go but only for legacy
// chatVerbResultMsg / chatNewSessionMsg paths the TUI itself fires
// (e.g. on session creation events). Those tests live below.

func TestChatVerb_NL_PhrasesForwardNotDispatch(t *testing.T) {
	// Both natural-language phrases and slash-prefixed input forward
	// to the daemon. No client-side regex / verb classification.
	for _, phrase := range []string{
		"show me the diff",
		"how's it going?",
		"what can you do",
		"merge it",
		"switch to the dark one",
		"start a new session",
		"/diff",
		"/sessions",
		"/help",
	} {
		m := newChatModel("/tmp/test.sock")
		m.input.ta.SetValue(phrase)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		cm := updated.(*chatModel)
		// The user echo `›  <phrase>` is appended unconditionally; the
		// chat model does NOT add an arrow note (which would indicate
		// client-side verb dispatch).
		for _, line := range cm.transcript {
			if strings.Contains(line, "→") {
				t.Errorf("phrase %q must forward, got arrow note %q", phrase, line)
			}
		}
	}
}

func TestChatVerb_TerminalExit_ExitsCleanly(t *testing.T) {
	for _, word := range []string{"quit", "exit", "bye", "/quit", "/exit", "QUIT"} {
		m := newChatModel("/tmp/test.sock")
		m.input.ta.SetValue(word)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Errorf("exit word %q should return tea.Quit cmd, got nil", word)
		}
	}
}

func TestChatVerb_VerbResultMsg_AppendsLines(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatVerbResultMsg{kind: "ok", text: "line1\nline2"})
	cm := updated.(*chatModel)
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
