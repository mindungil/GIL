package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/sdk"
)

// writeStatusVisual is the Phase-14 default rendering of `gil status`.
// Each session is a two-line "card":
//
//   ●  abc123   ▰▰▰▰▰▰▰▱▱▱▱▱   23/100   $0.61   Add dark mode to web frontend
//               iter 23  ·  ASK_DESTRUCTIVE  ·  started 18:01  ·  2h 36m
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
func writeStatusVisual(w io.Writer, list []*sdk.Session, ascii bool) error {
	g := uistyle.NewGlyphs(ascii)
	p := uistyle.NewPalette(false)

	if len(list) == 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "   %s\n", p.Dim("No sessions yet."))
		fmt.Fprintf(w, "   %s  %s   %s\n",
			p.Info(g.Arrow), p.Primary("gil interview"), p.Dim("start a new task"))
		fmt.Fprintln(w)
		return nil
	}

	total := len(list)
	const maxRows = 10
	if total > maxRows {
		list = list[:maxRows]
	}

	fmt.Fprintln(w)
	for _, s := range list {
		if s == nil {
			continue
		}
		writeStatusCard(w, g, p, s)
	}

	uistyle.OverflowHint(w, p, total, len(list))

	fmt.Fprintln(w)
	return nil
}

func writeStatusCard(w io.Writer, g uistyle.Glyphs, p uistyle.Palette, s *sdk.Session) {
	marker, role := sessionStatusGlyph(g,s.Status)
	col := colourMarker(p, marker, role)
	bar := uistyle.BarFixed(g, int(s.CurrentIteration), 100)
	row := summaryRowFromSession(s)
	iter := iterDisplay(row)
	cost := renderCostCell(g, p, row)
	goal := truncRune(s.GoalHint, 48)
	fmt.Fprintf(w, "   %s  %s   %s   %-7s  %-18s %s\n",
		col, p.Dim(shortID(s.ID)), bar, iter, cost, goal)

	// Meta band — joins non-empty fragments with " · ". This keeps the
	// row stable across sessions that don't have every datum (e.g. a
	// CREATED session has no "iter" yet, no "started" timestamp).
	meta := []string{}
	if s.CurrentIteration > 0 {
		meta = append(meta, fmt.Sprintf("iter %d", s.CurrentIteration))
	}
	// Server doesn't surface autonomy on the SDK Session today; when it
	// does we'll splice it here. Showing the stuck note is the most
	// load-bearing meta a user wants at a glance, and falls naturally
	// out of the status string.
	if strings.EqualFold(s.Status, "STUCK") {
		meta = append(meta, p.Caution("STUCK"))
	}
	if len(meta) > 0 {
		indent := strings.Repeat(" ", 14)
		fmt.Fprintf(w, "%s%s\n", indent, p.Dim(strings.Join(meta, "  ·  ")))
	}
}
