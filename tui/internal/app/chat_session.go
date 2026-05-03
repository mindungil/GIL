package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/mindungil/gil/sdk"
)

// renderPreFirstTurn returns the session-list-and-invite block shown
// in the conversation region until the user submits their first prompt.
// Total height is pinned to convH rows by trailing-newline padding so
// the chrome below stays in its row.
func (m *chatModel) renderPreFirstTurn(convH int) string {
	const topN = 5

	var b strings.Builder
	b.WriteString("\n") // 1 row of breathing space

	if len(m.sessions) == 0 {
		empty := truncRunes("no past sessions  — describe what you want to build below", max(0, m.width-6), true)
		b.WriteString("      ")
		b.WriteString(styleSurface(empty))
		b.WriteString("\n")
		return padToHeight(b.String(), convH)
	}

	lead := truncRunes(chatLeadIn(len(m.sessions), topN), max(0, m.width-6), true)
	b.WriteString("      ")
	b.WriteString(styleSurface(lead))
	b.WriteString("\n\n")

	shown := m.sessions
	if len(shown) > topN {
		shown = shown[:topN]
	}
	for _, s := range shown {
		b.WriteString("         ")
		b.WriteString(formatChatSessionRow(s, m.width))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("         ")
	b.WriteString(styleMeta(truncRunes("›  describe a new task, or resume one above by name", max(0, m.width-9), true)))
	b.WriteString("\n")
	return padToHeight(b.String(), convH)
}

// chatLeadIn produces the prose lead-in line above the session rows.
func chatLeadIn(total, topN int) string {
	if total == 1 {
		return "1 past session — pick it below or describe a new task"
	}
	if total <= topN {
		return fmt.Sprintf("%d past sessions — pick one below or describe a new task", total)
	}
	return fmt.Sprintf("%d past sessions — most recent %d below, describe a new task or resume one",
		total, topN)
}

// formatChatSessionRow renders one row: glyph, slug (truncated), age,
// phase. Width-aware so narrow terminals don't tear.
func formatChatSessionRow(s *sdk.Session, termWidth int) string {
	glyph := statusGlyph(Glyphs(), strings.ToUpper(s.Status))
	slug := s.GoalHint
	if slug == "" {
		// fall back to ID prefix (rune-aware so non-ASCII IDs survive)
		slug = truncRunes(strings.ToLower(s.ID), 10, false)
	}
	slug = truncRunes(slug, 32, true)
	age := relAgeShort(s.CreatedAt)
	phase := strings.ToLower(s.Status)

	// Fixed columns: glyph(1) + 2sp + slug(32) + 4sp + age(5) + 4sp + phase
	row := fmt.Sprintf("%s  %-32s    %-5s    %s",
		glyph, styleSurface(slug), styleMeta(age), styleMeta(phase))
	if termWidth < 80 {
		// Narrow: drop phase, shorten slug. Match package-wide narrow
		// threshold (see view.go: narrow := m.width < 80).
		shortSlug := truncRunes(slug, 18, true)
		row = fmt.Sprintf("%s  %-18s  %s",
			glyph, styleSurface(shortSlug), styleMeta(age))
	}
	return row
}

// truncRunes shortens s to at most max runes, optionally appending the
// active ellipsis glyph (taking 1 rune of the budget) when truncation
// occurs. Operates on runes, not bytes, so multi-byte UTF-8 sequences
// (Korean, CJK, emoji) are never split mid-rune.
func truncRunes(s string, max int, withEllipsis bool) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if !withEllipsis || max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + Glyphs().Ellipsis
}

func relAgeShort(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return "—"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// padToHeight ensures s ends with enough newlines to span exactly h
// rows. If s is already taller than h, returns s unchanged.
func padToHeight(s string, h int) string {
	have := strings.Count(s, "\n")
	if have >= h-1 {
		return s
	}
	return s + strings.Repeat("\n", h-1-have)
}
