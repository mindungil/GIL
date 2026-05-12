package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseClarifyRequested_Valid(t *testing.T) {
	got := parseClarifyRequested("s1", "clarify_requested", []byte(`{
        "ask_id":"a1",
        "question":"Why?",
        "context":"ctx",
        "suggestions":["yes","no"],
        "urgency":"high"
    }`))
	if got == nil {
		t.Fatalf("expected non-nil")
	}
	if got.SessionID != "s1" || got.AskID != "a1" {
		t.Errorf("got: %+v", got)
	}
	if got.Question != "Why?" || got.Context != "ctx" {
		t.Errorf("missing fields: %+v", got)
	}
	if len(got.Suggestions) != 2 {
		t.Errorf("suggestions: %d", len(got.Suggestions))
	}
	if got.Urgency != "high" {
		t.Errorf("urgency: %s", got.Urgency)
	}
}

func TestParseClarifyRequested_WrongType(t *testing.T) {
	if got := parseClarifyRequested("s1", "permission_ask", []byte(`{}`)); got != nil {
		t.Errorf("non-clarify event leaked through: %+v", got)
	}
}

func TestParseClarifyRequested_MissingAskID(t *testing.T) {
	if got := parseClarifyRequested("s1", "clarify_requested", []byte(`{"question":"q"}`)); got != nil {
		t.Errorf("missing ask_id should be rejected, got: %+v", got)
	}
}

func TestParseClarifyRequested_BadJSON(t *testing.T) {
	if got := parseClarifyRequested("s1", "clarify_requested", []byte(`{not json}`)); got != nil {
		t.Errorf("bad JSON should be rejected, got: %+v", got)
	}
}

