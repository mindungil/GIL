package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func initGitRepo(t *testing.T, branch string, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	runGit("init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644))
	runGit("add", "README.md")
	runGit("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	runGit("branch", "-M", branch)
	if dirty {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty\n"), 0o644))
	}
	return dir
}

func TestGitWorkspaceSummary_CachesSubprocessCalls(t *testing.T) {
	clearGitSummaryCache()
	t.Cleanup(clearGitSummaryCache)

	binDir := t.TempDir()
	workdir := t.TempDir()
	counterPath := filepath.Join(t.TempDir(), "git-count")
	script := "#!/bin/sh\n" +
		"count=$(cat \"" + counterPath + "\" 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\n" +
		"printf '%s' \"$count\" > \"" + counterPath + "\"\n" +
		"printf '## feat/cache-test\\n'\n" +
		"printf ' M file.txt\\n'\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	oldTTL := gitSummaryCacheTTL
	gitSummaryCacheTTL = time.Hour
	t.Cleanup(func() { gitSummaryCacheTTL = oldTTL })

	require.Equal(t, "feat/cache-test · dirty (1 files)", gitWorkspaceSummary(context.Background(), workdir))
	require.Equal(t, "feat/cache-test · dirty (1 files)", gitWorkspaceSummary(context.Background(), workdir))

	data, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	require.Equal(t, "1", string(data))
}

func TestGitWorkspaceSummary_CachesEmptyResult(t *testing.T) {
	clearGitSummaryCache()
	t.Cleanup(clearGitSummaryCache)

	binDir := t.TempDir()
	workdir := t.TempDir()
	counterPath := filepath.Join(t.TempDir(), "git-count")
	script := "#!/bin/sh\n" +
		"count=$(cat \"" + counterPath + "\" 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\n" +
		"printf '%s' \"$count\" > \"" + counterPath + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	oldTTL := gitSummaryCacheTTL
	gitSummaryCacheTTL = time.Hour
	t.Cleanup(func() { gitSummaryCacheTTL = oldTTL })

	require.Empty(t, gitWorkspaceSummary(context.Background(), workdir))
	require.Empty(t, gitWorkspaceSummary(context.Background(), workdir))

	data, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	require.Equal(t, "1", string(data))
}
