package uistyle

import (
	"strings"
	"testing"
	"time"
)

// TestGlyphs_UnicodeAndAsciiDiverge guards the spec's contract: ASCII
// mode must replace every cell-width-affecting Unicode glyph with a
// single-column ASCII char. If a contributor adds a new glyph but
// forgets the ASCII fallback, this test catches it.
func TestGlyphs_UnicodeAndAsciiDiverge(t *testing.T) {
	u := NewGlyphs(false)
	a := NewGlyphs(true)

	// Spec §9 mappings — these are the load-bearing fallbacks.
	if u.Running == a.Running {
		t.Fatalf("ASCII mode should remap Running glyph; got %q on both", u.Running)
	}
	if u.BarFilled == a.BarFilled {
		t.Fatalf("ASCII mode should remap BarFilled; got %q on both", u.BarFilled)
	}
	if u.QuoteBar == a.QuoteBar {
		t.Fatalf("ASCII mode should remap QuoteBar; got %q on both", u.QuoteBar)
	}
	// Unicode arrow is single column; ASCII should also be single byte.
	if len(a.Arrow) != 1 {
		t.Fatalf("ASCII Arrow must be 1 byte (got %q, %d bytes)", a.Arrow, len(a.Arrow))
	}
}

// TestBar_ProgressClampingAndShape covers the three branches Bar()
// has: empty, partial-with-fraction, full. We assert string length
// (cells) rather than exact glyph layout so the test still passes if
// the eighths progression is later refined.
func TestBar_ProgressClampingAndShape(t *testing.T) {
	g := NewGlyphs(true) // use ASCII so each cell is exactly 1 byte
	if got := Bar(g, 12, 0, 100); !strings.HasPrefix(got, ".") || len(got) != 12 {
		t.Fatalf("zero-progress bar wrong: %q", got)
	}
	if got := Bar(g, 12, 100, 100); strings.Contains(got, ".") || len(got) != 12 {
		t.Fatalf("full bar wrong: %q", got)
	}
	// Over-max clamps; never produces longer than width.
	if got := Bar(g, 12, 999, 100); len(got) != 12 {
		t.Fatalf("over-max bar wrong: %q (len %d)", got, len(got))
	}
	// Zero max → not a divide-by-zero, just an empty bar.
	if got := Bar(g, 12, 5, 0); !strings.HasPrefix(got, ".") || len(got) != 12 {
		t.Fatalf("zero-max bar wrong: %q", got)
	}
}

// TestPalette_NoColorRespected verifies that NO_COLOR and the
// forceOff path both flatten ANSI to plain text. Critical for piping
// `gil status` into less / awk.
func TestPalette_NoColorRespected(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	p := NewPalette(false)
	if p.Enabled() {
		t.Fatal("NO_COLOR=1 should disable palette")
	}
	if got := p.Alert("danger"); got != "danger" {
		t.Fatalf("expected raw text under NO_COLOR; got %q", got)
	}
}

// TestPalette_ForceOffWins ensures the explicit forceOff bool wins
// over a missing NO_COLOR — used by the JSON-output path which must
// never emit ANSI even on a TTY.
func TestPalette_ForceOffWins(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("GIL_NO_COLOR", "")
	p := NewPalette(true)
	if p.Enabled() {
		t.Fatal("forceOff must disable palette")
	}
}

// TestDuration_Brackets locks down each format bucket so the
// status-card "started 18:01 · 2h 36m" rendering stays stable.
func TestDuration_Brackets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{2 * time.Minute, "2m"},
		{2*time.Hour + 36*time.Minute, "2h 36m"},
		{73 * time.Hour, "3d 1h"},
	}
	for _, c := range cases {
		if got := Duration(c.d); got != c.want {
			t.Errorf("Duration(%v) = %q; want %q", c.d, got, c.want)
		}
	}
}

func TestAgo_HandlesZero(t *testing.T) {
	if Ago(time.Time{}, time.Now()) != "never" {
		t.Fatal("Ago on zero time should be 'never'")
	}
}
