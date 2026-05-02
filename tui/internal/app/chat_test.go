package app

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
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

func TestChatSession_PreFirstTurn_RendersList(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.sessions = []*sdk.Session{
		{ID: "01HQXYZ001", GoalHint: "add dark mode", Status: "INTERVIEWING", CreatedAt: time.Now().Add(-4 * time.Minute)},
		{ID: "01HQXYZ002", GoalHint: "fix oauth", Status: "DONE", CreatedAt: time.Now().Add(-2 * time.Hour)},
	}

	got := m.renderPreFirstTurn(20)

	if !strings.Contains(got, "2 past sessions") {
		t.Errorf("missing lead-in: %q", got)
	}
	if !strings.Contains(got, "add dark mode") {
		t.Errorf("session 1 not rendered: %q", got)
	}
	if !strings.Contains(got, "fix oauth") {
		t.Errorf("session 2 not rendered: %q", got)
	}
}

func TestChatSession_NoSessions_FallsBackToInvite(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32

	got := m.renderPreFirstTurn(20)
	if !strings.Contains(got, "no past sessions") {
		t.Errorf("expected empty-state invite, got %q", got)
	}
}

func TestChatSession_FormatRow_KoreanSlugDoesNotSplitRune(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	// 35-rune Korean slug; byte-length is ~105.
	long := strings.Repeat("가", 35)
	s := &sdk.Session{ID: "01HQ", GoalHint: long, Status: "INTERVIEWING", CreatedAt: time.Now()}
	row := formatChatSessionRow(s, 100)
	if !utf8.ValidString(row) {
		t.Fatalf("row contains invalid UTF-8: %q", row)
	}
}

func TestChatSession_FormatRow_NarrowMode(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	s := &sdk.Session{ID: "01HQ", GoalHint: "add dark mode toggle to settings page", Status: "DONE", CreatedAt: time.Now()}
	wide := formatChatSessionRow(s, 100)
	narrow := formatChatSessionRow(s, 70) // < 80 → narrow branch
	if wide == narrow {
		t.Fatalf("expected narrow rendering at width 70, got identical strings")
	}
	// Narrow mode drops phase column.
	if strings.Contains(narrow, "done") {
		t.Fatalf("narrow row should not include phase column, got %q", narrow)
	}
}

func TestChatUpdate_EnterSubmitsAndAppendsToTranscript(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.input.ti.SetValue("hi there")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// Buffer cleared.
	if cm.input.ti.Value() != "" {
		t.Fatalf("buffer should clear on submit, got %q", cm.input.ti.Value())
	}
	// Transcript has the user line.
	if len(cm.transcript) == 0 || !strings.Contains(cm.transcript[len(cm.transcript)-1], "hi there") {
		t.Fatalf("transcript missing user line: %v", cm.transcript)
	}
	// firstTurnDone flips on first submit.
	if !cm.firstTurnDone {
		t.Fatalf("expected firstTurnDone=true after first submit")
	}
}

func TestChatUpdate_CtrlC_ReturnsQuitCmd(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("Ctrl+C should return a Quit cmd")
	}
}

func TestChatUpdate_SlashQuit_ReturnsQuitCmd(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ti.SetValue("/quit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("/quit should return a Quit cmd")
	}
}

func TestChatUpdate_Up_RecallsHistory(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.history = []string{"first", "second"}
	m.input.histIdx = -1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	cm := updated.(*chatModel)
	if cm.input.ti.Value() != "second" {
		t.Fatalf("up → want \"second\", got %q", cm.input.ti.Value())
	}
}
