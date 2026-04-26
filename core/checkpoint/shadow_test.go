package checkpoint

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestShadowGit_InitIdempotent(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)

	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	// Second Init must be a no-op.
	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init (should be idempotent): %v", err)
	}
	// GitDir must exist as a directory.
	info, err := os.Stat(s.GitDir)
	if err != nil {
		t.Fatalf("GitDir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("GitDir is not a directory: %s", s.GitDir)
	}
}

func TestShadowGit_CommitAndList(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Write v1 and commit.
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1, err := s.Commit(ctx, "first")
	if err != nil {
		t.Fatalf("Commit first: %v", err)
	}
	if sha1 == "" {
		t.Fatal("expected non-empty SHA for first commit")
	}

	// Write v2 and commit.
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2, err := s.Commit(ctx, "second")
	if err != nil {
		t.Fatalf("Commit second: %v", err)
	}
	if sha2 == "" {
		t.Fatal("expected non-empty SHA for second commit")
	}
	if sha1 == sha2 {
		t.Fatal("expected different SHAs for different commits")
	}

	commits, err := s.ListCommits(ctx)
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	// Newest first.
	if commits[0].Message != "second" {
		t.Errorf("expected commits[0].Message=\"second\", got %q", commits[0].Message)
	}
	if commits[1].Message != "first" {
		t.Errorf("expected commits[1].Message=\"first\", got %q", commits[1].Message)
	}
	if commits[0].SHA != sha2 {
		t.Errorf("expected commits[0].SHA=%q, got %q", sha2, commits[0].SHA)
	}
}

func TestShadowGit_Restore_RevertsFile(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	filePath := filepath.Join(workspace, "file.txt")

	// Commit v1.
	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1, err := s.Commit(ctx, "first")
	if err != nil {
		t.Fatalf("Commit first: %v", err)
	}

	// Commit v2.
	if err := os.WriteFile(filePath, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, "second"); err != nil {
		t.Fatalf("Commit second: %v", err)
	}

	// Restore to sha1.
	if err := s.Restore(ctx, sha1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile after restore: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("expected \"v1\" after restore, got %q", string(got))
	}
}

func TestShadowGit_Restore_DeletedFile(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	aPath := filepath.Join(workspace, "a.txt")
	bPath := filepath.Join(workspace, "b.txt")

	// Write both files and commit.
	if err := os.WriteFile(aPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Commit(ctx, "both")
	if err != nil {
		t.Fatalf("Commit both: %v", err)
	}

	// Retrieve the "both" SHA from the log for a precise restore.
	commits, err := s.ListCommits(ctx)
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}
	bothSHA := commits[len(commits)-1].SHA // oldest = "both"

	// Delete b.txt and commit.
	if err := os.Remove(bPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, "del-b"); err != nil {
		t.Fatalf("Commit del-b: %v", err)
	}

	// Restore to "both" snapshot.
	if err := s.Restore(ctx, bothSHA); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// b.txt should be back.
	if _, err := os.Stat(bPath); err != nil {
		t.Errorf("b.txt should exist after restore, got: %v", err)
	}
}

func TestShadowGit_Reset_HardResetsHead(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	sg := New(workspace, t.TempDir())
	require.NoError(t, sg.Init(context.Background()))

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("v1"), 0o644))
	sha1, err := sg.Commit(context.Background(), "first")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("v2"), 0o644))
	_, err = sg.Commit(context.Background(), "second")
	require.NoError(t, err)

	// Reset to v1
	require.NoError(t, sg.Reset(context.Background(), sha1))

	// Working tree reverted
	data, err := os.ReadFile(filepath.Join(workspace, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "v1", string(data))

	// HEAD is now sha1 (unlike Restore which leaves HEAD untouched)
	commits, err := sg.ListCommits(context.Background())
	require.NoError(t, err)
	require.Len(t, commits, 1, "after --hard reset, history is just the v1 commit")
	require.Equal(t, sha1, commits[0].SHA)
}

func TestShadowGit_New_HashIsDeterministic(t *testing.T) {
	base := "/base"

	s1 := New("/work/a", base)
	s2 := New("/work/a", base)
	if s1.GitDir != s2.GitDir {
		t.Errorf("same workspace should produce same GitDir: %q vs %q", s1.GitDir, s2.GitDir)
	}

	s3 := New("/work/b", base)
	if s1.GitDir == s3.GitDir {
		t.Errorf("different workspaces should produce different GitDir: both %q", s1.GitDir)
	}
}

func TestShadowGit_DoesNotPolluteWorkspaceDotGit(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Write a file and commit.
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, "first"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The workspace itself must not contain a .git entry.
	dotGit := filepath.Join(workspace, ".git")
	if _, err := os.Stat(dotGit); !os.IsNotExist(err) {
		t.Errorf("workspace must not have a .git entry; stat returned: %v", err)
	}
}

func TestShadowGit_ListCommits_EmptyOnFreshInit(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not in PATH")
	}
	workspace := t.TempDir()
	base := t.TempDir()
	s := New(workspace, base)
	ctx := context.Background()

	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	commits, err := s.ListCommits(ctx)
	if err != nil {
		t.Fatalf("ListCommits on fresh init should not error: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits on fresh init, got %d", len(commits))
	}
}
