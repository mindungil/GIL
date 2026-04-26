package app

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/slash"
	"github.com/jedutools/gil/sdk"
)

// stubSlashState builds a slashState that's wired to an in-memory fetcher
// instead of a real gRPC client, so we can drive the TUI Update loop in
// tests without standing up gild.
func stubSlashState(sessions []*sdk.Session, currentIdx int, m *Model) *slashState {
	st := initSlashState(nil) // nil client → Fetcher returns an error
	// Replace the Fetcher with a synchronous in-memory map.
	st.env.Fetcher = func(ctx context.Context, id string) (*slash.SessionInfo, error) {
		for _, s := range sessions {
			if s.ID == id {
				return &slash.SessionInfo{
					ID:               s.ID,
					Status:           s.Status,
					WorkingDir:       s.WorkingDir,
					GoalHint:         s.GoalHint,
					CurrentIteration: s.CurrentIteration,
					CurrentTokens:    s.CurrentTokens,
				}, nil
			}
		}
		return nil, nil
	}
	if currentIdx >= 0 && currentIdx < len(sessions) {
		st.env.SessionID = sessions[currentIdx].ID
	}
	st.env.Local.ClearEvents = func() { m.events = nil }
	return st
}

func newTestModelWithSlash(sessions []*sdk.Session) *Model {
	m := &Model{
		keys:     DefaultKeys(),
		sessions: sessions,
		width:    80,
		height:   24,
	}
	m.slash = stubSlashState(sessions, 0, m)
	return m
}

// TestSlash_InputModeOpensAndClosesOnEsc checks the bubbletea key plumbing
// that "/" enters input mode and Esc cancels it.
func TestSlash_InputModeOpensAndClosesOnEsc(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})

	// Press "/" → enters input mode.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	require.True(t, m2.(*Model).slash.inputting)

	// Esc cancels.
	m3, _ := m2.(*Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, m3.(*Model).slash.inputting)
}

// TestSlash_HelpDispatchProducesOutput drives /help end-to-end through the
// Update loop and verifies the deferred Cmd resolves into the expected
// slashResultMsg.
func TestSlash_HelpDispatchProducesOutput(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})

	// Open prompt.
	m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "help" {
		m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.Equal(t, "help", m.slash.buffer)

	// Press Enter → returns a Cmd that produces slashResultMsg.
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	msg := cmd()
	out, ok := msg.(slashResultMsg)
	require.True(t, ok, "expected slashResultMsg, got %T", msg)
	require.Contains(t, out.output, "/status")
	require.Contains(t, out.output, "/quit")

	// Feed the message back to Update so the panel becomes visible.
	m3, _ := m2.(*Model).Update(out)
	require.Contains(t, m3.(*Model).slash.output, "/help")
}

// TestSlash_QuitReturnsTeaQuit drives /quit and verifies the resulting
// slashQuitMsg makes Update return tea.Quit.
func TestSlash_QuitReturnsTeaQuit(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})

	m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "quit" {
		m, _ = updateAs(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	msg := cmd()
	_, isQuit := msg.(slashQuitMsg)
	require.True(t, isQuit)

	// Feed it back; Update should now return tea.Quit.
	_, cmd2 := m.Update(msg)
	require.NotNil(t, cmd2)
	_, isQuit2 := cmd2().(tea.QuitMsg)
	require.True(t, isQuit2, "slashQuitMsg should produce tea.Quit")
}

// TestSlash_StatusReportsSessionFromFetcher exercises the gRPC-backed
// /status path against the stub fetcher.
func TestSlash_StatusReportsSessionFromFetcher(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{
		{ID: "s1", Status: "RUNNING", GoalHint: "ship", WorkingDir: "/tmp/w"},
	})
	cmd := dispatchSlashCmd(m, "/status")
	msg := cmd()
	out, ok := msg.(slashResultMsg)
	require.True(t, ok)
	require.Contains(t, out.output, "s1")
	require.Contains(t, out.output, "RUNNING")
	require.Contains(t, out.output, "ship")
}

// TestSlash_ClearWipesEventsBufferOnly proves /clear is local-only —
// it must not call the gRPC client.
func TestSlash_ClearWipesEventsBufferOnly(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	m.events = []string{"a", "b", "c"}
	cmd := dispatchSlashCmd(m, "/clear")
	_ = cmd()
	require.Empty(t, m.events)
}

// TestSlash_UnknownCommandReportsError keeps the "type-os shouldn't crash"
// invariant.
func TestSlash_UnknownCommandReportsError(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	cmd := dispatchSlashCmd(m, "/nopenope")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "unknown command")
}

// TestSlash_ModelHandlerStubbed reflects the ground-rule that /model is a
// suggestion, never a forced switch.
func TestSlash_ModelHandlerStubbed(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	cmd := dispatchSlashCmd(m, "/model gpt-4o")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "gpt-4o")
	require.Contains(t, strings.ToLower(out.output), "hint queued")
}

// TestSlash_CompactStubbed covers the "compact via slash is opt-in only"
// rule for now.
func TestSlash_CompactStubbed(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	cmd := dispatchSlashCmd(m, "/compact")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, strings.ToLower(out.output), "not yet wired")
}

// TestSlash_AgentsHandlerNoFile keeps the no-AGENTS path safe.
func TestSlash_AgentsHandlerNoFile(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	cmd := dispatchSlashCmd(m, "/agents")
	msg := cmd()
	out := msg.(slashResultMsg)
	// Either "no AGENTS.md" or the user's actual file — both are valid.
	if !strings.Contains(out.output, "no AGENTS.md") &&
		!strings.Contains(out.output, "AGENTS.md") {
		t.Fatalf("expected agents output to mention AGENTS.md, got %q", out.output)
	}
}

// TestSlash_DiffNoCheckpoints verifies /diff doesn't crash when the
// session has never produced a checkpoint.
func TestSlash_DiffNoCheckpoints(t *testing.T) {
	dir := t.TempDir()
	m := newTestModelWithSlash([]*sdk.Session{
		{ID: "s1", Status: "RUNNING", WorkingDir: dir},
	})
	cmd := dispatchSlashCmd(m, "/diff")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "no checkpoints")
}

// updateAs is a tiny adapter that asserts the Bubbletea Update return
// is a *Model and unwraps it to keep the test bodies short.
func updateAs(m *Model, msg tea.Msg) (*Model, tea.Cmd) {
	mm, c := m.Update(msg)
	return mm.(*Model), c
}
