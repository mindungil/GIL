package session

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMigrate_FreshDB_CreatesTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, Migrate(db))

	row := db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1")
	var v int
	require.NoError(t, row.Scan(&v))
	require.Equal(t, 1, v)
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, Migrate(db))
	require.NoError(t, Migrate(db)) // 재호출 OK
}
