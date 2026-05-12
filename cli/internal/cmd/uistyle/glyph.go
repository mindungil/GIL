// Package uistyle is the shared visual vocabulary for the gil CLI's
// "mission-control" surface — glyphs, palette, sub-cell progress bars,
// relative-time formatting. It is intentionally implemented with stdlib
// + a hand-rolled ANSI helper so the cli module does not pull in
// lipgloss (which is reserved for the TUI module). The TUI uses a
// mirror copy of the glyph constants under tui/internal/app/style.go;
// see docs/design/terminal-aesthetic.md for the canonical spec the two
// surfaces share.
package uistyle

// Glyphs is the active glyph set. Constructed via NewGlyphs() with the
// caller's --ascii preference. Keeping the values on a struct (rather
// than package-level vars) makes the swap explicit at every call-site:
// renderers receive a Glyphs by value and never reach for the global,
// which keeps the ASCII fallback testable and stops a stray flag from
// silently muting the Unicode default.
type Glyphs struct {
	Running    string // session running / live indicator
	Idle       string // idle / pending
	Paused     string // paused / suspended
	Done       string // verify pass / run completed
	Failed     string // verify fail / run errored
	Warn       string // stuck / caution
	TrendUp    string // cost ↑ over time
	TrendDown  string // cost ↓ over time
	Arrow      string // next-step suggestion ("›  gil status")
	Bullet     string // list bullet (memory bank excerpts)
	QuoteBar   string // left margin on log lines
	BarFilled  string // progress bar filled cell
	BarEmpty   string // progress bar empty cell
	Eighths    [9]string // sub-cell progress fractions (0/8 .. 8/8)
	HeavyHRule string // section divider
	LightHRule string // sub-divider
	ActionMark string // → for tool_call / provider_request
	ObserveMrk string // ≪ for tool_result / provider_response
	StuckMark  string // ⚠ for stuck/warn lines (alias of Warn for log lines)
	SpinFrames []string // braille spinner — 10 frames, 80ms each
}

// unicodeGlyphs is the default set per the aesthetic spec
// (terminal-aesthetic.md §3). The eighths table is the standard
// Unicode block-element progression used to render sub-cell motion.
var unicodeGlyphs = Glyphs{
	Running:    "●",
	Idle:       "○",
	Paused:    "◐",
	Done:       "✓",
	Failed:     "✗",
	Warn:       "⚠",
	TrendUp:    "▲",
	TrendDown:  "▼",
	Arrow:      "›",
	Bullet:     "»",
	QuoteBar:   "▏",
	BarFilled:  "▰",
	BarEmpty:   "▱",
	Eighths:    [9]string{" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"},
	HeavyHRule: "━",
	LightHRule: "─",
	ActionMark: "→",
	ObserveMrk: "≪",
	StuckMark:  "⚠",
	SpinFrames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
}

// asciiGlyphs is the LANG=C / --ascii fallback. The mappings are the
// ones called out in terminal-aesthetic.md §9: ●→*, ▰→#, ▱→., ›→>,
// ▏→|. The rest follow the same plain-ASCII spirit so a render under
// --ascii is still parseable without any Unicode font.
var asciiGlyphs = Glyphs{
	Running:    "*",
	Idle:       "o",
	Paused:    "=",
	Done:       "v",
	Failed:     "x",
	Warn:       "!",
	TrendUp:    "^",
	TrendDown:  "v",
	Arrow:      ">",
	Bullet:     "*",
	QuoteBar:   "|",
	BarFilled:  "#",
	BarEmpty:   ".",
	Eighths:    [9]string{" ", " ", " ", " ", "#", "#", "#", "#", "#"},
	HeavyHRule: "=",
	LightHRule: "-",
	ActionMark: ">",
	ObserveMrk: "<",
	StuckMark:  "!",
	SpinFrames: []string{"|", "/", "-", "\\", "|", "/", "-", "\\", "|", "/"},
}

// NewGlyphs returns the active glyph set. Pass true for ASCII mode
// (the global --ascii flag wires this); false uses the Unicode
// default the spec calls for.
func NewGlyphs(ascii bool) Glyphs {
	if ascii {
		return asciiGlyphs
	}
	return unicodeGlyphs
}
