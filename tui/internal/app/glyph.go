package app

import (
	"os"
	"strings"
)

// Glyph is a Unicode-only icon set used by the TUI per the terminal
// aesthetic spec §3 (Iconography). All glyphs have an ASCII fallback
// for terminals that lack Unicode (LANG=C) or when --ascii is requested.
//
// Selection happens once at process start through SelectGlyphs. Tests
// can override by calling NewGlyphSet directly.
type Glyph struct {
	Running    string   // ●
	Idle       string   // ○
	Paused     string   // ◐
	Done       string   // ✓
	Failed     string   // ✗
	Warn       string   // ⚠
	TrendUp    string   // ▲
	TrendDown  string   // ▼
	Arrow      string   // › (next-step / selected-row marker in modals)
	Bullet     string   // » (list bullet)
	QuoteBar   string   // ▏ (left margin on quote/log lines)
	HSep       string   // ─ (light horizontal divider)
	HSepHeavy  string   // ━ (heavy horizontal — section divider)
	BarFill    string   // ▰ (progress filled cell)
	BarEmpty   string   // ▱ (progress empty cell)
	BarPartial []string // sub-cell smoothing: 8 widths from ▏ to █
	Spinner    []string // 10-frame Braille spinner
	Ellipsis   string   // …
	Dot        string   // · (footer separator)
	Section    string   // ╴╴ (sub-section glyph — pre-first-turn headers)

	BoxHeavyTL    string // ╔ — top-left corner of prompt panel
	BoxHeavyTR    string // ╗
	BoxHeavyBL    string // ╚
	BoxHeavyBR    string // ╝
	BoxHeavyHRule string // ═
	BoxHeavyVRule string // ║
	BoxLightTL    string // ╭ — header rounded corner (top-left)
	BoxLightTR    string // ╮
	BoxLightBL    string // ╰
	BoxLightBR    string // ╯
	BoxLightHRule string // ─
	BoxLightVRule string // │
}

// unicodeGlyphs returns the canonical glyph set per spec §3.
func unicodeGlyphs() Glyph {
	return Glyph{
		Running:   "●",
		Idle:      "○",
		Paused:    "◐",
		Done:      "✓",
		Failed:    "✗",
		Warn:      "⚠",
		TrendUp:   "▲",
		TrendDown: "▼",
		Arrow:     "›",
		Bullet:    "»",
		QuoteBar:  "▏",
		HSep:      "─",
		HSepHeavy: "━",
		BarFill:   "▰",
		BarEmpty:  "▱",
		BarPartial: []string{
			" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉",
		},
		Spinner: []string{
			"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
		},
		Ellipsis: "…",
		Dot:      "·",
		Section:  "╴╴",

		BoxHeavyTL: "╔", BoxHeavyTR: "╗", BoxHeavyBL: "╚", BoxHeavyBR: "╝",
		BoxHeavyHRule: "═", BoxHeavyVRule: "║",
		BoxLightTL: "╭", BoxLightTR: "╮", BoxLightBL: "╰", BoxLightBR: "╯",
		BoxLightHRule: "─", BoxLightVRule: "│",
	}
}

// asciiGlyphs returns the ASCII fallback per spec §9 (Degraded
// gracefully). Mappings: ●→*, ○→o, ◐→@, ✓→+, ✗→x, ⚠→!, ▰→#, ▱→.,
// ›→>, »→-, ▏→|, ▲→^, ▼→v, ─→-, ━→=, …→..., ·→·-as-`.`, spinner→|/-\.
func asciiGlyphs() Glyph {
	return Glyph{
		Running:   "*",
		Idle:      "o",
		Paused:    "@",
		Done:      "+",
		Failed:    "x",
		Warn:      "!",
		TrendUp:   "^",
		TrendDown: "v",
		Arrow:     ">",
		Bullet:    "-",
		QuoteBar:  "|",
		HSep:      "-",
		HSepHeavy: "=",
		BarFill:   "#",
		BarEmpty:  ".",
		BarPartial: []string{
			" ", " ", " ", " ", "#", "#", "#", "#",
		},
		Spinner: []string{
			"|", "/", "-", "\\", "|", "/", "-", "\\", "|", "/",
		},
		Ellipsis: "...",
		Dot:      ".",
		Section:  "--",

		BoxHeavyTL: "+", BoxHeavyTR: "+", BoxHeavyBL: "+", BoxHeavyBR: "+",
		BoxHeavyHRule: "=", BoxHeavyVRule: "|",
		BoxLightTL: "+", BoxLightTR: "+", BoxLightBL: "+", BoxLightBR: "+",
		BoxLightHRule: "-", BoxLightVRule: "|",
	}
}

// asciiMode is the process-wide flag set once at startup, controlled by
// the --ascii flag, GIL_ASCII=1, or LANG=C heuristics.
var asciiMode = detectAscii()

// detectAscii returns true when the environment doesn't safely support
// Unicode glyphs. Heuristics, in order:
//
//  1. GIL_ASCII=1 (explicit override)
//  2. LANG/LC_ALL/LC_CTYPE has a UTF-8 codeset (`.UTF-8` / `.utf8`) → Unicode
//  3. Bare "C" or "POSIX" (no codeset) → ASCII
//  4. Anything else (a real locale name like "en_US.UTF-8") → Unicode
//
// The codeset check is what distinguishes `C` (legacy 7-bit ASCII C
// locale) from `C.UTF-8` (POSIX with UTF-8 codeset, the modern Ubuntu/
// Debian default). A previous version of this function dot-split first
// and matched the language tag as `C`, which falsely degraded
// `C.UTF-8` users to ASCII glyphs even though their terminal handled
// Unicode fine.
func detectAscii() bool {
	if os.Getenv("GIL_ASCII") == "1" {
		return true
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		// First-occurrence wins (POSIX). Check codeset suffix first
		// — `C.UTF-8` and `en_US.utf8` are both Unicode-capable
		// regardless of the language tag.
		upper := strings.ToUpper(v)
		if strings.Contains(upper, ".UTF-8") || strings.Contains(upper, ".UTF8") {
			return false
		}
		// No codeset (or non-UTF-8 codeset). Bare "C"/"POSIX" means
		// 7-bit ASCII; anything else (e.g. "en_US") falls back to
		// Unicode-on by default on the assumption that modern
		// terminals handle UTF-8 even without a codeset declaration.
		head := strings.SplitN(upper, ".", 2)[0]
		switch head {
		case "C", "POSIX":
			return true
		}
		return false
	}
	return false
}

// SetAsciiMode lets startup code (--ascii flag) force ASCII glyphs.
// Tests use this too — call SetAsciiMode(true) in a defer-restore
// pattern to verify ASCII renders.
func SetAsciiMode(on bool) { asciiMode = on }

// IsAsciiMode reports whether ASCII glyph fallback is currently active.
func IsAsciiMode() bool { return asciiMode }

// Glyphs returns the active glyph set — Unicode by default, ASCII when
// SetAsciiMode(true) was called or the locale heuristic detected a
// non-Unicode environment.
func Glyphs() Glyph {
	if asciiMode {
		return asciiGlyphs()
	}
	return unicodeGlyphs()
}
