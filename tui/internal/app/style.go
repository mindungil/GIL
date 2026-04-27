package app

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Palette holds every accent + role color the TUI is allowed to use.
// The values come straight from terminal-aesthetic.md §1.
//
// The "max 2 accent colors visible at once" rule (spec §1) is enforced
// by convention in the view code, NOT by this palette — it's a budget,
// not a list. The selected-row lavender + state cyan/coral count as one
// rule (state-specific), so most screens use 1-2 of these accents at a
// time.
type Palette struct {
	Primary  lipgloss.Color // brightest text — header, etc.
	Surface  lipgloss.Color // default body text
	Meta     lipgloss.Color // dim text — timestamps, labels
	Frame    lipgloss.Color // border / divider
	Info     lipgloss.Color // mint-cyan — running, section headers
	Success  lipgloss.Color // sage — verify ok, done
	Caution  lipgloss.Color // amber — paused, stuck warn
	Alert    lipgloss.Color // coral — verify fail, stuck unrecovered
	Emphasis lipgloss.Color // lavender — "now active" selection
	BgFill   lipgloss.Color // subtle dark fill for selected row
}

// truecolorPalette is the canonical palette per spec §1. Used whenever
// the terminal supports >256 colors (the lipgloss/termenv default
// detection handles fallbacks; we don't gate on it ourselves).
func truecolorPalette() Palette {
	return Palette{
		Primary:  lipgloss.Color("#fafafa"),
		Surface:  lipgloss.Color("#cdcdcd"),
		Meta:     lipgloss.Color("#7a7a7a"),
		Frame:    lipgloss.Color("#3a3a3a"),
		Info:     lipgloss.Color("#5eead4"),
		Success:  lipgloss.Color("#86efac"),
		Caution:  lipgloss.Color("#fbbf24"),
		Alert:    lipgloss.Color("#fb7185"),
		Emphasis: lipgloss.Color("#a5b4fc"),
		BgFill:   lipgloss.Color("#1a1a1a"),
	}
}

// pal is the process-wide palette. Tests can swap it.
var pal = truecolorPalette()

// noColor is set at init when NO_COLOR is in the environment per the
// informal spec at https://no-color.org/. Style helpers below short-
// circuit to plain text when set.
var noColor = os.Getenv("NO_COLOR") != ""

// SetNoColor lets startup code or tests force NO_COLOR semantics.
func SetNoColor(on bool) { noColor = on }

// IsNoColor reports whether NO_COLOR mode is active.
func IsNoColor() bool { return noColor }

// styled wraps a lipgloss.NewStyle() builder with the NO_COLOR escape
// hatch — if the user opted out of color, every Foreground/Background/
// Bold/Italic call no-ops. Returns a fresh style that callers can chain
// further methods on.
func styled() lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle().UnsetForeground().UnsetBackground().UnsetBold().UnsetItalic().UnsetFaint()
	}
	return lipgloss.NewStyle()
}

// Style helpers — small builders so view code stays declarative.
//
// Convention: every helper takes the text it should render and returns
// the styled string. View code never calls lipgloss directly except
// through these helpers (or paneFrame for borders).

// styleSurface returns plain body text in the surface color.
func styleSurface(s string) string { return styled().Foreground(pal.Surface).Render(s) }

// styleMeta returns dim italic meta text — timestamps, labels.
func styleMeta(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Meta).Italic(true).Render(s)
}

// styleDim returns dim non-italic text — keymap footers, separators.
func styleDim(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Meta).Render(s)
}

// styleInfo returns text in the info accent (mint-cyan).
func styleInfo(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Info).Render(s)
}

// styleHeader returns a bold info-accent string for section headers.
func styleHeader(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Info).Bold(true).Render(s)
}

// styleSuccess returns text in the success accent (sage).
func styleSuccess(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Success).Render(s)
}

// styleCaution returns text in the caution accent (amber).
func styleCaution(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Caution).Render(s)
}

// styleAlert returns text in the alert accent (coral).
func styleAlert(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Alert).Render(s)
}

// styleEmphasis returns text in the emphasis accent (lavender) — used
// sparingly for "now active" markers (selected row glyph, modal arrow).
func styleEmphasis(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Emphasis).Render(s)
}

// styleCritical is bold + alert color for things like STUCK headers.
func styleCritical(s string) string {
	if noColor {
		return lipgloss.NewStyle().Bold(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(pal.Alert).Bold(true).Render(s)
}

// styleSelectedBg returns text rendered with the subtle dark highlight
// background. Used for the selected row in the sessions pane.
func styleSelectedBg(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Background(pal.BgFill).Foreground(pal.Surface).Render(s)
}

// paneFrame returns a lipgloss.Style configured as a rounded light
// frame per spec §4. width and height are content area sizes; padding
// is applied internally per spec §5 (1 row top/bottom, 2 cols sides).
func paneFrame(title string) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	st := lipgloss.NewStyle().
		Border(border).
		Padding(0, 1)
	if !noColor {
		st = st.BorderForeground(pal.Frame)
	}
	if title != "" {
		// Title is composed externally because lipgloss has no
		// "title-in-border" primitive; see view.go renderPane().
		_ = title
	}
	return st
}

// statusGlyph returns the glyph + appropriate accent color for a
// session status string. Statuses are the strings emitted by
// gilv1.SessionStatus.String() with the SESSION_STATUS_ prefix
// stripped (FROZEN, RUNNING, DONE, FAILED, STOPPED, STUCK, ...).
func statusGlyph(g Glyph, status string) string {
	switch status {
	case "RUNNING":
		return styleInfo(g.Running)
	case "DONE":
		return styleSuccess(g.Done)
	case "FAILED", "ERROR":
		return styleAlert(g.Failed)
	case "STUCK":
		return styleCaution(g.Warn)
	case "FROZEN", "PAUSED":
		return styleCaution(g.Paused)
	default:
		return styleDim(g.Idle)
	}
}
