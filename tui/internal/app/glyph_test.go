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

// Regression: `LANG=C.UTF-8` is the modern POSIX UTF-8 codeset
// (Ubuntu/Debian default in cloud images). The previous detection
// dot-split first and matched language="C" → falsely degraded to
// ASCII. The codeset suffix must be checked first.
func TestGlyphs_DetectAscii_CDotUTF8IsUnicode(t *testing.T) {
	t.Setenv("GIL_ASCII", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	require.False(t, detectAscii(), "C.UTF-8 must be treated as Unicode-capable")
}

func TestGlyphs_DetectAscii_CDotUTF8LowerCase(t *testing.T) {
	t.Setenv("GIL_ASCII", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.utf8")
	require.False(t, detectAscii(), "lowercase utf8 codeset must also be recognised")
}

func TestBoxGlyphs_UnicodeAndAscii(t *testing.T) {
	prev := IsAsciiMode()
	defer SetAsciiMode(prev)

	SetAsciiMode(false)
	g := Glyphs()
	if g.BoxHeavyTL != "╔" || g.BoxHeavyTR != "╗" || g.BoxHeavyHRule != "═" || g.BoxHeavyVRule != "║" {
		t.Fatalf("unicode heavy box glyphs missing: %+v", g)
	}
	if g.BoxLightTL != "╭" || g.BoxLightHRule != "─" {
		t.Fatalf("unicode light box glyphs missing: %+v", g)
	}

	SetAsciiMode(true)
	a := Glyphs()
	if a.BoxHeavyTL != "+" || a.BoxHeavyHRule != "=" || a.BoxHeavyVRule != "|" {
		t.Fatalf("ascii heavy box fallback wrong: %+v", a)
	}
	if a.BoxLightTL != "+" || a.BoxLightHRule != "-" {
		t.Fatalf("ascii light box fallback wrong: %+v", a)
	}
}
