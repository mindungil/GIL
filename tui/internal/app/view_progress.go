package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseVerifyChecks turns a verify_result event payload into the
// per-check pass/fail/skip slice the view consumes. Accepts both the
// terse form ({"checks":[{"pass":true}, …]}) and a flat
// {"results":["pass","fail",…]} form for compatibility with future
// emitters.
func parseVerifyChecks(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var d struct {
		Checks []struct {
			Pass   *bool  `json:"pass"`
			Status string `json:"status"`
		} `json:"checks"`
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if len(d.Results) > 0 {
		return d.Results
	}
	out := make([]string, 0, len(d.Checks))
	for _, c := range d.Checks {
		switch {
		case c.Pass != nil && *c.Pass:
			out = append(out, "pass")
		case c.Pass != nil && !*c.Pass:
			out = append(out, "fail")
		case c.Status != "":
			out = append(out, c.Status)
		default:
			out = append(out, "")
		}
	}
	return out
}

// parseBudgetEvent extracts (reason, used, limit) from a budget_warning
// or budget_exceeded payload. The runner emits "tokens" with int64
// counters and "cost" with float64 USD; we read both as float64 so a
// single helper covers both shapes (cost.used is e.g. 0.61, tokens.used
// is e.g. 75000, both round-trip cleanly through JSON's number type).
//
// Returns ("", 0, 0) on parse failure so callers can ignore malformed
// payloads silently — a missing budget event is preferable to a
// half-rendered meter pointing at nonsense numbers.
func parseBudgetEvent(raw []byte) (reason string, used, limit float64) {
	if len(raw) == 0 {
		return "", 0, 0
	}
	var d struct {
		Reason string  `json:"reason"`
		Used   float64 `json:"used"`
		Limit  float64 `json:"limit"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", 0, 0
	}
	return d.Reason, d.Used, d.Limit
}

// applyBudget folds a budget event into the ProgressData. The reason
// dispatches to the right pair of fields; we deliberately copy `used`
// onto the live counter as well so the meter renders even between
// iteration_start events (the session row's CurrentTokens lags by one
// stream tick).
func applyBudget(pd *ProgressData, reason string, used, limit float64, exceeded bool) {
	switch reason {
	case "tokens":
		if limit > 0 {
			pd.TokensBudget = int64(limit)
		}
		// Don't overwrite the live in/out split — the row helper sums
		// them. Just make sure the totals are at least as high as the
		// reported `used` so the meter doesn't snap backwards if events
		// land out of order.
		if int64(used) > pd.TokensIn+pd.TokensOut {
			pd.TokensIn = int64(used) // accumulate as input-side; sum is what the renderer cares about
			pd.TokensOut = 0
		}
	case "cost":
		if limit > 0 {
			pd.CostBudget = limit
		}
		if used > pd.CostUSD {
			pd.CostUSD = used
		}
	}
	if exceeded {
		pd.BudgetExceeded = true
	}
}

// parseStuckPattern returns the "pattern" field from a stuck_detected.
func parseStuckPattern(raw []byte) string {
	var d struct {
		Pattern string `json:"pattern"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Pattern
}

// parseStuckStrategy returns the "strategy" field from stuck_recovered.
func parseStuckStrategy(raw []byte) string {
	var d struct {
		Strategy string `json:"strategy"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Strategy
}

// renderProgressBar draws an N-cell progress bar with sub-cell smoothing
// per spec §3 (Iconography → Progress fill) and §8 (Motion → Progress
// bar). value is a 0..1 fraction; cells is the integer cell count.
//
// The integer part fills with `▰`; the fractional remainder uses one of
// the eighths (`▏▎▍▌▋▊▉`) to approximate sub-cell positioning; the rest
// is `▱`. value is clamped to [0,1].
//
// Coloring: filled portion (integer + partial) uses success accent;
// empty cells use frame/dim. For 100% the bar is fully success-colored.
func renderProgressBar(value float64, cells int) string {
	if cells <= 0 {
		return ""
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	g := Glyphs()

	totalEighths := int(value*float64(cells)*8 + 0.5)
	fullCells := totalEighths / 8
	remainder := totalEighths % 8
	if fullCells > cells {
		fullCells = cells
		remainder = 0
	}
	emptyCells := cells - fullCells
	if remainder > 0 {
		emptyCells--
	}
	if emptyCells < 0 {
		emptyCells = 0
	}

	var sb strings.Builder
	if fullCells > 0 {
		sb.WriteString(styleSuccess(strings.Repeat(g.BarFill, fullCells)))
	}
	if remainder > 0 {
		// ASCII fallback partials are mostly empty; only add when the
		// glyph is non-trivial (avoids a stray space in Unicode mode
		// for remainder=0, which we already short-circuit above).
		p := g.BarPartial[remainder]
		if p == " " || p == "" {
			// Treat as empty cell — leave it for the dim repeat below.
			emptyCells++
		} else {
			sb.WriteString(styleSuccess(p))
		}
	}
	if emptyCells > 0 {
		sb.WriteString(styleDim(strings.Repeat(g.BarEmpty, emptyCells)))
	}
	return sb.String()
}

// renderVerifyMatrix draws "✓ ✓ ✓ ✗ ─ ─" per spec §6, with one glyph
// per check. results is a slice where each element is one of
// "pass" / "fail" / "skip" / "" (unknown). Returns the styled string
// followed by " N / M checks" tally.
//
// Layout: glyphs separated by single spaces. The summary tally is dim.
func renderVerifyMatrix(results []string) string {
	g := Glyphs()
	if len(results) == 0 {
		return styleDim(g.HSep + " (no verification checks)")
	}
	var glyphs []string
	pass := 0
	for _, r := range results {
		switch r {
		case "pass":
			glyphs = append(glyphs, styleSuccess(g.Done))
			pass++
		case "fail":
			glyphs = append(glyphs, styleAlert(g.Failed))
		default:
			glyphs = append(glyphs, styleDim(g.HSep))
		}
	}
	tally := styleDim(fmt.Sprintf("%d / %d checks", pass, len(results)))
	return strings.Join(glyphs, " ") + "  " + tally
}

// formatCostUSD renders a float USD cost as "$0.61" (2 decimals) or
// "$1.20" — never scientific notation. Negative or NaN inputs become
// "$—" (dim em-dash) so a missing value reads correctly.
func formatCostUSD(usd float64) string {
	if usd < 0 || usd != usd { // NaN
		return styleDim("$" + Glyphs().HSep)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// formatTokensPair renders "32.1K in / 8.4K out" with thousands shortcut.
func formatTokensPair(in, out int64) string {
	return fmt.Sprintf("%s in / %s out", humanTokens(in), humanTokens(out))
}

// humanTokens is "1.2K" / "12.3K" / "1.2M" — keeps things narrow enough
// for the right pane. <1000 prints as a plain integer.
func humanTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000.0)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	}
}

// renderBudgetMeter renders the "<used> / <total>   <bar>   <pct>%"
// suffix used by the Tokens and Cost rows when the spec set a cap on
// that dimension. The bar fill / percentage text shifts color at the
// 75% (caution → amber) and 100% (alert → coral) thresholds per
// terminal-aesthetic.md §1; when both severities apply only the
// higher one is shown (caller passes exceeded=true to force coral).
//
// The 10-cell bar mirrors the spec mockup width and is intentionally
// shorter than the Progress row's 12 cells so the eye reads them as
// distinct meters rather than a stacked total.
func renderBudgetMeter(used, total string, frac float64, exceeded bool) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	bar := renderProgressBar(frac, 10)
	pct := fmt.Sprintf("%d%%", int(frac*100+0.5))
	g := Glyphs()

	// Severity selection: exceeded wins over warning so the same row
	// never carries both ⚠ and ✗ (spec aesthetic budget: "never both at
	// once"). The bar is recoloured by overwriting it with the alert
	// palette when exceeded.
	switch {
	case exceeded:
		// Recolor: build a fresh fully-filled coral bar so the user sees
		// the run stopped rather than a half-filled "still going" bar.
		bar = styleAlert(strings.Repeat(g.BarFill, 10))
		pct = styleAlert(pct)
		tail := "  " + styleAlert(g.Failed+" EXHAUSTED — run stopped")
		return fmt.Sprintf("%s / %s   %s   %s%s", used, total, bar, pct, tail)
	case frac >= 0.75:
		// Warning: keep the success-colored bar (it's a normal in-progress
		// run) but tint the percentage + add an amber suffix so a
		// peripheral glance picks up the approaching limit.
		pct = styleCaution(pct)
		tail := "  " + styleCaution(g.Warn+" approaching limit")
		return fmt.Sprintf("%s / %s   %s   %s%s", used, total, bar, pct, tail)
	default:
		return fmt.Sprintf("%s / %s   %s   %s", used, total, bar, styleDim(pct))
	}
}

// renderTokensRow returns the right-hand cell for the Tokens row. When
// no token budget is set, falls back to the legacy "in / out" pair so
// the row is unchanged for the no-budget case (the more common path).
func renderTokensRow(p ProgressData) string {
	base := formatTokensPair(p.TokensIn, p.TokensOut)
	if p.TokensBudget <= 0 {
		return base
	}
	used := p.TokensIn + p.TokensOut
	frac := float64(used) / float64(p.TokensBudget)
	usedStr := humanTokens(used)
	totStr := humanTokens(p.TokensBudget)
	exceeded := p.BudgetExceeded && p.BudgetReason == "tokens"
	return renderBudgetMeter(usedStr, totStr, frac, exceeded)
}

// renderCostRow returns the right-hand cell for the Cost row. When no
// cost budget is set, falls back to the bare formatted value so the
// existing layout (and Phase-14 cost trend in `gil watch`) is unchanged
// for the no-budget case.
func renderCostRow(p ProgressData) string {
	base := formatCostUSD(p.CostUSD)
	if p.CostBudget <= 0 {
		return base
	}
	frac := p.CostUSD / p.CostBudget
	usedStr := formatCostUSD(p.CostUSD)
	totStr := formatCostUSD(p.CostBudget)
	exceeded := p.BudgetExceeded && p.BudgetReason == "cost"
	return renderBudgetMeter(usedStr, totStr, frac, exceeded)
}

// renderProgressPane composes the spec+progress block content (without
// border). Layout per spec §6:
//
//	<goal one-liner, bold>
//
//	Progress  <bar>   <iter> / <maxIter>
//	Verify    <matrix>
//	Tokens    <human>
//	Cost      $X.YY
//	Stuck     <─ or warn>
//	Autonomy  <enum>
//
// All labels are dim; values are surface; accents only on the bar/glyphs.
// width is the content width (border + padding excluded); when very
// narrow we drop the bar entirely.
func renderProgressPane(width int, p ProgressData) string {
	var sb strings.Builder
	if p.Goal != "" {
		sb.WriteString(styleHeader(truncate(p.Goal, width)))
		sb.WriteString("\n\n")
	}
	bar := renderProgressBar(p.IterFraction(), 12)
	progressVal := fmt.Sprintf("%d / %d", p.Iter, p.MaxIter)
	if p.MaxIter <= 0 {
		bar = ""
		progressVal = fmt.Sprintf("%d", p.Iter)
	}

	rows := [][2]string{
		{"Progress", strings.TrimSpace(bar + "  " + progressVal)},
		{"Verify", renderVerifyMatrix(p.VerifyResults)},
		{"Tokens", renderTokensRow(p)},
		{"Cost", renderCostRow(p)},
		{"Stuck", renderStuckCell(p)},
		{"Autonomy", styleDim(p.Autonomy)},
	}
	for _, r := range rows {
		label := styleDim(fmt.Sprintf("%-9s", r[0]))
		sb.WriteString(label)
		sb.WriteString(" ")
		sb.WriteString(r[1])
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderStuckCell returns the right-hand value for the Stuck row.
// "─" (dim) when no stuck, "⚠ <pattern> (recovery: <strategy>)" when
// active. When recovery has been exhausted it switches to alert color.
func renderStuckCell(p ProgressData) string {
	g := Glyphs()
	if p.StuckPattern == "" {
		return styleDim(g.HSep)
	}
	body := fmt.Sprintf("%s %s", g.Warn, p.StuckPattern)
	if p.StuckRecovery != "" {
		body += fmt.Sprintf(" (recovery: %s)", p.StuckRecovery)
	}
	if p.StuckExhausted {
		return styleCritical(g.Failed + " " + body + " (exhausted)")
	}
	return styleCaution(body)
}

// ProgressData is the view-model for the progress pane. It's a flat
// struct so renderers stay pure functions; the Update layer fills it
// from session + tail events.
//
// TokensBudget / CostBudget are 0 when the spec didn't set a cap on
// that dimension; renderers must treat zero as "no budget" and fall
// back to the bare value. BudgetExceeded is a sticky bit set when the
// runner emits budget_exceeded so the row stays in the alert palette
// even after the run stops and the live counter rolls over.
type ProgressData struct {
	Goal           string
	Iter           int32
	MaxIter        int32
	VerifyResults  []string // "pass" / "fail" / "skip" / ""
	TokensIn       int64
	TokensOut      int64
	TokensBudget   int64
	CostUSD        float64
	CostBudget     float64
	BudgetExceeded bool   // sticky after a budget_exceeded event
	BudgetReason   string // "tokens" | "cost" — set with BudgetExceeded
	Autonomy       string
	StuckPattern   string // empty when not stuck
	StuckRecovery  string // strategy currently in flight
	StuckExhausted bool
}

// IterFraction returns the current iteration as a 0..1 fraction of the
// budget. Returns 0 when MaxIter is unset.
func (p ProgressData) IterFraction() float64 {
	if p.MaxIter <= 0 {
		return 0
	}
	return float64(p.Iter) / float64(p.MaxIter)
}
