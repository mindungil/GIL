package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/specstore"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func newRunSvc(t *testing.T, mockTurns []provider.MockTurn) (*RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(mockTurns), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	return NewRunService(repo, sessionsBase, factory), repo, sessionsBase
}

func makeFrozenSpec(t *testing.T, sessionsBase, sessionID, workingDir string) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-spec",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner:               "create hello.txt",
			SuccessCriteriaNatural: []string{"file exists", "contains hello", "no other files"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"bash"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello.txt"},
			},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workingDir},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "mock", ModelId: "mock-model"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
		Budget:    &gilv1.Budget{MaxIterations: 5},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

func TestRunService_Start_HelloTxt_Done(t *testing.T) {
	workDir := t.TempDir()

	mockTurns := []provider.MockTurn{
		{Text: "Creating", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hello"}`)},
		}, StopReason: "tool_use"},
		{Text: "Done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.NoError(t, err)
	require.Equal(t, "done", resp.Status)
	require.Equal(t, int32(2), resp.Iterations)
	require.Len(t, resp.VerifyResults, 1)
	require.True(t, resp.VerifyResults[0].Passed)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "done", got.Status)
}

func TestRunService_Start_NotFrozen_FailsPrecondition(t *testing.T) {
	svc, repo, _ := newRunSvc(t, nil)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: t.TempDir()})
	require.NoError(t, err)

	_, err = svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "frozen")
}

func TestRunService_Start_NotFound(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	_, err := svc.Start(context.Background(), &gilv1.StartRunRequest{SessionId: "nope", Provider: "mock"})
	require.Error(t, err)
}
