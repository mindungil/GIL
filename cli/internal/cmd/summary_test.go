package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
)

// renderSummary is the unit-of-truth for the no-arg surface; we test
// it directly with a fixed env so the assertions don't have to model
// gild over a UDS. The flow is: build the env, render to a buffer,
// assert on substrings + structure.

func TestRenderSummary_ZeroSessions(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version:  "v0.1.0-alpha",
		User:     "test",
		Host:     "example",
		Now:      time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC),
		Glyphs:   uistyle.NewGlyphs(false),
		Palette:  uistyle.NewPalette(true),
		Sessions: nil,
	})
	out := buf.String()
	require.Contains(t, out, "G I L", "header should be letterspaced")
	require.Contains(t, out, "v0.1.0-alpha")
	require.Contains(t, out, "No sessions yet.")
	require.Contains(t, out, "gil interview")
	require.Contains(t, out, "gil --help")
	require.Contains(t, out, "gil doctor")
}

func TestRenderSummary_OneSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0",
		User:    "u",
		Host:    "h",
		Now:     time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01ABCDEFGH", Status: "RUNNING", Iter: 23, MaxIter: 100, CostUSD: 0.61, Goal: "Add dark mode"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "1 session", "singular noun for one session")
	require.Contains(t, out, "01abcd")          // short ULID
	require.Contains(t, out, "23/100")          // RUNNING shows denominator
	require.Contains(t, out, "$0.61")
	require.Contains(t, out, "Add dark mode")
}

func TestRenderSummary_ThreeSessions_StuckAnnotated(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0",
		User:    "u",
		Host:    "h",
		Now:     time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01AAA", Status: "RUNNING", Iter: 23, MaxIter: 100, CostUSD: 0.61, Goal: "A"},
			{ID: "01BBB", Status: "DONE", Iter: 45, CostUSD: 1.20, Goal: "B"},
			{ID: "01CCC", Status: "STUCK", Iter: 12, CostUSD: 0.32, Goal: "C", StuckNote: "RepeatedAction (2/3)"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "3 sessions")
	require.Contains(t, out, "STUCK · RepeatedAction (2/3)")
	// DONE row should NOT show denominator; just the integer iter.
	// DONE row should NOT show denominator. shortID lowercases
	// for column-width consistency.
	require.Regexp(t, `01bbb.*45 `, out)
}

func TestRenderSummary_AsciiFallback(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0",
		User:    "u",
		Host:    "h",
		Now:     time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(true),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01AAA", Status: "RUNNING", Iter: 5, MaxIter: 100, Goal: "x"},
		},
	})
	out := buf.String()
	// ASCII bar chars — '#' and '.', not Unicode blocks.
	require.True(t, strings.ContainsAny(out, "#."), "expected ASCII bar chars")
	require.NotContains(t, out, "▰")
	require.NotContains(t, out, "▱")
	require.NotContains(t, out, "●")
}