func TestRenderClarifyModal_PickMode(t *testing.T) {
	ask := &pendingClarifyMsg{
		AskID:       "a1",
		Question:    "Deploy now?",
		Context:     "verifier passed",
		Suggestions: []string{"yes, deploy", "no, hold"},
		Urgency:     "high",
	}
	out := renderClarifyModal(ask, &clarifyModalState{mode: clarifyModePick}, 60)
	for _, want := range []string{"Clarify", "Deploy now", "verifier passed", "yes, deploy", "no, hold", "[1]", "[2]", "type a custom"} {
		if !strings.Contains(out, want) {
			t.Errorf("modal missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderClarifyModal_TypeMode(t *testing.T) {
	ask := &pendingClarifyMsg{AskID: "a1", Question: "Q?"}
	st := &clarifyModalState{mode: clarifyModeType, typeBuf: "hello"}
	out := renderClarifyModal(ask, st, 60)
	if !strings.Contains(out, "answer>") {
		t.Errorf("type-mode modal missing answer prompt: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("type-mode modal missing buffer: %s", out)
	}
	if !strings.Contains(out, "[enter] send") {
		t.Errorf("type-mode footer missing: %s", out)
	}
}

func TestRenderClarifyModal_NoSuggestions(t *testing.T) {
	ask := &pendingClarifyMsg{AskID: "a1", Question: "Q?"}
	out := renderClarifyModal(ask, &clarifyModalState{}, 60)
	if !strings.Contains(out, "no suggestions") {
		t.Errorf("expected hint when suggestions empty: %s", out)
	}
}

func TestRenderClarifyModal_VariousWidths(t *testing.T) {
	ask := &pendingClarifyMsg{
		AskID:       "a1",
		Question:    "This is a fairly long question that should wrap nicely without exploding the modal box.",
		Suggestions: []string{"a", "b"},
	}
	for _, w := range []int{30, 60, 100} {
		out := renderClarifyModal(ask, &clarifyModalState{}, w)
		if out == "" {
			t.Errorf("empty render at width %d", w)
		}
	}
}

func TestClarifyKeyToSuggestionIndex(t *testing.T) {
	sugg := []string{"a", "b", "c"}
	cases := []struct {
		k    string
		want int
	}{
		{"1", 0}, {"2", 1}, {"3", 2},
		{"4", -1},      // out of range
		{"0", -1},      // not a 1-based digit
		{"a", -1},      // letter
		{"enter", -1},  // multi-char
		{"", -1},
	}
	for _, c := range cases {
		got := clarifyKeyToSuggestionIndex(c.k, sugg)
		if got != c.want {
			t.Errorf("clarifyKeyToSuggestionIndex(%q) = %d, want %d", c.k, got, c.want)
		}
	}
}

func TestWrapText_NoWrapNeeded(t *testing.T) {
	if got := wrapText("short", 80); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestWrapText_BreaksAtWordBoundary(t *testing.T) {
	got := wrapText("one two three four five six seven", 10)
	if !strings.Contains(got, "\n") {
		t.Errorf("expected wrap, got %q", got)
	}
}

func TestHandleClarifyKey_PickModeNumber(t *testing.T) {
	m := &Model{
		pendingClarify: &pendingClarifyMsg{
			SessionID:   "s1",
			AskID:       "a1",
			Suggestions: []string{"yes", "no"},
		},
	}
	_, cmd, handled := m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if !handled {
		t.Errorf("expected handled=true")
	}
	if cmd == nil {
		t.Errorf("expected answer cmd to be returned")
	}
	if m.pendingClarify != nil {
		t.Errorf("modal should clear after answer")
	}
}

func TestHandleClarifyKey_PickModeT_SwitchesToTypeMode(t *testing.T) {
	m := &Model{
		pendingClarify: &pendingClarifyMsg{AskID: "a1", Suggestions: []string{"x"}},
	}
	_, _, handled := m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !handled {
		t.Errorf("expected handled=true")
	}
	if m.clarifyState.mode != clarifyModeType {
		t.Errorf("mode not switched: %v", m.clarifyState.mode)
	}
	if m.pendingClarify == nil {
		t.Errorf("modal should still be visible while typing")
	}
}

func TestHandleClarifyKey_PickModeEsc_Dismisses(t *testing.T) {
	m := &Model{
		pendingClarify: &pendingClarifyMsg{AskID: "a1"},
	}
	_, _, handled := m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Errorf("expected handled=true")
	}
	if m.pendingClarify != nil {
		t.Errorf("modal should clear on esc")
	}
}

func TestHandleClarifyKey_TypeMode_AppendsAndSends(t *testing.T) {
	m := &Model{
		pendingClarify: &pendingClarifyMsg{AskID: "a1", SessionID: "s1"},
		clarifyState:   clarifyModalState{mode: clarifyModeType},
	}
	// Append two runes.
	_, _, _ = m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	_, _, _ = m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if m.clarifyState.typeBuf != "hi" {
		t.Errorf("buf=%q", m.clarifyState.typeBuf)
	}
	// Backspace one rune.
	_, _, _ = m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.clarifyState.typeBuf != "h" {
		t.Errorf("buf after backspace=%q", m.clarifyState.typeBuf)
	}
	// Enter sends.
	_, cmd, handled := m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Errorf("enter not handled")
	}
	if cmd == nil {
		t.Errorf("enter should return a Cmd")
	}
	if m.pendingClarify != nil {
		t.Errorf("modal should clear after send")
	}
}

func TestHandleClarifyKey_TypeMode_EscReturnsToPick(t *testing.T) {
	m := &Model{
		pendingClarify: &pendingClarifyMsg{AskID: "a1"},
		clarifyState:   clarifyModalState{mode: clarifyModeType, typeBuf: "abc"},
	}
	_, _, handled := m.handleClarifyKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Errorf("esc not handled")
	}
	if m.clarifyState.mode != clarifyModePick {
		t.Errorf("mode should revert to pick: %v", m.clarifyState.mode)
	}
	if m.clarifyState.typeBuf != "" {
		t.Errorf("type buffer should clear: %q", m.clarifyState.typeBuf)
	}
	if m.pendingClarify == nil {
		t.Errorf("modal should still be visible")
	}
}
