package uistyle

import "strings"

// Bar renders a horizontal progress bar with sub-cell precision per
// the aesthetic spec §3 ("Progress fill: ▰ + ▱; sub-cell ▏▎▍▌▋▊▉█").
//
// width is the number of cells reserved for the bar.
// value is current progress; max is the maximum (max==0 → empty bar,
// not divide-by-zero). Values clamped to [0, max].
//
// The bar uses BarFilled for whole filled cells, then a single eighths
// glyph for the fractional remainder, then BarEmpty for the rest. In
// ASCII mode the eighths table degrades cleanly to either '#' or
// nothing, so the bar still reads even without Unicode.
func Bar(g Glyphs, width, value, max int) string {
	if width <= 0 {
		return ""
	}
	if max <= 0 || value <= 0 {
		return strings.Repeat(g.BarEmpty, width)
	}
	if value > max {
		value = max
	}
	// Total sub-cell resolution = width * 8. We compute the integer
	// number of eighths, then split into whole cells + remainder. We
	// avoid floats so the rendering is byte-stable for snapshot tests.
	total := width * 8
	fill := value * total / max
	whole := fill / 8
	rem := fill % 8
	if whole > width {
		whole = width
		rem = 0
	}
	var b strings.Builder
	b.Grow(width * 3) // generous; UTF-8 ≤ 3 bytes for our glyphs
	for i := 0; i < whole; i++ {
		b.WriteString(g.BarFilled)
	}
	if whole < width {
		// rem==0 → an empty cell rather than a stray "0/8" glyph.
		// rem==8 cannot occur (it rolls into whole), so the table
		// indexing is always in-range.
		if rem > 0 {
			b.WriteString(g.Eighths[rem])
		} else {
			b.WriteString(g.BarEmpty)
		}
		for i := whole + 1; i < width; i++ {
			b.WriteString(g.BarEmpty)
		}
	}
	return b.String()
}

// BarFixed is a convenience for the no-arg / status / watch screens
// where width is always 12 cells (per the spec mockups). Centralising
// the constant keeps every surface visually consistent — if we change
// the canonical width later, every renderer follows automatically.
func BarFixed(g Glyphs, value, max int) string {
	return Bar(g, 12, value, max)
}
