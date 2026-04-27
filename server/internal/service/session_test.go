package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mindungil/gil/core/session"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func newTestService(t *testing.T) *SessionService {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	return NewSessionService(session.NewRepo(db), nil)
}

func TestSessionService_Create(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &gilv1.CreateRequest{
		WorkingDir: "/tmp/x",
		GoalHint:   "test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Id)
	require.Equal(t, "/tmp/x", resp.WorkingDir)
	require.Equal(t, gilv1.SessionStatus_CREATED, resp.Status)
}

func TestSessionService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
	require.NoError(t, err)

	got, err := svc.Get(ctx, &gilv1.GetRequest{Id: created.Id})
	require.NoError(t, err)
	require.Equal(t, created.Id, got.Id)
}

func TestSessionService_List(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	resp, err := svc.List(ctx, &gilv1.ListRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, resp.Sessions, 2)
}

func TestSessionService_Get_NotFound(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Get(ctx, &gilv1.GetRequest{Id: "nonexistent"})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestSessionService_Delete covers the row-only delete path (no
// sessionsBase wired). Deleting twice must return NotFound the second
// time so a CLI batch-delete can render an honest count.
func TestSessionService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
	require.NoError(t, err)

	resp, err := svc.Delete(ctx, &gilv1.DeleteRequest{Id: created.Id})
	require.NoError(t, err)
	require.Equal(t, int64(0), resp.FreedBytes) // no sessionsBase wired

	// Second delete is NotFound.
	_, err = svc.Delete(ctx, &gilv1.DeleteRequest{Id: created.Id})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestSessionService_Delete_UnlinksDirectory exercises the on-disk
// side-effect: when sessionsBase is set we should remove the per-session
// directory and report freed bytes.
func TestSessionService_Delete_UnlinksDirectory(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	sessionsBase := filepath.Join(dir, "sessions")
	svc := NewSessionService(session.NewRepo(db), nil).WithSessionsBase(sessionsBase)

	ctx := context.Background()
	created, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
	require.NoError(t, err)

	// Drop a fake artefact so freed_bytes is non-zero.
	sd := filepath.Join(sessionsBase, created.Id)
	require.NoError(t, os.MkdirAll(sd, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sd, "spec.json"), []byte("hello"), 0o644))

	resp, err := svc.Delete(ctx, &gilv1.DeleteRequest{Id: created.Id})
	require.NoError(t, err)
	require.Equal(t, int64(5), resp.FreedBytes)

	// Directory should be gone.
	_, err = os.Stat(sd)
	require.True(t, os.IsNotExist(err))
}

// TestSessionService_Delete_RefusesRunning verifies the
// FailedPrecondition guard for in-flight runs. We mark the session
// status manually via the repo since the test service has no real
// AgentLoop to set RUNNING for us.
func TestSessionService_Delete_RefusesRunning(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)
	svc := NewSessionService(repo, nil)
	ctx := context.Background()
	created, err := svc.Create(ctx, &gilv1.CreateRequest{WorkingDir: "/x"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, created.Id, "running"))

	_, err = svc.Delete(ctx, &gilv1.DeleteRequest{Id: created.Id})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
