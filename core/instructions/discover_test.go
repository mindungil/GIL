package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFile is a t.TempDir helper that creates intermediate directories.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestDiscover_EmptyWorkspaceReturnsNil(t *testing.T) {
	srcs, err := Discover(DiscoverOptions{})
	require.NoError(t, err)
	require.Nil(t, srcs)
}

func TestDiscover_MissingWorkspaceReturnsNil(t *testing.T) {
	srcs, err := Discover(DiscoverOptions{Workspace: "/nonexistent/path/that/should/never/exist"})
	require.NoError(t, err)
	require.Nil(t, srcs)
}

func TestDiscover_WorkspaceAGENTSMD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# Project conventions\nUse tabs.\n")

	srcs, err := Discover(DiscoverOptions{Workspace: dir})
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	require.Equal(t, "workspace", srcs[0].Origin)
	require.Contains(t, srcs[0].Body, "Use tabs.")
}

func TestDiscover_WorkspaceCLAUDEMDIncludedByDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "agents body\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude body\n")

	srcs, err := Discover(DiscoverOptions{Workspace: dir})
	require.NoError(t, err)
	require.Len(t, srcs, 2)

	// AGENTS.md is read before CLAUDE.md within a layer.
	require.Equal(t, "AGENTS.md", filepath.Base(srcs[0].Path))
	require.Equal(t, "CLAUDE.md", filepath.Base(srcs[1].Path))
}

func TestDiscover_DisableClaudeMDSkipsCLAUDEMD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "agents\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude\n")

	srcs, err := Discover(DiscoverOptions{Workspace: dir, DisableClaudeMD: true})
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	require.Equal(t, "AGENTS.md", filepath.Base(srcs[0].Path))
}

func TestDiscover_AncestorAGENTSMDFoundUntilGitRoot(t *testing.T) {
	root := t.TempDir()
	// Build: <root>/.git/, <root>/AGENTS.md, <root>/sub/AGENTS.md, <root>/sub/leaf/
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root agents\n")
	writeFile(t, filepath.Join(root, "sub", "AGENTS.md"), "sub agents\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "leaf"), 0o755))
	writeFile(t, filepath.Join(root, "sub", "leaf", "AGENTS.md"), "leaf agents\n")

	srcs, err := Discover(DiscoverOptions{
		Workspace:     filepath.Join(root, "sub", "leaf"),
		StopAtGitRoot: true,
	})
	require.NoError(t, err)
	require.Len(t, srcs, 3)

	// Order: highest ancestor first → workspace last.
	require.Contains(t, srcs[0].Body, "root agents")
	require.Equal(t, "ancestor", srcs[0].Origin)
	require.Contains(t, srcs[1].Body, "sub agents")
	require.Equal(t, "ancestor", srcs[1].Origin)
	require.Contains(t, srcs[2].Body, "leaf agents")
	require.Equal(t, "workspace", srcs[2].Origin)
}

func TestDiscover_StopAtGitRootFalseWalksFurther(t *testing.T) {
	// Directory with a git root, then a workspace deep inside, then turn
	// off StopAtGitRoot — we should keep walking past the git boundary.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outer", ".git"), 0o755))
	writeFile(t, filepath.Join(root, "AGENTS.md"), "above git\n")
	writeFile(t, filepath.Join(root, "outer", "AGENTS.md"), "git root\n")
	writeFile(t, filepath.Join(root, "outer", "inner", "AGENTS.md"), "ws agents\n")

	srcsStop, err := Discover(DiscoverOptions{
		Workspace:     filepath.Join(root, "outer", "inner"),
		StopAtGitRoot: true,
	})
	require.NoError(t, err)
	// Should include outer (git root) + inner (workspace) but NOT root.
	require.Len(t, srcsStop, 2)
	require.Contains(t, srcsStop[0].Body, "git root")
	require.Contains(t, srcsStop[1].Body, "ws agents")

	srcsAll, err := Discover(DiscoverOptions{
		Workspace:     filepath.Join(root, "outer", "inner"),
		StopAtGitRoot: false,
	})
	require.NoError(t, err)
	// Should include root (above git) too. Note: the t.TempDir parent
	// chain may yield additional empty ancestors; check that the root one
	// is present.
	bodies := make([]string, 0, len(srcsAll))
	for _, s := range srcsAll {
		bodies = append(bodies, s.Body)
	}
	require.Contains(t, strings.Join(bodies, "\n"), "above git")
}

func TestDiscover_GlobalConfigDirAGENTSMDIncluded(t *testing.T) {
	ws := t.TempDir()
	global := t.TempDir()
	writeFile(t, filepath.Join(global, "AGENTS.md"), "global default\n")
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "ws override\n")

	srcs, err := Discover(DiscoverOptions{
		Workspace:       ws,
		GlobalConfigDir: global,
	})
	require.NoError(t, err)
	require.Len(t, srcs, 2)

	// Global must come before workspace (lower priority first).
	require.Equal(t, "global", srcs[0].Origin)
	require.Contains(t, srcs[0].Body, "global default")
	require.Equal(t, "workspace", srcs[1].Origin)
	require.Contains(t, srcs[1].Body, "ws override")
}

