package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
)

// writeStatusVisual is the Phase-14 default rendering of `gil status`.
// Each session is a two-line "card":
//
//	●  abc123   ▰▰▰▰▰▰▰▱▱▱▱▱   23/100   $0.61   Add dark mode to web frontend
//	            iter 23  ·  ASK_DESTRUCTIVE  ·  started 18:01  ·  2h 36m
//
// The first line is the same shape as the no-arg summary so a user
// can move between the two surfaces without retraining the eye. The
// second line is the meta band — autonomy, started/finished, stuck
// note when applicable.
//
// We use uistyle so colors honour NO_COLOR and the glyph swap honours
// --ascii. When the session list is empty we print a single dim hint
// rather than a raw table header (the empty-table case under the old
// renderer was visually noisy).
func writeStatusVisual(w io.Writer, rows []summaryRow, ascii bool) error {
	g := uistyle.NewGlyphs(ascii)
	p := uistyle.NewPalette(false)

	if len(rows) == 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "   %s\n", p.Dim("No sessions yet."))
		fmt.Fprintf(w, "   %s  %s   %s\n",
			p.Info(g.Arrow), p.Primary("gil interview"), p.Dim("start a new task"))
		fmt.Fprintln(w)
		return nil
	}

	total := len(rows)
	const maxRows = 10
	if total > maxRows {
		rows = rows[:maxRows]
	}

	fmt.Fprintln(w)
	for _, row := range rows {
		writeStatusCard(w, g, p, row)
	}

	uistyle.OverflowHint(w, p, total, len(rows))

	fmt.Fprintln(w)
	return nil
}

func writeStatusCard(w io.Writer, g uistyle.Glyphs, p uistyle.Palette, row summaryRow) {
	marker, role := sessionStatusGlyph(g, row.Status)
	col := colourMarker(p, marker, role)
	bar := uistyle.BarFixed(g, int(row.Iter), 100)
	iter := iterDisplay(row)
	cost := renderCostCell(g, p, row)
	goal := truncRune(displayGoal(row), 48)
	fmt.Fprintf(w, "   %s  %-22s   %s   %-7s  %-18s %s\n",
		col, p.Dim(row.Name), bar, iter, cost, goal)

	// Meta band — joins non-empty fragments with " · ". This keeps the
	// row stable across sessions that don't have every datum (e.g. a
	// CREATED session has no "iter" yet, no "started" timestamp).
	meta := []string{}
	if row.Iter > 0 {
		meta = append(meta, fmt.Sprintf("iter %d", row.Iter))
	}
	// Phase 25 A4 — relative time for "when did this start", much more
	// readable than the raw RFC3339-ish absolute we used to bury in the
	// JSON output. Falls through silently when CreatedAt is zero
	// (older daemons).
	if rel := relTime(row.CreatedAt); rel != "" {
		meta = append(meta, "started "+rel)
	}
	// Server doesn't surface autonomy on the SDK Session today; when it
	// does we'll splice it here. Showing the stuck note is the most
	// load-bearing meta a user wants at a glance, and falls naturally
	// out of the status string.
	if strings.EqualFold(row.Status, "STUCK") {
		meta = append(meta, p.Caution("STUCK"))
	}
	if len(meta) > 0 {
		indent := strings.Repeat(" ", 14)
		fmt.Fprintf(w, "%s%s\n", indent, p.Dim(strings.Join(meta, "  ·  ")))
	}
	if gitSummary := row.GitSummary; gitSummary != "" {
		indent := strings.Repeat(" ", 14)
		fmt.Fprintf(w, "%s%s\n", indent, p.Dim("git "+gitSummary))
	}
	if row.LatestType != "" {
		indent := strings.Repeat(" ", 14)
		fmt.Fprintf(w, "%s%s\n", indent, p.Dim(fmt.Sprintf("latest %s · %s", row.LatestType, relTime(row.LatestAt))))
	}
}
