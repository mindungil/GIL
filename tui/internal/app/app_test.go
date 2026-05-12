package app

import (
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

// Smoke test the model logic without spinning a real gild.
func TestModel_KeyHandling_NavigatesAndQuits(t *testing.T) {
	// Build a Model with a fake session list (skip Dial)
	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{
			{ID: "a", Status: "FROZEN", GoalHint: "first"},
			{ID: "b", Status: "RUNNING", GoalHint: "second"},
			{ID: "c", Status: "DONE", GoalHint: "third"},
		},
		width: 80, height: 24,
	}
	// Down arrow → selectedIdx 1
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 1, m2.(*Model).selectedIdx)
	// Down again → 2
	m3, _ := m2.(*Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 2, m3.(*Model).selectedIdx)
	// Down at end → stays at 2
	m4, _ := m3.(*Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 2, m4.(*Model).selectedIdx)
	// Up → 1
	m5, _ := m4.(*Model).Update(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, 1, m5.(*Model).selectedIdx)

	// 'q' returns tea.Quit cmd
	_, cmd := m5.(*Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	require.True(t, isQuit, "q should produce tea.QuitMsg")
}

func TestModel_View_RendersWithoutCrashing(t *testing.T) {
	m := &Model{
		keys:     DefaultKeys(),
		sessions: []*sdk.Session{{ID: "x", Status: "FROZEN", GoalHint: "test"}},
		width:    80,
		height:   24,
	}
	out := m.View()
	require.Contains(t, out, "Sessions")
	require.Contains(t, out, "x")
	require.Contains(t, out, "test")
}

func TestModel_View_EmptySessions(t *testing.T) {
	m := &Model{keys: DefaultKeys(), width: 80, height: 24}
	out := m.View()
	require.Contains(t, out, "no sessions")
}

func TestTruncate(t *testing.T) {
	// Pin to Unicode so the canonical "…" ellipsis is exercised; ASCII
	// mode is covered separately in glyph_test.go.
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	require.Equal(t, "abc", truncate("abc", 10))
	require.Equal(t, "abcdefghi…", truncate("abcdefghijk", 10))
	require.Equal(t, "abc", truncate("abcdef", 3))
}

func TestTruncate_RuneSafety(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	// Each Korean syllable is 3 bytes / 1 rune. Byte-cut would split one
	// mid-sequence and emit invalid UTF-8.
	got := truncate("가나다라마바사", 5)
	require.True(t, utf8.ValidString(got), "truncate must produce valid UTF-8: got %q", got)

	// Mixed ASCII + arrow + Korean — bug seen in the affordance line.
	got = truncate("history ↑↓ next 가나다", 12)
	require.True(t, utf8.ValidString(got), "truncate of mixed string must stay valid UTF-8: %q", got)
}

func TestDefaultSocket_NotEmpty(t *testing.T) {
	s := DefaultSocket()
	require.NotEmpty(t, s)
}
