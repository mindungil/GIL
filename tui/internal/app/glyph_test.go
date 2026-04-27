package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGlyphs_UnicodeDefaults(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(false)
	defer SetAsciiMode(prev)

	g := Glyphs()
	require.Equal(t, "●", g.Running)
	require.Equal(t, "✓", g.Done)
	require.Equal(t, "✗", g.Failed)
	require.Equal(t, "⚠", g.Warn)
	require.Equal(t, "▰", g.BarFill)
	require.Equal(t, "▱", g.BarEmpty)
	require.Equal(t, "›", g.Arrow)
	require.Equal(t, "▏", g.QuoteBar)
	require.Equal(t, "…", g.Ellipsis)
	require.Equal(t, 10, len(g.Spinner))
}

func TestGlyphs_AsciiFallback(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(true)
	defer SetAsciiMode(prev)

	g := Glyphs()
	require.Equal(t, "*", g.Running)
	require.Equal(t, "+", g.Done)
	require.Equal(t, "x", g.Failed)
	require.Equal(t, "!", g.Warn)
	require.Equal(t, "#", g.BarFill)
	require.Equal(t, ".", g.BarEmpty)
	require.Equal(t, ">", g.Arrow)
	require.Equal(t, "|", g.QuoteBar)
	require.Equal(t, "...", g.Ellipsis)
}

func TestGlyphs_AsciiTruncate_UsesAsciiEllipsis(t *testing.T) {
	prev := IsAsciiMode()
	SetAsciiMode(true)
	defer SetAsciiMode(prev)

	out := truncate("abcdefghijk", 10)
	require.Equal(t, "abcdefg...", out)
}

func TestGlyphs_DetectAscii_ExplicitOverride(t *testing.T) {
	t.Setenv("GIL_ASCII", "1")
	t.Setenv("LANG", "en_US.UTF-8")
	require.True(t, detectAscii())
}

func TestGlyphs_DetectAscii_LocaleC(t *testing.T) {
	t.Setenv("GIL_ASCII", "")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "")
	t.Setenv("LC_CTYPE", "")
	require.True(t, detectAscii())
}

func TestGlyphs_DetectAscii_UTF8Locale(t *testing.T) {
	t.Setenv("GIL_ASCII", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.UTF-8")
	require.False(t, detectAscii())
}
