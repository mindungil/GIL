package patch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApply_AddFile(t *testing.T) {
	dir := t.TempDir()
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkAddFile, Path: "hello.txt", AddContents: "hi\n"}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	got, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.Equal(t, "hi\n", string(got))
}

func TestApply_AddFile_CreatesDirs(t *testing.T) {
	dir := t.TempDir()
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkAddFile, Path: "deep/nested/x.txt", AddContents: "hi"}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	_, err := os.Stat(filepath.Join(dir, "deep", "nested", "x.txt"))
	require.NoError(t, err)
}

func TestApply_DeleteFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkDeleteFile, Path: "gone.txt"}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	_, err := os.Stat(filepath.Join(dir, "gone.txt"))
	require.True(t, os.IsNotExist(err))
}

func TestApply_DeleteFile_Missing(t *testing.T) {
	dir := t.TempDir()
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkDeleteFile, Path: "nope"}}}
	rs := a.Apply(p)
	require.True(t, HasError(rs))
}

func TestApply_UpdateFile_Simple(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("alpha\nbeta\ngamma\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{
		{OldLines: []string{"beta"}, NewLines: []string{"BETA"}},
	}}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs), "errs: %v", rs)
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "alpha\nBETA\ngamma\n", string(got))
}

func TestApply_UpdateFile_WithContext(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("a\nb\nc\nd\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{
		{ChangeContext: "b", OldLines: []string{"c"}, NewLines: []string{"C"}},
	}}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "a\nb\nC\nd\n", string(got))
}

func TestApply_UpdateFile_MultipleChunks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("a\nb\nc\nd\ne\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{
		{OldLines: []string{"a"}, NewLines: []string{"A"}},
		{OldLines: []string{"d"}, NewLines: []string{"D"}},
	}}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "A\nb\nc\nD\ne\n", string(got))
}

func TestApply_UpdateFile_EOFAnchor(t *testing.T) {
	dir := t.TempDir()
	// "tail" appears at both index 0 and index 2; eof anchor must pick the LAST one.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("tail\nmid\ntail\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{
		{OldLines: []string{"tail"}, NewLines: []string{"TAIL"}, IsEndOfFile: true},
	}}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "tail\nmid\nTAIL\n", string(got))
}

func TestApply_UpdateFile_MoveTo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.go"), []byte("x\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "old.go", MovePath: "new.go", Chunks: []UpdateChunk{
		{OldLines: []string{"x"}, NewLines: []string{"X"}},
	}}}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	_, err := os.Stat(filepath.Join(dir, "old.go"))
	require.True(t, os.IsNotExist(err))
	got, _ := os.ReadFile(filepath.Join(dir, "new.go"))
	require.Equal(t, "X\n", string(got))
}

func TestApply_DryRun_NoMutation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("orig\n"), 0o644))
	a := &Applier{WorkspaceDir: dir, DryRun: true}
	p := &Patch{Hunks: []Hunk{
		{Kind: HunkAddFile, Path: "new.txt", AddContents: "x"},
		{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{{OldLines: []string{"orig"}, NewLines: []string{"NEW"}}}},
	}}
	rs := a.Apply(p)
	require.False(t, HasError(rs))
	// No filesystem mutation occurred.
	_, err := os.Stat(filepath.Join(dir, "new.txt"))
	require.True(t, os.IsNotExist(err))
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "orig\n", string(got))
	// But results indicate Applied=true.
	require.True(t, rs[0].Applied)
	require.True(t, rs[1].Applied)
}

func TestApply_ChunkNotFound_ReportsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("alpha\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{{Kind: HunkUpdateFile, Path: "x.go", Chunks: []UpdateChunk{
		{OldLines: []string{"completely different"}, NewLines: []string{"x"}},
	}}}}
	rs := a.Apply(p)
	require.True(t, HasError(rs))
}

func TestApply_ContinuesOnPerHunkError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.go"), []byte("y\n"), 0o644))
	a := &Applier{WorkspaceDir: dir}
	p := &Patch{Hunks: []Hunk{
		{Kind: HunkDeleteFile, Path: "missing"}, // will fail
		{Kind: HunkAddFile, Path: "good2.go", AddContents: "ok\n"}, // should still be applied
	}}
	rs := a.Apply(p)
	require.Len(t, rs, 2)
	require.NotNil(t, rs[0].Err)
	require.Nil(t, rs[1].Err)
	require.True(t, rs[1].Applied)
	_, err := os.Stat(filepath.Join(dir, "good2.go"))
	require.NoError(t, err)
}
