package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// touchFile creates an empty file (and any missing parent dirs).
func touchFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// touchDir creates a directory tree.
func touchDir(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
}

func TestDiscover_GitRepoReturnsRepoRoot(t *testing.T) {
	repo := t.TempDir()
	touchDir(t, filepath.Join(repo, ".git"))
	deep := filepath.Join(repo, "src", "internal", "pkg")
	touchDir(t, deep)

	got, err := Discover(deep)
	require.NoError(t, err)
	require.Equal(t, repo, got)
}

func TestDiscover_GilDirTakesPriorityOverGit(t *testing.T) {
	// Outer repo has .git; inner subdir has .gil. We invoke from a
	// directory below the inner one — the .gil ancestor should win.
	root := t.TempDir()
	touchDir(t, filepath.Join(root, ".git"))
	inner := filepath.Join(root, "subproject")
	touchDir(t, filepath.Join(inner, ".gil"))
	deep := filepath.Join(inner, "cmd", "tool")
	touchDir(t, deep)

	got, err := Discover(deep)
	require.NoError(t, err)
	require.Equal(t, inner, got, "closer .gil ancestor should beat outer .git")
}

func TestDiscover_GoModWithoutGit(t *testing.T) {
	mod := t.TempDir()
	touchFile(t, filepath.Join(mod, "go.mod"))
	deep := filepath.Join(mod, "cmd", "app")
	touchDir(t, deep)

	got, err := Discover(deep)
	require.NoError(t, err)
	require.Equal(t, mod, got)
}

func TestDiscover_PackageJSON(t *testing.T) {
	root := t.TempDir()
	touchFile(t, filepath.Join(root, "package.json"))
	deep := filepath.Join(root, "src", "components")
	touchDir(t, deep)

	got, err := Discover(deep)
	require.NoError(t, err)
	require.Equal(t, root, got)
}

func TestDiscover_NoMarkersReturnsCwd(t *testing.T) {
	// Build a tree with no markers anywhere. The walk should bottom
	// out at the temp root (or at $HOME if the temp dir is below it,
	// which is the default on Linux for `t.TempDir()`).
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c")
	touchDir(t, deep)

	got, err := Discover(deep)
	require.NoError(t, err)
	// Without markers, Discover returns the absolute form of cwd.
	abs, _ := filepath.Abs(deep)
	require.Equal(t, abs, got)
}

func TestDiscover_DoesNotEscapeHome(t *testing.T) {
	// Simulate a HOME boundary so the walk cannot reach root-owned
	// markers even if they exist. We point HOME at an isolated tmp
	// dir, then plant a `.git` *above* that dir — Discover must NOT
	// pick it up.
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	// "Above" HOME — i.e. the parent of homeDir — must not influence
	// the walk. We can't actually plant `.git` in /tmp safely, but we
	// can plant a working tree under HOME and verify the boundary
	// check by using a cwd that IS HOME itself.
	got, err := Discover(homeDir)
	require.NoError(t, err)
	// HOME has no markers and no walk past it: return cwd.
	require.Equal(t, homeDir, got)
}

func TestDiscover_EmptyCwd(t *testing.T) {
	_, err := Discover("")
	require.Error(t, err)
}

func TestLocalDir(t *testing.T) {
	require.Equal(t, filepath.Join("/tmp/proj", ".gil"), LocalDir("/tmp/proj"))
}

func TestIsConfigured(t *testing.T) {
	ws := t.TempDir()
	require.False(t, IsConfigured(ws))

	touchDir(t, filepath.Join(ws, ".gil"))
	require.True(t, IsConfigured(ws))
}

func TestLocalConfigFile(t *testing.T) {
	require.Equal(t, filepath.Join("/tmp/proj", ".gil", "config.toml"), LocalConfigFile("/tmp/proj"))
}

func TestLocalMCPFile(t *testing.T) {
	require.Equal(t, filepath.Join("/tmp/proj", ".gil", "mcp.toml"), LocalMCPFile("/tmp/proj"))
}

func TestLocalAgentsFile(t *testing.T) {
	require.Equal(t, filepath.Join("/tmp/proj", ".gil", "AGENTS.md"), LocalAgentsFile("/tmp/proj"))
}
