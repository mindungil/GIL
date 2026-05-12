package service

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// G3 — verifies plan_steps survives a "daemon restart" by writing
// through a *sql.DB, dropping the in-memory cache, and rehydrating
// from the same DB on the next access.

func openTestPlanDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "plan.db"))
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newStoreWithDB(db *sql.DB) *planStore {
	s := &planStore{items: make(map[string][]*planStep)}
	s.SetDB(db)
	return s
}

func TestPlanStore_ReplacePersistsThroughDB(t *testing.T) {
	db := openTestPlanDB(t)
	s := newStoreWithDB(db)

	s.replace("sess-a", []planStepInput{
		{Description: "build", AcceptanceCheck: "go build ./..."},
		{Description: "test", AcceptanceCheck: "go test ./..."},
	})
	require.NoError(t, s.markVerified("sess-a", 1))
	require.NoError(t, s.markFailed("sess-a", 2, "fail tail"))

	// Simulate daemon restart: fresh store pointing at the same DB.
	revived := newStoreWithDB(db)
	snap := revived.snapshot("sess-a")
	require.Len(t, snap, 2)
	require.Equal(t, "verified", snap[0].Status)
	require.Equal(t, "failed", snap[1].Status)
	require.Equal(t, "fail tail", snap[1].LastFailure)
}

func TestPlanStore_ReplaceClearsRowsOnPlanChange(t *testing.T) {
	db := openTestPlanDB(t)
	s := newStoreWithDB(db)

	s.replace("sess-b", []planStepInput{
		{Description: "old1", AcceptanceCheck: "true"},
		{Description: "old2", AcceptanceCheck: "true"},
	})
	// New plan with a single step.
	s.replace("sess-b", []planStepInput{
		{Description: "new1", AcceptanceCheck: "true"},
	})

	// Daemon restart — confirm DB matches the *new* plan, not the old.
	revived := newStoreWithDB(db)
	snap := revived.snapshot("sess-b")
	require.Len(t, snap, 1)
	require.Equal(t, "new1", snap[0].Description)
}

func TestPlanStore_NoDBStillWorks(t *testing.T) {
	s := &planStore{items: make(map[string][]*planStep)}
	s.replace("sess-c", []planStepInput{
		{Description: "x", AcceptanceCheck: "true"},
	})
	require.NoError(t, s.markVerified("sess-c", 1))
	snap := s.snapshot("sess-c")
	require.Len(t, snap, 1)
	require.Equal(t, "verified", snap[0].Status)
}

func TestPlanStore_StatusPreservedAcrossPlanReplace(t *testing.T) {
	db := openTestPlanDB(t)
	s := newStoreWithDB(db)

	s.replace("sess-d", []planStepInput{
		{Description: "build", AcceptanceCheck: "go build ./..."},
		{Description: "test", AcceptanceCheck: "go test ./..."},
	})
	require.NoError(t, s.markVerified("sess-d", 1))

	// Daemon restart, agent re-issues plan_steps with same descriptions.
	revived := newStoreWithDB(db)
	revived.replace("sess-d", []planStepInput{
		{Description: "build", AcceptanceCheck: "go build ./..."},
		{Description: "test", AcceptanceCheck: "go test ./..."},
		{Description: "lint", AcceptanceCheck: "golangci-lint run"},
	})

	snap := revived.snapshot("sess-d")
	require.Len(t, snap, 3)
	require.Equal(t, "verified", snap[0].Status, "verified status survives restart + plan re-issue")
	require.Equal(t, "pending", snap[2].Status, "new step starts pending")
}
