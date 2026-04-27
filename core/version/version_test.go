package version

import (
	"strings"
	"testing"
)

// withVars temporarily overrides Version/Commit/BuildDate for a single
// test. We cannot use t.Setenv-style helpers because these are package
// vars, so the helper takes a snapshot, runs the closure, and restores
// the originals via t.Cleanup.
func withVars(t *testing.T, v, c, d string) {
	t.Helper()
	pv, pc, pd := Version, Commit, BuildDate
	Version, Commit, BuildDate = v, c, d
	t.Cleanup(func() {
		Version, Commit, BuildDate = pv, pc, pd
	})
}

func TestString_AllFieldsSet(t *testing.T) {
	withVars(t, "v0.1.0-alpha", "abc1234", "2026-04-27T12:34:56Z")
	got := String()
	want := "v0.1.0-alpha (abc1234, 2026-04-27T12:34:56Z)"
	if got != want {
		t.Fatalf("String()=%q want %q", got, want)
	}
}

func TestShort_ReturnsVersionOnly(t *testing.T) {
	withVars(t, "v1.2.3", "deadbeef", "2026-04-27T00:00:00Z")
	if got := Short(); got != "v1.2.3" {
		t.Fatalf("Short()=%q want v1.2.3", got)
	}
}

// When neither commit nor date is stamped, String collapses to just the
// version — that's the shape a `go build` of a non-VCS tree produces.
func TestString_VersionOnly_WhenCommitAndDateUnknown(t *testing.T) {
	withVars(t, "v0.5.0", "unknown", "unknown")
	if got := String(); got != "v0.5.0" {
		t.Fatalf("String()=%q want v0.5.0", got)
	}
}

// When only commit is missing we still want the date in the output —
// "v0.5.0 (2026-04-27T00:00:00Z)" is more useful than dropping the date
// silently.
func TestString_VersionAndDate_WhenCommitUnknown(t *testing.T) {
	withVars(t, "v0.5.0", "unknown", "2026-04-27T00:00:00Z")
	got := String()
	want := "v0.5.0 (2026-04-27T00:00:00Z)"
	if got != want {
		t.Fatalf("String()=%q want %q", got, want)
	}
}

// And symmetrically: when only date is missing, the commit alone is
// useful enough to surface.
func TestString_VersionAndCommit_WhenDateUnknown(t *testing.T) {
	withVars(t, "v0.5.0", "abc1234", "unknown")
	got := String()
	want := "v0.5.0 (abc1234)"
	if got != want {
		t.Fatalf("String()=%q want %q", got, want)
	}
}

// Dev fallback: the bare default values must produce something
// non-empty. We don't assert the exact shape because BuildInfo from
// `go test` may stamp the test binary with VCS info, which is fine —
// we only care that String() doesn't return an empty string and
// preserves the version sentinel when no other info is available.
func TestString_DevFallback_NotEmpty(t *testing.T) {
	withVars(t, "0.0.0-dev", "unknown", "unknown")
	got := String()
	if got == "" {
		t.Fatal("String() returned empty for dev build")
	}
	// Should at least contain "dev" (either "0.0.0-dev" or "(devel)"
	// from BuildInfo).
	if !strings.Contains(got, "dev") {
		t.Fatalf("String()=%q expected to contain 'dev'", got)
	}
}

// shortCommit truncates to 12 chars; shorter inputs pass through.
func TestShortCommit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc1234", "abc1234"},
		{"abcdef0123456789abcdef0123456789abcdef01", "abcdef012345"},
		{"  abcdef012345 ", "abcdef012345"},
		{"", ""},
	}
	for _, c := range cases {
		if got := shortCommit(c.in); got != c.want {
			t.Errorf("shortCommit(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// resolved should pass through ldflags-set values unchanged.
func TestResolved_LdflagsTakePrecedence(t *testing.T) {
	withVars(t, "v9.9.9", "deadbeef1234", "2026-12-31T23:59:59Z")
	v, c, d := resolved()
	if v != "v9.9.9" || c != "deadbeef1234" || d != "2026-12-31T23:59:59Z" {
		t.Fatalf("resolved()=(%q,%q,%q) want (v9.9.9,deadbeef1234,2026-12-31T23:59:59Z)", v, c, d)
	}
}
