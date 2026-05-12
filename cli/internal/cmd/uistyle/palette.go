package uistyle

import (
	"os"
	"strings"
)

// Palette is the resolved color helper. All emit methods are pure
// functions — they prepend an ANSI SGR sequence and append the reset.
// When colors are disabled (NO_COLOR=1, or stdout not a TTY at the
// caller's discretion) every method returns the input verbatim, so
// renderers do not need a parallel "plain-text" code path.
type Palette struct {
	enabled bool
}

// NewPalette returns a palette whose color output respects NO_COLOR.
// Pass forceOff=true to suppress colors regardless of env (e.g. when
// the caller has already detected a non-TTY stdout).
//
// We keep this on the cli side intentionally — the TUI module owns its
// lipgloss styling, and the cli renderers need only a handful of
// 16-color SGR codes that lipgloss would be overkill for.
func NewPalette(forceOff bool) Palette {
	if forceOff {
		return Palette{enabled: false}
	}
	if v := os.Getenv("NO_COLOR"); v != "" && v != "0" {
		return Palette{enabled: false}
	}
	// Respect a CI override too — same convention as goose/codex, and
	// it lets test scripts assert plain text without monkeying with
	// terminal detection.
	if v := os.Getenv("GIL_NO_COLOR"); v != "" && v != "0" {
		return Palette{enabled: false}
	}
	return Palette{enabled: true}
}

// ANSI SGR codes — 16-color set per spec §1 fallback table.
const (
	sgrReset    = "\x1b[0m"
	sgrBold     = "\x1b[1m"
	sgrDim      = "\x1b[2m"
	sgrItalic   = "\x1b[3m"
	sgrFgWhite  = "\x1b[37m"
	sgrFgGray   = "\x1b[90m" // bright black = dim/meta
	sgrFgCyan   = "\x1b[36m" // info accent
	sgrFgGreen  = "\x1b[32m" // success accent
	sgrFgYellow = "\x1b[33m" // caution accent
	sgrFgRed    = "\x1b[31m" // alert accent
	sgrFgMag    = "\x1b[95m" // bright magenta = emphasis
)

// Each accent helper is a thin wrapper. Spec §1 rule: at most 2
// accent colors per visible screen — that's a discipline the renderer
// enforces, not the palette; the palette just exposes the vocabulary.

func (p Palette) Primary(s string) string  { return p.wrap(sgrBold+sgrFgWhite, s) }
func (p Palette) Surface(s string) string  { return s } // default text — no SGR needed
func (p Palette) Dim(s string) string      { return p.wrap(sgrFgGray, s) }
func (p Palette) Meta(s string) string     { return p.wrap(sgrDim+sgrItalic, s) }
func (p Palette) Info(s string) string     { return p.wrap(sgrFgCyan, s) }
func (p Palette) Success(s string) string  { return p.wrap(sgrFgGreen, s) }
func (p Palette) Caution(s string) string  { return p.wrap(sgrFgYellow, s) }
func (p Palette) Alert(s string) string    { return p.wrap(sgrFgRed, s) }
func (p Palette) Emphasis(s string) string { return p.wrap(sgrFgMag, s) }
func (p Palette) Bold(s string) string     { return p.wrap(sgrBold, s) }

// wrap is the single point that decides whether to emit ANSI. We use
// strings.Builder rather than fmt.Sprintf because most of these calls
// are on the per-line hot path of `gil watch` — fmt's reflection-based
// formatter is ~10x slower for the common "two-string" case.
func (p Palette) wrap(prefix, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(prefix) + len(s) + len(sgrReset))
	b.WriteString(prefix)
	b.WriteString(s)
	b.WriteString(sgrReset)
	return b.String()
}

// Enabled reports whether the palette is emitting ANSI. Renderers use
// this for compositional decisions (e.g. whether to reserve a column
// for a colored ✓/✗ vs falling back to a wider word like "ok"/"fail").
func (p Palette) Enabled() bool { return p.enabled }
