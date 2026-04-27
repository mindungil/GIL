package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func TestBank_Init_CreatesAllStubs(t *testing.T) {
	dir := t.TempDir()
	b := New(filepath.Join(dir, "memory"))
	require.NoError(t, b.Init())
	for _, f := range AllFiles {
		path := filepath.Join(b.Dir, f)
		_, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", f)
	}
}

func TestBank_Init_DoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	b := New(filepath.Join(dir, "memory"))
	require.NoError(t, b.Init())
	require.NoError(t, b.Write(FileProgress, "custom content\n"))
	require.NoError(t, b.Init()) // call again
	got, err := b.Read(FileProgress)
	require.NoError(t, err)
	require.Equal(t, "custom content\n", got)
}

func TestBank_InitFromSpec_PopulatesUntouchedFiles(t *testing.T) {
	dir := t.TempDir()
	b := New(filepath.Join(dir, "memory"))
	require.NoError(t, b.Init())
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{
			OneLiner:               "build a CLI",
			SuccessCriteriaNatural: []string{"runs", "tests pass"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"go", "cobra"}},
	}
	populated, err := b.InitFromSpec(spec)
	require.NoError(t, err)
	require.Contains(t, populated, FileProjectBrief)
	require.Contains(t, populated, FileTechContext)
	pb, _ := b.Read(FileProjectBrief)
	require.Contains(t, pb, "build a CLI")
	require.Contains(t, pb, "runs")
}

func TestBank_InitFromSpec_LeavesCustomFilesAlone(t *testing.T) {
	dir := t.TempDir()
	b := New(filepath.Join(dir, "memory"))
	require.NoError(t, b.Init())
	require.NoError(t, b.Write(FileProjectBrief, "MY CUSTOM BRIEF"))
	spec := &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "something else"}}
	populated, err := b.InitFromSpec(spec)
	require.NoError(t, err)
	require.NotContains(t, populated, FileProjectBrief)
	got, _ := b.Read(FileProjectBrief)
	require.Equal(t, "MY CUSTOM BRIEF", got)
}

func TestBank_Read_UnknownFile(t *testing.T) {
	b := New(t.TempDir())
	_, err := b.Read("nope.md")
	require.ErrorIs(t, err, ErrUnknownFile)
}

func TestBank_Read_NotFound(t *testing.T) {
	b := New(t.TempDir())
	_, err := b.Read(FileProgress)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestBank_Read_AcceptsShortName(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, "hi"))
	got, err := b.Read("progress")
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}

func TestBank_Write_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "memory")
	b := New(dir)
	require.NoError(t, b.Write(FileProgress, "x"))
	got, _ := b.Read(FileProgress)
	require.Equal(t, "x", got)
}

func TestBank_Append_NoFileYet(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Append(FileProgress, "first"))
	got, _ := b.Read(FileProgress)
	require.Equal(t, "first", got)
}

func TestBank_Append_AddsNewlineIfMissing(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, "abc"))
	require.NoError(t, b.Append(FileProgress, "def"))
	got, _ := b.Read(FileProgress)
	require.Equal(t, "abc\ndef", got)
}

func TestBank_AppendSection_AddsNewSection(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, "## Done\n- a\n"))
	require.NoError(t, b.AppendSection(FileProgress, "Blocked", "- nothing"))
	got, _ := b.Read(FileProgress)
	require.Contains(t, got, "## Blocked")
	require.Contains(t, got, "- nothing")
}

func TestBank_AppendSection_AppendsToExistingSection(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, "## Done\n- a\n## Blocked\n- x\n"))
	require.NoError(t, b.AppendSection(FileProgress, "Done", "- b"))
	got, _ := b.Read(FileProgress)
	// - b should appear after - a but before ## Blocked
	aIdx := strings.Index(got, "- a")
	bIdx := strings.Index(got, "- b")
	blkIdx := strings.Index(got, "## Blocked")
	require.Greater(t, bIdx, aIdx)
	require.Less(t, bIdx, blkIdx)
}

func TestBank_Snapshot_OmitsMissingFiles(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, "a"))
	require.NoError(t, b.Write(FileTechContext, "b"))
	snap, err := b.Snapshot()
	require.NoError(t, err)
	require.Len(t, snap, 2)
	require.Equal(t, "a", snap[FileProgress])
	require.Equal(t, "b", snap[FileTechContext])
	_, has := snap[FileProjectBrief]
	require.False(t, has)
}

func TestBank_EstimateTokens(t *testing.T) {
	b := New(t.TempDir())
	require.NoError(t, b.Write(FileProgress, strings.Repeat("x", 400)))
	n, err := b.EstimateTokens()
	require.NoError(t, err)
	require.InDelta(t, 100, n, 5) // ~400 chars / 4
}

func TestBank_Write_UnknownFileRejects(t *testing.T) {
	b := New(t.TempDir())
	err := b.Write("evil.md", "x")
	require.ErrorIs(t, err, ErrUnknownFile)
}
