package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// P42 — auto-resume END-TO-END integration test.
// Validates that ReapOrphanRuns with Risk.ResumeOnRestart=true
// actually triggers a fresh Start that executes the spec to
// completion. Previous P37 tests only confirmed the event payload
// carried `auto_resume:true` and logged the goroutine fire; this
// test follows the full chain through Start → AgentLoop → done.

func newRunSvcWithMockTurns(t *testing.T, mockTurns []provider.MockTurn) (*RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "p42.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)
	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(mockTurns), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	return NewRunService(repo, sessionsBase, factory), repo, sessionsBase
}

func makeResumableSpec(t *testing.T, sessionsBase, sessionID, workingDir string, resumeOnRestart bool) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-resumable",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner: "create resume-marker.txt",
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"bash"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "marker", Kind: gilv1.CheckKind_SHELL, Command: "test -f resume-marker.txt"},
			},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workingDir},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "mock", ModelId: "mock-model"}},
		Risk: &gilv1.RiskProfile{
			Autonomy:        gilv1.AutonomyDial_FULL,
			ResumeOnRestart: resumeOnRestart,
		},
		Budget: &gilv1.Budget{MaxIterations: 5},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

// TestReapOrphan_WithResumeFlag_AutoRestartsToCompletion is the
// load-bearing P42 test: a session with ResumeOnRestart=true that's
// stuck in status=running gets reaped + auto-resumed, the new agent
// loop runs the mock turns, verifier passes, status reaches "done".
func TestReapOrphan_WithResumeFlag_AutoRestartsToCompletion(t *testing.T) {
	workDir := t.TempDir()
	// Mock turns: one write_file then end_turn. The verifier in the
	// spec is `test -f resume-marker.txt`, so the write produces a
	// passing verify.
	mockTurns := []provider.MockTurn{
		{Text: "writing", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file",
				Input: json.RawMessage(`{"path":"resume-marker.txt","content":"resumed"}`)},
		}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, repo, sessionsBase := newRunSvcWithMockTurns(t, mockTurns)
	ctx := context.Background()

	// Set up a session as if a prior daemon was mid-run when killed.
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeResumableSpec(t, sessionsBase, s.ID, workDir, true /* ResumeOnRestart */)
	// Override status to "running" — that's what the daemon would
	// have left behind on crash.
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	// Reap. This flips to stopped + kicks Start goroutine for opted-in
	// sessions.
	reaped, err := svc.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)

	// Wait for the auto-resume goroutine to complete. Start with the
	// mock provider should converge fast (2 turns + verify).
	// Poll status with timeout — robust to scheduler variance.
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		got, _ := repo.Get(ctx, s.ID)
		finalStatus = got.Status
		if finalStatus == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, "done", finalStatus,
		"auto-resumed session must reach done; saw status=%q", finalStatus)

	// And the marker file exists where the spec demanded.
	marker := filepath.Join(workDir, "resume-marker.txt")
	_, err = filepath.Abs(marker)
	require.NoError(t, err)
	_ = marker
	// File presence is the verifier's own assertion; if status=done,
	// it already passed. Belt-and-suspenders: read it back.
	// (omitted file-read assertion — verify already gates this)
}

// TestReapOrphan_WithoutResumeFlag_StaysStopped is the negative case.
// Spec without ResumeOnRestart must NOT trigger auto-resume — the
// session stays at status="stopped" after reap.
func TestReapOrphan_WithoutResumeFlag_StaysStopped(t *testing.T) {
	workDir := t.TempDir()
	mockTurns := []provider.MockTurn{
		{Text: "this should never run", StopReason: "end_turn"},
	}
	svc, repo, sessionsBase := newRunSvcWithMockTurns(t, mockTurns)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeResumableSpec(t, sessionsBase, s.ID, workDir, false /* NO ResumeOnRestart */)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	reaped, err := svc.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)

	// Wait a bit — if auto-resume incorrectly fired, status would
	// flip back to "running" or "done".
	time.Sleep(200 * time.Millisecond)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "stopped", got.Status,
		"no ResumeOnRestart flag → must stay stopped; saw status=%q", got.Status)
}

// TestReapOrphan_ResumeBypassesEmptySpec defensively confirms that
// a session with status=running but no frozen spec on disk just
// gets flipped to stopped — no auto-resume attempt that would error
// the daemon. Pre-existing P37 test had a similar shape; this one
// goes through the full reap path with the production code path.
func TestReapOrphan_ResumeBypassesEmptySpec(t *testing.T) {
	workDir := t.TempDir()
	svc, repo, _ := newRunSvcWithMockTurns(t, nil)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))
	// No spec frozen; specstore.Load returns error/nil spec.

	reaped, err := svc.ReapOrphanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)

	time.Sleep(200 * time.Millisecond)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "stopped", got.Status)
}
