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

func TestRenderSummary_BudgetCellRendersUsageVsTotal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0",
		User:    "u", Host: "h",
		Now:     time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01AAA", Status: "RUNNING", Iter: 23, MaxIter: 100,
				CostUSD: 0.61, CostBudget: 5.00, Goal: "Add dark mode"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "$0.61 / $5.00", "budget cell shows used/total")
}

func TestRenderSummary_BudgetWarningGlyphAt75(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0", User: "u", Host: "h",
		Now:     time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01BBB", Status: "RUNNING", Iter: 75, MaxIter: 100,
				CostUSD: 3.85, CostBudget: 5.00, Goal: "Long task"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "$3.85 / $5.00")
	// Warn glyph (⚠) prefixes the cell when frac >= 0.75
	require.Contains(t, out, "⚠")
}

func TestRenderSummary_BudgetExhaustedShowsAlert(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0", User: "u", Host: "h",
		Now:     time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01CCC", Status: "STOPPED", Iter: 17, MaxIter: 50,
				CostUSD: 5.02, CostBudget: 5.00,
				BudgetExceeded: true, Goal: "Hit cap"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "$5.02 / $5.00")
	require.Contains(t, out, "✗", "alert glyph when budget exhausted")
}

func TestRenderSummary_NoBudget_KeepsBareValue(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderSummary(&buf, summaryEnv{
		Version: "v0.1.0", User: "u", Host: "h",
		Now:     time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		Glyphs:  uistyle.NewGlyphs(false),
		Palette: uistyle.NewPalette(true),
		Sessions: []summaryRow{
			{ID: "01DDD", Status: "RUNNING", Iter: 5, MaxIter: 100,
				CostUSD: 0.42, Goal: "No budget"},
		},
	})
	out := buf.String()
	require.Contains(t, out, "$0.42")
	require.NotContains(t, out, " / $", "no budget → no slash form")
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
