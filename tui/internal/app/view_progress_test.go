package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pin Unicode mode for the progress bar tests so glyph counts are
// deterministic regardless of CI locale.
func unicodeOnly(t *testing.T) {
	t.Helper()
	prev := IsAsciiMode()
	SetAsciiMode(false)
	t.Cleanup(func() { SetAsciiMode(prev) })
}

// Strip ANSI by force — NO_COLOR keeps the byte stream clean, which
// makes substring assertions trivial.
func nocolor(t *testing.T) {
	t.Helper()
	prev := IsNoColor()
	SetNoColor(true)
	t.Cleanup(func() { SetNoColor(prev) })
}

func TestRenderProgressBar_Zero(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderProgressBar(0, 12)
	require.Equal(t, strings.Repeat("▱", 12), out)
}

func TestRenderProgressBar_Full(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderProgressBar(1, 12)
	require.Equal(t, strings.Repeat("▰", 12), out)
}

func TestRenderProgressBar_Half(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderProgressBar(0.5, 12)
	// 6 filled + 6 empty.
	require.Equal(t, strings.Repeat("▰", 6)+strings.Repeat("▱", 6), out)
}

func TestRenderProgressBar_SubCellBetweenIntegers(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	// 0.30 → 0.30*12*8 = 28.8 → 29 eighths → 3 full + 5/8 partial = "▋"
	out := renderProgressBar(0.30, 12)
	require.Contains(t, out, "▰▰▰")
	// One of the partial glyphs should be present.
	hasPartial := false
	for _, g := range []string{"▏", "▎", "▍", "▌", "▋", "▊", "▉"} {
		if strings.Contains(out, g) {
			hasPartial = true
			break
		}
	}
	require.True(t, hasPartial, "expected a sub-cell partial glyph in %q", out)
}

func TestRenderProgressBar_Clamp(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderProgressBar(1.7, 8)
	require.Equal(t, strings.Repeat("▰", 8), out)
}

func TestRenderVerifyMatrix_AllStates(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	out := renderVerifyMatrix([]string{"pass", "pass", "fail", "skip"})
	require.Contains(t, out, "✓")
	require.Contains(t, out, "✗")
	require.Contains(t, out, "─")
	require.Contains(t, out, "2 / 4 checks")
}

func TestRenderVerifyMatrix_Empty(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	out := renderVerifyMatrix(nil)
	require.Contains(t, out, "no verification checks")
}

func TestFormatCostUSD(t *testing.T) {
	require.Equal(t, "$0.61", formatCostUSD(0.61))
	require.Equal(t, "$1.20", formatCostUSD(1.2))
	nocolor(t)
	unicodeOnly(t)
	out := formatCostUSD(-1)
	require.Contains(t, out, "$")
}

func TestHumanTokens(t *testing.T) {
	require.Equal(t, "0", humanTokens(0))
	require.Equal(t, "999", humanTokens(999))
	require.Equal(t, "1.2K", humanTokens(1200))
	require.Equal(t, "32.1K", humanTokens(32100))
	require.Equal(t, "1.0M", humanTokens(1_000_000))
}

func TestRenderProgressPane_Composes(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	pd := ProgressData{
		Goal:          "Add dark mode to web frontend",
		Iter:          23,
		MaxIter:       100,
		VerifyResults: []string{"pass", "pass", "pass", "fail", "skip", ""},
		TokensIn:      32100,
		TokensOut:     8400,
		CostUSD:       0.61,
		Autonomy:      "ASK_DESTRUCTIVE",
	}
	out := renderProgressPane(80, pd)
	require.Contains(t, out, "Add dark mode to web frontend")
	require.Contains(t, out, "23 / 100")
	require.Contains(t, out, "✓")
	require.Contains(t, out, "32.1K in / 8.4K out")
	require.Contains(t, out, "$0.61")
	require.Contains(t, out, "ASK_DESTRUCTIVE")
}

func TestRenderProgressPane_StuckActive(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	pd := ProgressData{
		Goal:          "x",
		MaxIter:       100,
		StuckPattern:  "RepeatedAction",
		StuckRecovery: "AltToolOrder",
	}
	out := renderProgressPane(80, pd)
	require.Contains(t, out, "RepeatedAction")
	require.Contains(t, out, "AltToolOrder")
	require.Contains(t, out, "⚠")
}

func TestRenderProgressPane_StuckExhausted(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	pd := ProgressData{
		MaxIter:        100,
		StuckPattern:   "RepeatedAction",
		StuckExhausted: true,
	}
	out := renderProgressPane(80, pd)
	require.Contains(t, out, "exhausted")
}
