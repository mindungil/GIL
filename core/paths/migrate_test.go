package paths

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// migrateTestSetup points HOME at a fresh tmpdir and routes all four XDG
// roots into a sibling tmpdir, returning both. This lets tests pretend
// they own the user's home, drop a fake legacy ~/.gil tree, and verify
// the XDG side of the migration without touching the real disk.
func migrateTestSetup(t *testing.T) (home string, layout Layout) {
	t.Helper()
	home = t.TempDir()
	xdg := t.TempDir()
	withCleanEnv(t, home)
	t.Setenv("GIL_HOME", xdg)
	l, err := FromEnv()
	require.NoError(t, err)
	return home, l
}

func TestMigrate_NoOpWhenLegacyMissing(t *testing.T) {
	_, l := migrateTestSetup(t)
	moved, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.False(t, moved)
}

func TestMigrate_MovesSessionsAndSocket(t *testing.T) {
	home, l := migrateTestSetup(t)
	legacy := filepath.Join(home, ".gil")
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "sessions", "01abc"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sessions", "01abc", "spec.json"), []byte(`{"id":"01abc"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sessions.db"), []byte("sqlite_marker"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "gild.pid"), []byte("12345"), 0o600))

	moved, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.True(t, moved, "expected something to move")

	// Verify destinations exist.
	body, err := os.ReadFile(filepath.Join(l.SessionsDir(), "01abc", "spec.json"))
	require.NoError(t, err)
	require.Equal(t, `{"id":"01abc"}`, string(body))

	dbBody, err := os.ReadFile(l.SessionsDB())
	require.NoError(t, err)
	require.Equal(t, "sqlite_marker", string(dbBody))

	pidBody, err := os.ReadFile(l.Pid())
	require.NoError(t, err)
	require.Equal(t, "12345", string(pidBody))

	// Sources should be gone.
	_, err = os.Stat(filepath.Join(legacy, "sessions"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(legacy, "sessions.db"))
	require.ErrorIs(t, err, os.ErrNotExist)

	// Stamp present.
	_, err = os.Stat(filepath.Join(legacy, "MIGRATED"))
	require.NoError(t, err)
}

func TestMigrate_SkipsWhenDestinationHasFiles(t *testing.T) {
	home, l := migrateTestSetup(t)
	legacy := filepath.Join(home, ".gil")
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "sessions", "old"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sessions", "old", "spec.json"), []byte("LEGACY"), 0o600))

	// Pre-populate destination so we can assert it was preserved.
	require.NoError(t, os.MkdirAll(l.SessionsDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(l.SessionsDir(), "guard"), []byte("DO NOT TOUCH"), 0o600))

	_, err := MigrateLegacyTilde(l)
	require.NoError(t, err)

	guard, err := os.ReadFile(filepath.Join(l.SessionsDir(), "guard"))
	require.NoError(t, err)
	require.Equal(t, "DO NOT TOUCH", string(guard))

	// Legacy sessions should still be there because we skipped.
	legacyBody, err := os.ReadFile(filepath.Join(legacy, "sessions", "old", "spec.json"))
	require.NoError(t, err)
	require.Equal(t, "LEGACY", string(legacyBody))
}

func TestMigrate_StampShortCircuitsSubsequentCalls(t *testing.T) {
	home, l := migrateTestSetup(t)
	legacy := filepath.Join(home, ".gil")
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "sessions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sessions", "x.json"), []byte("x"), 0o600))

	moved1, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.True(t, moved1)

	// Re-create something under legacy to prove the second call ignores it.
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "sessions", "should-be-ignored"), 0o700))
	moved2, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.False(t, moved2, "stamp should short-circuit")
	_, err = os.Stat(filepath.Join(legacy, "sessions", "should-be-ignored"))
	require.NoError(t, err, "stamp short-circuit must not delete legacy contents")
}

func TestMigrate_PerUserSubtree(t *testing.T) {
	home, l := migrateTestSetup(t)
	legacy := filepath.Join(home, ".gil")
	// Build legacy/users/alice/sessions/...
	aliceSrc := filepath.Join(legacy, "users", "alice", "sessions", "sess1")
	require.NoError(t, os.MkdirAll(aliceSrc, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(aliceSrc, "spec.json"), []byte(`alice`), 0o600))

	moved, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.True(t, moved)

	want := filepath.Join(l.WithUser("alice").SessionsDir(), "sess1", "spec.json")
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	require.Equal(t, "alice", string(got))
}

// TestMigrate_CrossDeviceFallback monkey-patches renameFunc to simulate
// EXDEV and verifies the copy+delete branch produces the same end state.
// We restore renameFunc on test teardown via t.Cleanup so other tests
// remain unaffected.
func TestMigrate_CrossDeviceFallback(t *testing.T) {
	home, l := migrateTestSetup(t)

	original := renameFunc
	t.Cleanup(func() { renameFunc = original })
	renameFunc = func(_, _ string) error {
		return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
	}

	legacy := filepath.Join(home, ".gil")
	srcDir := filepath.Join(legacy, "sessions", "sX")
	require.NoError(t, os.MkdirAll(srcDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "spec.json"), []byte("crossfs"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "gild.pid"), []byte("777"), 0o600))

	moved, err := MigrateLegacyTilde(l)
	require.NoError(t, err)
	require.True(t, moved)

	body, err := os.ReadFile(filepath.Join(l.SessionsDir(), "sX", "spec.json"))
	require.NoError(t, err)
	require.Equal(t, "crossfs", string(body))

	pid, err := os.ReadFile(l.Pid())
	require.NoError(t, err)
	require.Equal(t, "777", string(pid))

	// Source dir should be gone after copy+delete.
	_, err = os.Stat(srcDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(legacy, "gild.pid"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
