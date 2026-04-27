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

// Budget meter coverage — verifies the row shape under each band:
//
//   0%   plain "$0.00 / $5.00 ▱▱▱▱▱▱▱▱▱▱ 0%" (no warning glyph)
//   50%  plain bar + dim percentage, no warning text
//   80%  amber percentage + "approaching limit" warn text
//   100% coral fill + "EXHAUSTED" alert text
//
// The 75% threshold is the same warning band as 80% so we don't
// duplicate; the 100% case requires BudgetExceeded sticky bit because
// the runner sets it as part of the budget_exceeded event payload.

func TestRenderCostRow_NoBudget_FallsBackToBareValue(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	out := renderCostRow(ProgressData{CostUSD: 0.61})
	require.Equal(t, "$0.61", out)
}

func TestRenderCostRow_Budget_ZeroPercent(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	pd := ProgressData{CostUSD: 0.00, CostBudget: 5.00}
	out := renderCostRow(pd)
	require.Contains(t, out, "$0.00 / $5.00")
	require.Contains(t, out, "0%")
	require.NotContains(t, out, "approaching")
	require.NotContains(t, out, "EXHAUSTED")
}

func TestRenderCostRow_Budget_FiftyPercent(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	pd := ProgressData{CostUSD: 2.50, CostBudget: 5.00}
	out := renderCostRow(pd)
	require.Contains(t, out, "$2.50 / $5.00")
	require.Contains(t, out, "50%")
	require.NotContains(t, out, "approaching")
}

func TestRenderCostRow_Budget_EightyPercent_ShowsWarning(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	pd := ProgressData{CostUSD: 4.00, CostBudget: 5.00}
	out := renderCostRow(pd)
	require.Contains(t, out, "$4.00 / $5.00")
	require.Contains(t, out, "80%")
	require.Contains(t, out, "approaching limit")
	require.Contains(t, out, "⚠")
}

func TestRenderCostRow_Budget_ExceededShowsAlert(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	pd := ProgressData{
		CostUSD: 5.02, CostBudget: 5.00,
		BudgetExceeded: true,
		BudgetReason:   "cost",
	}
	out := renderCostRow(pd)
	require.Contains(t, out, "EXHAUSTED")
	require.Contains(t, out, "✗")
	require.NotContains(t, out, "approaching", "amber+coral never both at once per spec")
}

func TestRenderTokensRow_NoBudget_FallsBackToPair(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)
	out := renderTokensRow(ProgressData{TokensIn: 32100, TokensOut: 8400})
	require.Equal(t, "32.1K in / 8.4K out", out)
}

func TestRenderTokensRow_Budget_PercentBands(t *testing.T) {
	nocolor(t)
	unicodeOnly(t)

	// 50% — no warning
	pd := ProgressData{TokensIn: 25_000, TokensOut: 25_000, TokensBudget: 100_000}
	out := renderTokensRow(pd)
	require.Contains(t, out, "50%")
	require.NotContains(t, out, "approaching")

	// 80% — amber warning
	pd = ProgressData{TokensIn: 40_000, TokensOut: 40_000, TokensBudget: 100_000}
	out = renderTokensRow(pd)
	require.Contains(t, out, "80%")
	require.Contains(t, out, "approaching limit")

	// 100%+ — coral exhaust
	pd = ProgressData{
		TokensIn: 60_000, TokensOut: 50_000, TokensBudget: 100_000,
		BudgetExceeded: true,
		BudgetReason:   "tokens",
	}
	out = renderTokensRow(pd)
	require.Contains(t, out, "EXHAUSTED")
}

func TestRenderProgressPane_BudgetVisibleAndOnlyOneSeverity(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	// Cost is exhausted; tokens are at 80% — both severity bands would
	// otherwise compete on the same screen. Per spec the higher severity
	// (coral) wins on the cost row, while the tokens row stays amber.
	pd := ProgressData{
		Goal:           "Add dark mode",
		Iter:           50,
		MaxIter:        100,
		TokensIn:       40_000,
		TokensOut:      40_000,
		TokensBudget:   100_000,
		CostUSD:        5.02,
		CostBudget:     5.00,
		BudgetExceeded: true,
		BudgetReason:   "cost",
		Autonomy:       "ASK_DESTRUCTIVE",
	}
	out := renderProgressPane(80, pd)
	require.Contains(t, out, "EXHAUSTED", "cost row in alert")
	require.Contains(t, out, "approaching limit", "tokens row in caution")
}

func TestParseBudgetEvent_TokensAndCost(t *testing.T) {
	r, used, lim := parseBudgetEvent([]byte(`{"reason":"tokens","used":750,"limit":1000,"fraction":0.75}`))
	require.Equal(t, "tokens", r)
	require.Equal(t, 750.0, used)
	require.Equal(t, 1000.0, lim)

	r, used, lim = parseBudgetEvent([]byte(`{"reason":"cost","used":3.85,"limit":5.0,"fraction":0.77}`))
	require.Equal(t, "cost", r)
	require.InDelta(t, 3.85, used, 0.001)
	require.Equal(t, 5.0, lim)

	r, used, lim = parseBudgetEvent(nil)
	require.Equal(t, "", r)
	require.Equal(t, 0.0, used)
	require.Equal(t, 0.0, lim)
}
