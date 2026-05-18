package service

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// P30 — verifies workingset survives a "daemon restart" by writing
// through a *sql.DB, dropping the in-memory cache, and rehydrating
// from the same DB on the next access. Mirrors
// agent_tools_plan_verify_persist_test.go.

func openTestWorkingSetDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ws.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newWorkingSetWithDB(db *sql.DB) *workingSet {
	s := newWorkingSet()
	s.SetDB(db)
	return s
}

func TestWorkingSet_AddPersistsThroughDB(t *testing.T) {
	db := openTestWorkingSetDB(t)
	s := newWorkingSetWithDB(db)

	added, dup := s.add("sess-a", []string{"main.go", "util.go"})
	require.ElementsMatch(t, []string{"main.go", "util.go"}, added)
	require.Empty(t, dup)

	// Simulate daemon restart: fresh store pointing at the same DB.
	revived := newWorkingSetWithDB(db)
	require.Equal(t, []string{"main.go", "util.go"}, revived.list("sess-a"))
}

func TestWorkingSet_DropPersistsThroughDB(t *testing.T) {
	db := openTestWorkingSetDB(t)
	s := newWorkingSetWithDB(db)

	s.add("sess-b", []string{"a.go", "b.go", "c.go"})
	dropped, missing := s.drop("sess-b", []string{"b.go"})
	require.Equal(t, []string{"b.go"}, dropped)
	require.Empty(t, missing)

	revived := newWorkingSetWithDB(db)
	require.Equal(t, []string{"a.go", "c.go"}, revived.list("sess-b"))
}

func TestWorkingSet_AddIsIdempotentAcrossRestart(t *testing.T) {
	db := openTestWorkingSetDB(t)
	s := newWorkingSetWithDB(db)

	s.add("sess-c", []string{"main.go"})
	revived := newWorkingSetWithDB(db)

	// Re-adding existing path on revived store reports it as duplicate,
	// not added — hydration populated the bag from DB.
	added, dup := revived.add("sess-c", []string{"main.go", "extra.go"})
	require.Equal(t, []string{"extra.go"}, added)
	require.Equal(t, []string{"main.go"}, dup)
}

func TestWorkingSet_NoDBStillWorks(t *testing.T) {
	s := newWorkingSet()
	added, _ := s.add("sess-d", []string{"x.go"})
	require.Equal(t, []string{"x.go"}, added)
	require.Equal(t, []string{"x.go"}, s.list("sess-d"))
}

func TestWorkingSet_PerSessionIsolationDB(t *testing.T) {
	db := openTestWorkingSetDB(t)
	s := newWorkingSetWithDB(db)

	s.add("sess-e", []string{"e.go"})
	s.add("sess-f", []string{"f.go"})

	revived := newWorkingSetWithDB(db)
	require.Equal(t, []string{"e.go"}, revived.list("sess-e"))
	require.Equal(t, []string{"f.go"}, revived.list("sess-f"))
}
