package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// newRunSvcWithRepo is a thin wrapper that lets the diff tests reuse
// the standard newRunSvc factory without spinning up a mock provider
// (the service factory expects one). Returns the service and a
// callable session-creator so tests can stage a real session row.
func newRunSvcWithRepo(t *testing.T) (*RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(nil), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsBase, 0o755))
	return NewRunService(repo, sessionsBase, factory), repo, sessionsBase
}

// TestDiff_SessionNotFound returns a NotFound code so surfaces can
// distinguish "bad id" from "no checkpoints".
func TestDiff_SessionNotFound(t *testing.T) {
	svc, _, _ := newRunSvcWithRepo(t)
	_, err := svc.Diff(context.Background(), &gilv1.DiffRequest{SessionId: "missing"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestDiff_NoCheckpointsYet covers the "session exists but never
// produced a snapshot" path. The note carries a friendly message and
// the counts stay zero so the surface can render either.
func TestDiff_NoCheckpointsYet(t *testing.T) {
	svc, repo, _ := newRunSvcWithRepo(t)
	workDir := t.TempDir()
	sess, err := repo.Create(context.Background(), session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)

	resp, err := svc.Diff(context.Background(), &gilv1.DiffRequest{SessionId: sess.ID})
	require.NoError(t, err)
	require.Equal(t, "no checkpoints yet for this session", resp.Note)
	require.Empty(t, resp.UnifiedDiff)
	require.Zero(t, resp.FilesChanged)
	require.Zero(t, resp.LinesAdded)
	require.Zero(t, resp.LinesRemoved)
}

// TestDiff_DetectsModifications stages a synthetic checkpoint, mutates
// the workspace, and checks that Diff reports the expected stats and
// body. The fixture uses the same shadow-git layout the runner
// produces (sessionsBase/<id>/shadow) so we exercise the real
// resolution path.
func TestDiff_DetectsModifications(t *testing.T) {
	svc, repo, sessionsBase := newRunSvcWithRepo(t)
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("v1\nline2\n"), 0o644))

	sess, err := repo.Create(context.Background(), session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)

	shadowBase := filepath.Join(sessionsBase, sess.ID, "shadow")
	require.NoError(t, os.MkdirAll(shadowBase, 0o755))
	sg := checkpoint.New(workDir, shadowBase)
	require.NoError(t, sg.Init(context.Background()))
	headSHA, err := sg.Commit(context.Background(), "initial")
	require.NoError(t, err)

	// Mutate workspace: rewrite line, add new file.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("v2\nline2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "g.txt"), []byte("hello\n"), 0o644))

	resp, err := svc.Diff(context.Background(), &gilv1.DiffRequest{SessionId: sess.ID})
	require.NoError(t, err)
	require.Equal(t, headSHA, resp.CheckpointSha)
	require.Contains(t, resp.UnifiedDiff, "-v1")
	require.Contains(t, resp.UnifiedDiff, "+v2")
	require.Contains(t, resp.UnifiedDiff, "g.txt")
	require.Equal(t, int32(2), resp.FilesChanged) // f.txt changed + g.txt added
	require.Greater(t, resp.LinesAdded, int32(0))
	require.False(t, resp.Truncated)
}

// TestDiff_TruncatesLargeBody asserts the 16 KB cap fires and the
// truncated flag + byte count are populated. We force a large diff by
// adding a single big file post-checkpoint.
func TestDiff_TruncatesLargeBody(t *testing.T) {
	svc, repo, sessionsBase := newRunSvcWithRepo(t)
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "seed.txt"), []byte("seed\n"), 0o644))

	sess, err := repo.Create(context.Background(), session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)

	shadowBase := filepath.Join(sessionsBase, sess.ID, "shadow")
	require.NoError(t, os.MkdirAll(shadowBase, 0o755))
	sg := checkpoint.New(workDir, shadowBase)
	require.NoError(t, sg.Init(context.Background()))
	_, err = sg.Commit(context.Background(), "initial")
	require.NoError(t, err)

	// Write a ~32 KB file to blow past the 16 KB cap.
	big := strings.Repeat("abcdefghij\n", 3000) // ~33 KB
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "big.txt"), []byte(big), 0o644))

	resp, err := svc.Diff(context.Background(), &gilv1.DiffRequest{SessionId: sess.ID})
	require.NoError(t, err)
	require.True(t, resp.Truncated)
	require.Greater(t, resp.TruncatedBytes, int32(0))
	require.LessOrEqual(t, len(resp.UnifiedDiff), diffMaxBytes+128) // body + truncation marker
	require.Contains(t, resp.UnifiedDiff, "bytes truncated")
}

// TestParseDiffStat covers the summary parser's edge cases — the diff
// body uses the standard --stat trailing line whose shape changes
// based on whether insertions or deletions exist.
func TestParseDiffStat(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantAdded   int
		wantRemoved int
		wantFiles   int
	}{
		{
			name:        "both insertions and deletions",
			in:          " f1 | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n",
			wantAdded:   1,
			wantRemoved: 1,
			wantFiles:   1,
		},
		{
			name:      "insertions only",
			in:        " f1 | 3 +++\n 1 file changed, 3 insertions(+)\n",
			wantAdded: 3,
			wantFiles: 1,
		},
		{
			name:        "deletions only",
			in:          " f1 | 2 --\n 1 file changed, 2 deletions(-)\n",
			wantRemoved: 2,
			wantFiles:   1,
		},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r, f := parseDiffStat(tc.in)
			require.Equal(t, tc.wantAdded, a)
			require.Equal(t, tc.wantRemoved, r)
			require.Equal(t, tc.wantFiles, f)
		})
	}
}
