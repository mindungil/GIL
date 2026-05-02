package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChatModel_InitialPhase_IsIdle(t *testing.T) {
	m := newChatModelForTest()
	if m.phase != ChatPhaseIdle {
		t.Fatalf("expected initial phase ChatPhaseIdle, got %v", m.phase)
	}
}

func TestChatModel_WindowSize_StoresDimensions(t *testing.T) {
	m := newChatModelForTest()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	cm := updated.(*chatModel)
	if cm.width != 100 || cm.height != 32 {
		t.Fatalf("expected width=100 height=32, got width=%d height=%d", cm.width, cm.height)
	}
}

// newChatModelForTest constructs a chatModel without dialing the daemon.
// All gRPC-dependent fields are left nil; tests that need them inject
// their own fake stream.
func newChatModelForTest() *chatModel {
	return &chatModel{phase: ChatPhaseIdle}
}

func TestChatInput_NewActive_HasFocus(t *testing.T) {
	in := newChatInput()
	if !in.ti.Focused() {
		t.Fatalf("new chat input should be focused")
	}
}

func TestChatInput_SubmitClearsBuffer(t *testing.T) {
	in := newChatInput()
	in.ti.SetValue("hello there")
	got := in.submit()
	if got != "hello there" {
		t.Fatalf("submit returned %q, want %q", got, "hello there")
	}
	if in.ti.Value() != "" {
		t.Fatalf("buffer should be cleared after submit, got %q", in.ti.Value())
	}
	if len(in.history) != 1 || in.history[0] != "hello there" {
		t.Fatalf("history not appended: %v", in.history)
	}
}

func TestChatInput_HistoryNavigation_UpDown(t *testing.T) {
	in := newChatInput()
	in.ti.SetValue("first")
	in.submit()
	in.ti.SetValue("second")
	in.submit()
	in.historyUp()
	if v := in.ti.Value(); v != "second" {
		t.Fatalf("up once → want \"second\", got %q", v)
	}
	in.historyUp()
	if v := in.ti.Value(); v != "first" {
		t.Fatalf("up twice → want \"first\", got %q", v)
	}
	in.historyDown()
	if v := in.ti.Value(); v != "second" {
		t.Fatalf("down → want \"second\", got %q", v)
	}
	in.historyDown()
	if v := in.ti.Value(); v != "" {
		t.Fatalf("down past end → want empty, got %q", v)
	}
}

func TestChatView_RendersAllRegions_AtFullSize(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.phase = ChatPhaseIdle

	out := m.View()

	if !strings.Contains(out, "G  I  L") && !strings.Contains(out, "G I L") {
		t.Errorf("header missing logo: %q", out)
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("status strip missing phase: %q", out)
	}
	if !strings.Contains(out, "describe a task") {
		t.Errorf("affordance subtitle missing: %q", out)
	}
	if !strings.Contains(out, "═") && !strings.Contains(out, "=") {
		t.Errorf("prompt panel border missing: %q", out)
	}
}

func TestChatView_NarrowMode_DegradesGracefully(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 50
	m.height = 18
	m.phase = ChatPhaseIdle

	out := m.View()
	if out == "" {
		t.Fatalf("narrow mode produced empty view")
	}
}
