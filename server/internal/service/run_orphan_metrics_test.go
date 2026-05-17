package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/server/internal/metrics"
)

// P45 — orphan reap + auto-resume metrics ticking.

func TestReapMetrics_DaemonRestartLabel_TicksOnReap(t *testing.T) {
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	// Snapshot the daemon_restart counter before the reap.
	before := testutil.ToFloat64(metrics.OrphanRunsReapedTotal.WithLabelValues("daemon_restart"))

	repo := session.NewRepo(db)
	s, _ := repo.Create(ctx, session.CreateInput{GoalHint: "m1"})
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	count, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	after := testutil.ToFloat64(metrics.OrphanRunsReapedTotal.WithLabelValues("daemon_restart"))
	require.Equal(t, 1.0, after-before,
		"daemon_restart reap must increment the counter by exactly 1; before=%g after=%g", before, after)
}

func TestReapMetrics_StaleHeartbeatLabel_TicksOnSweep(t *testing.T) {
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	before := testutil.ToFloat64(metrics.OrphanRunsReapedTotal.WithLabelValues("stale_heartbeat"))

	repo := session.NewRepo(db)
	s, _ := repo.Create(ctx, session.CreateInput{GoalHint: "m2"})
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionsBase, s.ID), 0o755))
	// No runProgress entry → stale → reaped by sweep.

	count := rs.sweepStaleHeartbeats(ctx)
	require.Equal(t, 1, count)

	after := testutil.ToFloat64(metrics.OrphanRunsReapedTotal.WithLabelValues("stale_heartbeat"))
	require.Equal(t, 1.0, after-before,
		"stale_heartbeat sweep must increment the counter by exactly 1; before=%g after=%g", before, after)
}

func TestAutoResumeMetric_TicksWhenSpecOptsIn(t *testing.T) {
	rs, db, sessionsBase := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	before := testutil.ToFloat64(metrics.AutoResumeKickedTotal)

	repo := session.NewRepo(db)
	s, _ := repo.Create(ctx, session.CreateInput{GoalHint: "auto-resume-metric"})
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	sessDir := filepath.Join(sessionsBase, s.ID)
	require.NoError(t, os.MkdirAll(sessDir, 0o755))
	require.NoError(t, specstore.NewStore(sessDir).Save(&gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "metric"},
		Risk: &gilv1.RiskProfile{ResumeOnRestart: true},
	}))

	_, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)

	after := testutil.ToFloat64(metrics.AutoResumeKickedTotal)
	require.Equal(t, 1.0, after-before,
		"auto-resume opt-in must increment AutoResumeKickedTotal")
}

func TestAutoResumeMetric_DoesNotTickWithoutOptIn(t *testing.T) {
	rs, db, _ := newRunSvcForOrphanTest(t)
	ctx := context.Background()

	before := testutil.ToFloat64(metrics.AutoResumeKickedTotal)

	repo := session.NewRepo(db)
	s, _ := repo.Create(ctx, session.CreateInput{GoalHint: "manual"})
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	// No spec frozen — defaults to manual.

	_, err := rs.ReapOrphanRuns(ctx)
	require.NoError(t, err)

	after := testutil.ToFloat64(metrics.AutoResumeKickedTotal)
	require.Equal(t, 0.0, after-before,
		"manual-default reap must NOT increment AutoResumeKickedTotal")
}
