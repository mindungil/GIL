package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return db
}

func TestRepo_Create_PersistsRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	s, err := repo.Create(ctx, CreateInput{
		WorkingDir: "/tmp/proj",
		GoalHint:   "build x",
	})
	require.NoError(t, err)
	require.NotEmpty(t, s.ID)
	require.Equal(t, "created", s.Status)
	require.Equal(t, "/tmp/proj", s.WorkingDir)
}

func TestRepo_Get_ReturnsCreated(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	created, err := repo.Create(ctx, CreateInput{WorkingDir: "/tmp", GoalHint: ""})
	require.NoError(t, err)

	got, err := repo.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
}

func TestRepo_Get_MissingReturnsErr(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	_, err := repo.Get(ctx, "nonexistent")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRepo_List_ReturnsAll(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	repo := NewRepo(db)

	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, CreateInput{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	list, err := repo.List(ctx, ListOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, list, 3)
}
