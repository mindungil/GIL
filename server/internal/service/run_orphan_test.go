package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// P36 — orphan run reaping. After daemon restart, any session left in
// status="running" had its agent loop goroutine killed. Reaping flips
// the DB row to "stopped" and appends a run_orphaned event so the
// surface and audit trail both reflect reality.

func newRunSvcForOrphanTest(t *testing.T) (*RunService, *sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "orphan.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	sessionsBase := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(sessionsBase, 0o755))
	repo := session.NewRepo(db)
	rs := NewRunService(repo, sessionsBase, nil)
	t.Cleanup(func() { _ = db.Close() })
	return rs, db, sessionsBase
}

func TestReapOrphanRuns_EmptyDB_NoOp(t *testing.T) {
	rs, _, _ := newRunSvcForOrphanTest(t)
	count, err := rs.ReapOrphanRuns(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestReapOrphanRuns_FlipsRunningToStopped(t *testing.T) {
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	// Create one running, one created, one done.
	repo := session.NewRepo(db)
	s1, err := repo.Create(ctx, session.CreateInput{GoalHint: "running-task"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s1.ID, "running"))

	s2, err := repo.Create(ctx, session.CreateInput{GoalHint: "created-task"})
	require.NoError(t, err)
	// Stay in default "created" status.

	s3, err := repo.Create(ctx, session.CreateInput{GoalHint: "done-task"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s3.ID, "done"))

	// Reap should flip s1 and leave s2/s3 untouched.
	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	got1, _ := repo.Get(ctx, s1.ID)
	require.Equal(t, "stopped", got1.Status, "s1 was running → must reap to stopped")

	got2, _ := repo.Get(ctx, s2.ID)
	require.Equal(t, "created", got2.Status, "s2 was created — must not be touched")

	got3, _ := repo.Get(ctx, s3.ID)
	require.Equal(t, "done", got3.Status, "s3 was done — must not be touched")
}

func TestReapOrphanRuns_AppendsOrphanedEvent(t *testing.T) {
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "test"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	// Pre-create the session dir so the persister can write to it.
	// (NewPersister creates the dir itself, but for the test we want
	// to control where it lands.)
	sessDir := filepath.Join(sessionsBase, s.ID)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Read events.jsonl and find the run_orphaned event.
	eventsPath := filepath.Join(sessDir, "events", "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	require.NoError(t, err, "events.jsonl should exist after reap")
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 1, "at least one event line")

	// Parse the last line — it should be the run_orphaned record.
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	require.Equal(t, "run_orphaned", got["type"])

	dataField, ok := got["data"].(string)
	require.True(t, ok, "data field should be a string in the JSONL row")
	require.Contains(t, dataField, "daemon_restart")
	require.Contains(t, dataField, "prior_status")
}

func TestReapOrphanRuns_MultipleOrphans(t *testing.T) {
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	const N = 5
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		s, err := repo.Create(ctx, session.CreateInput{GoalHint: "task"})
		require.NoError(t, err)
		require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
		ids[i] = s.ID
	}

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, N, count)

	// All N must be stopped now.
	for _, id := range ids {
		got, _ := repo.Get(ctx, id)
		require.Equal(t, "stopped", got.Status)
	}

	// Second reap is a no-op — no rows match status="running" anymore.
	count2, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count2, "second reap finds zero — reaping is idempotent")
}

func TestReapOrphanRuns_NilRepo_NoOp(t *testing.T) {
	rs := &RunService{} // bare struct, no repo
	count, err := rs.ReapOrphanRuns(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// P37 — auto_resume flag in the orphan event differentiates spec-opted
// sessions from default-stopped ones. The actual run-restart goroutine
// fire is hard to assert here (no provider factory wired), but the
// event payload is the deterministic record.

func TestReapOrphanRuns_AutoResumeFlagFlowsToEvent(t *testing.T) {
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "task-with-resume"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	// Persist a frozen spec with Risk.ResumeOnRestart=true.
	sessDir := filepath.Join(sessionsBase, s.ID)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))
	store := specstore.NewStore(sessDir)
	spec := &gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "test"},
		Risk: &gilv1.RiskProfile{
			Autonomy:        gilv1.AutonomyDial_FULL,
			ResumeOnRestart: true,
		},
	}
	require.NoError(t, store.Save(spec))

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// The event payload should carry auto_resume:true.
	eventsPath := filepath.Join(sessDir, "events", "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	require.Equal(t, "run_orphaned", got["type"])
	dataField, ok := got["data"].(string)
	require.True(t, ok)
	require.Contains(t, dataField, `"auto_resume":true`,
		"spec opted in via Risk.ResumeOnRestart — event must surface it")
}

func TestReapOrphanRuns_NoSpec_DefaultsToManualResume(t *testing.T) {
	// Session has no frozen spec — Reap proceeds with auto_resume:false.
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "task-no-spec"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsBase, s.ID), 0o755))

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	eventsPath := filepath.Join(sessionsBase, s.ID, "events", "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	dataField := got["data"].(string)
	require.Contains(t, dataField, `"auto_resume":false`,
		"no spec → default false; user must re-trigger manually")
}

func TestReapOrphanRuns_SpecResumeFalse_DefaultsToManual(t *testing.T) {
	// Spec frozen but Risk.ResumeOnRestart=false explicitly — auto_resume:false.
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "task-explicit-no-resume"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	sessDir := filepath.Join(sessionsBase, s.ID)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))
	require.NoError(t, specstore.NewStore(sessDir).Save(&gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "test"},
		Risk: &gilv1.RiskProfile{
			Autonomy:        gilv1.AutonomyDial_FULL,
			ResumeOnRestart: false, // explicit
		},
	}))

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	eventsPath := filepath.Join(sessDir, "events", "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	dataField := got["data"].(string)
	require.Contains(t, dataField, `"auto_resume":false`)
}
