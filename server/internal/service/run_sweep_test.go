package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// P38 — mid-session orphan sweeper. Runs while daemon is up; reaps
// sessions whose runProgress entry is missing OR whose lastHeartbeat
// is older than staleHeartbeatThreshold. Tests use the synchronous
// sweepStaleHeartbeats helper directly so we don't need to wait for
// the ticker.

func TestSweepStaleHeartbeats_NoOrphans_NoOp(t *testing.T) {
	rs, _, _ := newRunSvcForOrphanTest(t)
	count := rs.sweepStaleHeartbeats(context.Background())
	require.Equal(t, 0, count)
}

func TestSweepStaleHeartbeats_MissingProgressEntry_Reaps(t *testing.T) {
	// Session in DB status=running, but no runProgress entry exists
	// (this is what mid-session goroutine death looks like — the
	// snap was deleted by the defer that ran on panic recovery).
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "hung-task"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsBase, s.ID), 0o755))

	// Sweep should find this orphan and reap it.
	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, 1, count)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "stopped", got.Status, "missing runProgress → reaped")

	// Audit row uses reason=stale_heartbeat so consumers can tell it
	// from a P36 daemon-restart reap.
	eventsPath := filepath.Join(sessionsBase, s.ID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	require.Equal(t, "run_orphaned", rec["type"])
	dataField := rec["data"].(string)
	require.Contains(t, dataField, `"reason":"stale_heartbeat"`)
}

func TestSweepStaleHeartbeats_FreshHeartbeat_NotReaped(t *testing.T) {
	// runProgress exists with a fresh lastHeartbeat — sweep should
	// leave it alone.
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "live-task"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	// Seed a runProgress entry with heartbeat = now.
	rs.mu.Lock()
	rs.runProgress[s.ID] = &runProgressSnap{lastHeartbeat: time.Now()}
	rs.mu.Unlock()

	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, 0, count)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "running", got.Status, "fresh heartbeat → still running")
}

func TestSweepStaleHeartbeats_StaleHeartbeat_Reaps(t *testing.T) {
	// runProgress exists but lastHeartbeat is older than threshold —
	// the goroutine is alive in some sense (snap not deleted) but not
	// progressing. Treat as orphan and reap.
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	s, err := repo.Create(ctx, session.CreateInput{GoalHint: "hung-task"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsBase, s.ID), 0o755))

	rs.mu.Lock()
	rs.runProgress[s.ID] = &runProgressSnap{
		lastHeartbeat: time.Now().Add(-2 * staleHeartbeatThreshold), // very stale
	}
	rs.mu.Unlock()

	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, 1, count)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "stopped", got.Status)

	// In-memory runProgress entry should be cleared so a future Start
	// on the same session id rebuilds it.
	rs.mu.Lock()
	_, stillExists := rs.runProgress[s.ID]
	rs.mu.Unlock()
	require.False(t, stillExists, "sweeper must clear runProgress so re-Start is clean")
}

func TestSweepStaleHeartbeats_IgnoresNonRunningSessions(t *testing.T) {
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	// Create one in each non-running state. None should be touched.
	for _, status := range []string{"created", "frozen", "stopped", "done"} {
		s, err := repo.Create(ctx, session.CreateInput{GoalHint: "status-" + status})
		require.NoError(t, err)
		require.NoError(t, repo.UpdateStatus(ctx, s.ID, status))
	}

	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, 0, count, "sweep filters on status='running' only")
}

func TestSweepStaleHeartbeats_NilRepo_NoOp(t *testing.T) {
	rs := &RunService{}
	count := rs.sweepStaleHeartbeats(context.Background())
	require.Equal(t, 0, count)
}

func TestSweepStaleHeartbeats_Multiple_AllReaped(t *testing.T) {
	// Five orphans (no runProgress), one fresh — sweep reaps 5, leaves 1.
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	repo := session.NewRepo(db)
	const N = 5
	orphanIDs := make([]string, N)
	for i := 0; i < N; i++ {
		s, err := repo.Create(ctx, session.CreateInput{GoalHint: "stale"})
		require.NoError(t, err)
		require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
		require.NoError(t, os.MkdirAll(filepath.Join(sessionsBase, s.ID), 0o755))
		orphanIDs[i] = s.ID
	}
	// One fresh session with live heartbeat.
	live, err := repo.Create(ctx, session.CreateInput{GoalHint: "live"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, live.ID, "running"))
	rs.mu.Lock()
	rs.runProgress[live.ID] = &runProgressSnap{lastHeartbeat: time.Now()}
	rs.mu.Unlock()

	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, N, count)

	for _, id := range orphanIDs {
		got, _ := repo.Get(ctx, id)
		require.Equal(t, "stopped", got.Status)
	}
	gotLive, _ := repo.Get(ctx, live.ID)
	require.Equal(t, "running", gotLive.Status)
}
