package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestRunner_AllPass(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "true-check", Kind: gilv1.CheckKind_SHELL, Command: "true"},
		{Name: "echo-check", Kind: gilv1.CheckKind_SHELL, Command: "echo ok"},
	}

	results, allPass := r.RunAll(context.Background(), checks)
	require.True(t, allPass)
	require.Len(t, results, 2)
	for _, res := range results {
		require.True(t, res.Passed, "expected pass for %s", res.Name)
		require.Equal(t, 0, res.ExitCode)
	}
}

func TestRunner_OnePassOneFail(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "ok", Kind: gilv1.CheckKind_SHELL, Command: "true"},
		{Name: "bad", Kind: gilv1.CheckKind_SHELL, Command: "exit 5"},
	}

	results, allPass := r.RunAll(context.Background(), checks)
	require.False(t, allPass)
	require.True(t, results[0].Passed)
	require.False(t, results[1].Passed)
	require.Equal(t, 5, results[1].ExitCode)
}

func TestRunner_RespectsExpectedExitCode(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "expect-2", Kind: gilv1.CheckKind_SHELL, Command: "exit 2", ExpectedExitCode: 2},
	}
	results, allPass := r.RunAll(context.Background(), checks)
	require.True(t, allPass) // exit 2 matches expected
	require.True(t, results[0].Passed)
}

func TestRunner_FilesystemContext(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir)
	checks := []*gilv1.Check{
		{Name: "create", Kind: gilv1.CheckKind_SHELL, Command: "touch ./local"},
	}
	results, allPass := r.RunAll(context.Background(), checks)
	require.True(t, allPass)
	require.True(t, results[0].Passed)
	// File was created in dir (Cwd), not /
	_, err := os.Stat(filepath.Join(dir, "local"))
	require.NoError(t, err)
}

func TestRunner_CapturesStdoutStderr(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "noisy", Kind: gilv1.CheckKind_SHELL, Command: "echo out; echo err 1>&2"},
	}
	results, _ := r.RunAll(context.Background(), checks)
	require.True(t, results[0].Passed)
	require.Contains(t, results[0].Stdout, "out")
	require.Contains(t, results[0].Stderr, "err")
}

func TestRunner_TruncatesLargeOutput(t *testing.T) {
	r := NewRunner(t.TempDir())
	checks := []*gilv1.Check{
		{Name: "big", Kind: gilv1.CheckKind_SHELL, Command: "yes hi | head -c 10000"},
	}
	results, _ := r.RunAll(context.Background(), checks)
	require.Contains(t, results[0].Stdout, "(truncated)")
}

func TestRunner_EmptyChecks(t *testing.T) {
	r := NewRunner(t.TempDir())
	results, allPass := r.RunAll(context.Background(), nil)
	require.True(t, allPass) // no checks → vacuously true
	require.Empty(t, results)
}
