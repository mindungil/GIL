package repomap_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/repomap"
)

func TestWalkProject_AggregatesSymbols(t *testing.T) {
	syms, warns, err := repomap.WalkProject(context.Background(), "testdata/project", repomap.WalkOptions{})
	require.NoError(t, err)
	require.Empty(t, warns, "should have no warnings; got: %v", warns)

	files := make(map[string]bool)
	for _, fs := range syms {
		files[fs.File] = true
	}
	require.Contains(t, files, "main.go")
	require.Contains(t, files, filepath.Join("pkg", "util.go"))
	require.Contains(t, files, filepath.Join("scripts", "run.py"))
	require.Contains(t, files, filepath.Join("web", "app.ts"))
	// Excluded
	require.NotContains(t, files, filepath.Join("vendor", "lib.go"))
	require.NotContains(t, files, filepath.Join("node_modules", "x.js"))
	// Non-source
	require.NotContains(t, files, "README.md")
}

func TestWalkProject_SymbolFileIsRelative(t *testing.T) {
	syms, _, err := repomap.WalkProject(context.Background(), "testdata/project", repomap.WalkOptions{})
	require.NoError(t, err)
	for _, fs := range syms {
		require.False(t, filepath.IsAbs(fs.File), "file should be relative: %s", fs.File)
		for _, d := range fs.Defs {
			require.Equal(t, fs.File, d.File)
		}
	}
}

func TestWalkProject_MaxFileSize(t *testing.T) {
	// Set up a temp project copy with a giant file
	tmp := t.TempDir()
	require.NoError(t, copyTree("testdata/project", tmp))
	big := filepath.Join(tmp, "huge.go")
	var buf bytes.Buffer
	buf.WriteString("package x\n")
	for i := 0; i < 15000; i++ {
		fmt.Fprintf(&buf, "var V%d = %d\n", i, i)
	}
	require.NoError(t, os.WriteFile(big, buf.Bytes(), 0o644))

	// Default 256 KB → should skip if buf > 256 KB
	info, _ := os.Stat(big)
	require.Greater(t, info.Size(), int64(256*1024), "test setup: file should be > 256 KB")

	syms, warns, err := repomap.WalkProject(context.Background(), tmp, repomap.WalkOptions{})
	require.NoError(t, err)
	files := map[string]bool{}
	for _, fs := range syms {
		files[fs.File] = true
	}
	require.NotContains(t, files, "huge.go")
	// Warning should mention skip
	var found bool
	for _, w := range warns {
		if strings.Contains(w, "huge.go") && strings.Contains(w, "skipped") {
			found = true
		}
	}
	require.True(t, found, "expected a 'skipped' warning for huge.go; warnings: %v", warns)
}

func TestWalkProject_CustomExclude(t *testing.T) {
	// Exclude "scripts" directory + "*.ts" files
	syms, _, err := repomap.WalkProject(context.Background(), "testdata/project", repomap.WalkOptions{
		Exclude: []string{"scripts", "*.ts"},
	})
	require.NoError(t, err)
	for _, fs := range syms {
		require.NotContains(t, fs.File, "scripts")
		require.False(t, strings.HasSuffix(fs.File, ".ts"))
	}
}

func TestWalkProject_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	syms, _, err := repomap.WalkProject(ctx, "testdata/project", repomap.WalkOptions{})
	require.ErrorIs(t, err, context.Canceled)
	// syms may or may not be empty depending on timing; just ensure no panic
	_ = syms
}

func TestWalkProject_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	syms, warns, err := repomap.WalkProject(context.Background(), tmp, repomap.WalkOptions{})
	require.NoError(t, err)
	require.Empty(t, syms)
	require.Empty(t, warns)
}

func TestWalkProject_WarnsOnParseError(t *testing.T) {
	tmp := t.TempDir()
	// Create a syntactically invalid Go file
	bad := filepath.Join(tmp, "bad.go")
	require.NoError(t, os.WriteFile(bad, []byte("package x\nfunc !!! {"), 0o644))
	syms, warns, err := repomap.WalkProject(context.Background(), tmp, repomap.WalkOptions{})
	require.NoError(t, err)
	require.Empty(t, syms)
	require.NotEmpty(t, warns)
	require.Contains(t, warns[0], "bad.go")
}

// copyTree is a test helper that recursively copies src to dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
