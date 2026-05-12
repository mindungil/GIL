package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

// TestSnapshot_FullTUIAt100x40 renders the full mission-control TUI at
// 100x40 with a small mock dataset and checks the structural anchors:
// pane titles, footer keys, status glyphs. We deliberately do NOT
// snapshot byte-for-byte because lipgloss output depends on terminal
// capability detection (truecolor vs 256-color) — we'd be testing the
// terminal env, not the TUI.
//
// Per spec §1, max 2 accent colors visible per screen. We don't enforce
// that programmatically here (it's a soft budget), but the structural
// anchors below double as a regression net for unintended pane drift.
func TestSnapshot_FullTUIAt100x40(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)
	prevC := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevC)

	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{
			{ID: "abc12345", Status: "RUNNING", GoalHint: "Add dark mode to web frontend", CurrentIteration: 23, CurrentTokens: 32100},
			{ID: "def45678", Status: "DONE", GoalHint: "Implement OAuth login", CurrentIteration: 45},
			{ID: "ghi78901", Status: "STUCK", GoalHint: "Fix flaky test", CurrentIteration: 12},
		},
		width:          100,
		height:         40,
		activityFilter: FilterMilestones,
	}
	out := m.View()

	// Header
	require.Contains(t, out, "G I L")
	// Sessions pane title
	require.Contains(t, out, "Sessions")
	// Spec & Progress pane title (truncated by injectTitle but must appear)
	require.True(t,
		strings.Contains(out, "Spec & Progress") ||
			strings.Contains(out, "Spec"),
		"expected Spec & Progress pane title in:\n%s", out)
	// Activity pane title (with filter suffix)
	require.True(t,
		strings.Contains(out, "Activity") || strings.Contains(out, "Activit"),
		"expected Activity pane title in:\n%s", out)
	// Memory pane title
	require.Contains(t, out, "Memory")
	// Goal of selected session
	require.Contains(t, out, "Add dark mode")
	// Status glyphs (one of the rounded-status glyphs)
	g := Glyphs()
	require.Contains(t, out, g.Running)
	require.Contains(t, out, g.Done)
	require.Contains(t, out, g.Warn)
	// Footer keys
	require.Contains(t, out, "q quit")
	require.Contains(t, out, "c checkpoints")
	require.Contains(t, out, "t toggle")
}

func TestSnapshot_NarrowMode_HidesSessionsPane(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)
	prevC := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevC)

	m := &Model{
		keys:   DefaultKeys(),
		sessions: []*sdk.Session{
			{ID: "abc12345", Status: "RUNNING", GoalHint: "Add dark mode", CurrentIteration: 1},
		},
		width:  60, // < 80 → narrow
		height: 30,
	}
	out := m.View()
	// Sessions pane title is suppressed in narrow mode.
	require.NotContains(t, out, "Sessions")
	// Goal still rendered in the main column.
	require.Contains(t, out, "Add dark mode")
}

func TestSnapshot_AsciiMode(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(true)
	defer SetAsciiMode(prev)
	prevC := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevC)

	m := &Model{
		keys: DefaultKeys(),
		sessions: []*sdk.Session{
			{ID: "abc12345", Status: "RUNNING", GoalHint: "Add dark mode", CurrentIteration: 1},
		},
		width:  100,
		height: 30,
	}
	out := m.View()
	// In ASCII mode the running glyph is "*", not "●".
	require.Contains(t, out, "*")
	require.NotContains(t, out, "●")
	// Bar empty cell is "." in ASCII fallback.
	require.Contains(t, out, ".")
}
