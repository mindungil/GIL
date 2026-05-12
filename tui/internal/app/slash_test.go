package app

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/slash"
	"github.com/mindungil/gil/sdk"
)

// stubRunControl is a slash.RunControl that records what the surface
// sent and returns canned responses. Used by /model, /compact, /diff
// tests to drive the new RunService-backed slash paths without
// standing up gild.
type stubRunControl struct {
	compactQueued bool
	compactReason string
	compactErr    error
	compactCalls  []string

	hintPosted bool
	hintReason string
	hintErr    error
	hintCalls  []map[string]string

	diffResult *slash.DiffResult
	diffErr    error
}

func (s *stubRunControl) RequestCompact(_ context.Context, sessionID string) (bool, string, error) {
	s.compactCalls = append(s.compactCalls, sessionID)
	return s.compactQueued, s.compactReason, s.compactErr
}

func (s *stubRunControl) PostHint(_ context.Context, _ string, hint map[string]string) (bool, string, error) {
	cp := make(map[string]string, len(hint))
	for k, v := range hint {
		cp[k] = v
	}
	s.hintCalls = append(s.hintCalls, cp)
	return s.hintPosted, s.hintReason, s.hintErr
}

func (s *stubRunControl) Diff(_ context.Context, _ string) (*slash.DiffResult, error) {
	return s.diffResult, s.diffErr
}

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

// TestSlash_ModelHandlerForwardsHint exercises the RunControl-backed
// /model path: the surface adapter routes the hint through the
// PostHint RPC and reports the agent will consider it. Ground-rule
// preserved: a hint is a suggestion, never a forced switch.
func TestSlash_ModelHandlerForwardsHint(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	rc := &stubRunControl{hintPosted: true}
	m.slash.env.Run = rc
	cmd := dispatchSlashCmd(m, "/model gpt-4o")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "gpt-4o")
	require.Contains(t, strings.ToLower(out.output), "model hint posted")
	require.Len(t, rc.hintCalls, 1)
	require.Equal(t, "gpt-4o", rc.hintCalls[0]["model"])
}

// TestSlash_ModelHandlerNoRunInFlight asserts the friendly fallback
// when the daemon reports posted=false (e.g. no run is active).
func TestSlash_ModelHandlerNoRunInFlight(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	m.slash.env.Run = &stubRunControl{hintPosted: false, hintReason: "no run in flight"}
	cmd := dispatchSlashCmd(m, "/model haiku")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "no run in flight")
}

// TestSlash_CompactQueued covers the happy path: RequestCompact
// reports queued=true and the surface tells the user the next turn
// boundary will compact.
func TestSlash_CompactQueued(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	rc := &stubRunControl{compactQueued: true}
	m.slash.env.Run = rc
	cmd := dispatchSlashCmd(m, "/compact")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "compact requested for next turn boundary")
	require.Equal(t, []string{"s1"}, rc.compactCalls)
}

// TestSlash_CompactNoRunInFlight covers the falls-back-to-reason path
// — the slash command must not crash when no run is active, just
// surface the daemon's "no run in flight" reason.
func TestSlash_CompactNoRunInFlight(t *testing.T) {
	m := newTestModelWithSlash([]*sdk.Session{{ID: "s1", Status: "RUNNING"}})
	m.slash.env.Run = &stubRunControl{compactQueued: false, compactReason: "no run in flight"}
	cmd := dispatchSlashCmd(m, "/compact")
	msg := cmd()
	out := msg.(slashResultMsg)
	require.Contains(t, out.output, "no run in flight")
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
