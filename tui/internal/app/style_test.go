package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// hasANSI returns true when the input contains an ANSI CSI escape.
func hasANSI(s string) bool {
	return strings.Contains(s, "\x1b[")
}

func TestStyle_NoColorStripsANSI(t *testing.T) {
	prev := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prev)

	for name, fn := range map[string]func(string) string{
		"surface":  styleSurface,
		"meta":     styleMeta,
		"dim":      styleDim,
		"info":     styleInfo,
		"header":   styleHeader,
		"success":  styleSuccess,
		"caution":  styleCaution,
		"alert":    styleAlert,
		"emphasis": styleEmphasis,
		"critical": styleCritical,
		"selBg":    styleSelectedBg,
	} {
		out := fn("hello " + name)
		require.False(t, hasANSI(out), "expected no ANSI in NO_COLOR mode for %s, got %q", name, out)
		require.Contains(t, out, "hello")
	}
}

func TestStyle_ColorEnabledStillRendersText(t *testing.T) {
	// We can't reliably assert ANSI presence in CI — lipgloss/termenv
	// detects terminal capability and may emit plain text when stdout
	// isn't a TTY (which is the test environment). The contract we
	// CARE about is that the call doesn't crash and the text passes
	// through; ANSI presence is exercised in a real terminal session
	// during manual smoke testing.
	prev := IsNoColor()
	SetNoColor(false)
	defer SetNoColor(prev)

	require.Equal(t, "x", stripANSI(styleInfo("x")))
}

// stripANSI removes CSI sequences for substring assertions.
func stripANSI(s string) string {
	var b []byte
	in := []byte(s)
	for i := 0; i < len(in); i++ {
		if in[i] == 0x1b && i+1 < len(in) && in[i+1] == '[' {
			j := i + 2
			for j < len(in) {
				c := in[j]
				j++
				if c >= '@' && c <= '~' {
					break
				}
			}
			i = j - 1
			continue
		}
		b = append(b, in[i])
	}
	return string(b)
}

func TestStatusGlyph_PerStatus(t *testing.T) {
	prev := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prev)
	prevA := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prevA)

	g := Glyphs()
	require.Equal(t, g.Running, statusGlyph(g, "RUNNING"))
	require.Equal(t, g.Done, statusGlyph(g, "DONE"))
	require.Equal(t, g.Failed, statusGlyph(g, "FAILED"))
	require.Equal(t, g.Warn, statusGlyph(g, "STUCK"))
	require.Equal(t, g.Paused, statusGlyph(g, "FROZEN"))
	require.Equal(t, g.Idle, statusGlyph(g, "WHATEVER"))
}
