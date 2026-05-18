package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"
)

func TestMigrateV4_CreatesWorkingsetEntries(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ws.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, Migrate(db))

	// INSERT round-trip proves table exists with expected columns.
	_, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "main.go")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "util.go")
	require.NoError(t, err)

	// PK enforces dedupe — second insert of same (sid, path) is an error.
	_, err = db.Exec(`INSERT INTO workingset_entries (session_id, path)
        VALUES (?, ?)`, "s1", "main.go")
	require.Error(t, err, "primary key (session_id, path) should reject duplicate")

	// Index is queryable.
	rows, err := db.Query(`SELECT path FROM workingset_entries
        WHERE session_id = ? ORDER BY path ASC`, "s1")
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		got = append(got, p)
	}
	require.Equal(t, []string{"main.go", "util.go"}, got)
}

func TestRepoDB_ReturnsHandle(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "r.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, Migrate(db))

	repo := NewRepo(db)
	require.Same(t, db, repo.DB(), "Repo.DB returns the wrapped *sql.DB")
}