func TestDiscover_HomeDirIsLowestPriority(t *testing.T) {
	ws := t.TempDir()
	global := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "AGENTS.md"), "home agents\n")
	writeFile(t, filepath.Join(global, "AGENTS.md"), "global agents\n")
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "ws agents\n")

	srcs, err := Discover(DiscoverOptions{
		Workspace:       ws,
		GlobalConfigDir: global,
		HomeDir:         home,
	})
	require.NoError(t, err)
	require.Len(t, srcs, 3)
	require.Equal(t, "home", srcs[0].Origin)
	require.Equal(t, "global", srcs[1].Origin)
	require.Equal(t, "workspace", srcs[2].Origin)
}

func TestDiscover_CursorRulesConcatenated(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".cursor", "rules", "z-last.mdc"), "z rule\n")
	writeFile(t, filepath.Join(ws, ".cursor", "rules", "a-first.mdc"), "a rule\n")
	writeFile(t, filepath.Join(ws, ".cursor", "rules", "ignored.txt"), "not mdc\n")

	srcs, err := Discover(DiscoverOptions{Workspace: ws})
	require.NoError(t, err)
	require.Len(t, srcs, 2)

	// Sorted alphabetically for determinism.
	require.Equal(t, "cursor", srcs[0].Origin)
	require.Equal(t, "a-first.mdc", filepath.Base(srcs[0].Path))
	require.Equal(t, "z-last.mdc", filepath.Base(srcs[1].Path))
}

func TestDiscover_DisableCursorSkipsCursorRules(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "AGENTS.md"), "agents\n")
	writeFile(t, filepath.Join(ws, ".cursor", "rules", "x.mdc"), "rule\n")

	srcs, err := Discover(DiscoverOptions{Workspace: ws, DisableCursor: true})
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	require.Equal(t, "AGENTS.md", filepath.Base(srcs[0].Path))
}

func TestDiscover_EmptyAndMissingFilesDontError(t *testing.T) {
	ws := t.TempDir()
	// AGENTS.md exists but is zero bytes.
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), nil, 0o644))
	// CLAUDE.md is whitespace-only.
	writeFile(t, filepath.Join(ws, "CLAUDE.md"), "   \n  \n")

	srcs, err := Discover(DiscoverOptions{Workspace: ws})
	require.NoError(t, err)
	require.Empty(t, srcs)
}

func TestDiscover_PerFileTruncationMarker(t *testing.T) {
	ws := t.TempDir()
	// 80 KB of "x" — exceeds 64 KB per-file cap.
	big := strings.Repeat("x", 80*1024)
	writeFile(t, filepath.Join(ws, "AGENTS.md"), big)

	srcs, err := Discover(DiscoverOptions{Workspace: ws})
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	require.Contains(t, srcs[0].Body, "[truncated]")
	require.LessOrEqual(t, len(srcs[0].Body), 64*1024+len("\n\n... [truncated]"))
}

func TestRender_DropsLowestPriorityWhenOverBudget(t *testing.T) {
	srcs := []Source{
		{Path: "/h/AGENTS.md", Origin: "home", Body: strings.Repeat("h", 4000)},
		{Path: "/g/AGENTS.md", Origin: "global", Body: strings.Repeat("g", 4000)},
		{Path: "/w/AGENTS.md", Origin: "workspace", Body: strings.Repeat("w", 4000)},
	}
	out := Render(srcs, 6000)
	// home + global must be dropped; only workspace fits.
	require.NotContains(t, out, "home")
	require.NotContains(t, out, "global")
	require.Contains(t, out, "workspace")
	require.Contains(t, out, strings.Repeat("w", 4000))
}

func TestRender_IncludesBeginEndDelimitersWithOriginAndBasename(t *testing.T) {
	srcs := []Source{
		{Path: "/abs/dir/AGENTS.md", Origin: "workspace", Body: "hello\n"},
	}
	out := Render(srcs, 0)
	require.Contains(t, out, "--- BEGIN workspace: AGENTS.md ---")
	require.Contains(t, out, "--- END workspace: AGENTS.md ---")
	require.Contains(t, out, "hello")
}

func TestRender_EmptyInputReturnsEmptyString(t *testing.T) {
	require.Empty(t, Render(nil, 0))
	require.Empty(t, Render([]Source{}, 0))
}

func TestDiscover_SymlinkEscapingWorkspaceIsRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.md"), "leak\n")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	srcs, err := Discover(DiscoverOptions{Workspace: ws})
	require.NoError(t, err)
	require.Empty(t, srcs)
}

func TestDiscover_AncestorWalkTerminatesAtFilesystemRoot(t *testing.T) {
	// Just call with a deep nested workspace and StopAtGitRoot=false:
	// the loop must terminate.
	ws := t.TempDir()
	srcs, err := Discover(DiscoverOptions{Workspace: ws, StopAtGitRoot: false})
	require.NoError(t, err)
	// Nothing planted, so result is empty (but not infinite).
	_ = srcs
}
