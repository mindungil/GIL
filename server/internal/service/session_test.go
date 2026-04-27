package service

import (
	"context"
	"database/sql"
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
