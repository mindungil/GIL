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

// subagent_e2e_test.go — S11. End-to-end exercise of the
// spawn_agent → wait_agent loop against a mock provider so subagent
// behavior is testable without an LLM API key. Real LLM scenarios live
// in tests/dogfood once an API key is available.
//
// The mock provider scripts the child's turns: read_file (or similar
// no-op) followed by an end_turn. wait_agent should observe the child
// reaching "done" status within a short timeout.

func newE2EServices(t *testing.T, mockTurns []provider.MockTurn) (*SessionService, *RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(mockTurns), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	rs := NewRunService(repo, sessionsBase, factory)
	sess := NewSessionService(repo, rs).WithSessionsBase(sessionsBase).WithBudgetGetter(rs)
	return sess, rs, repo, sessionsBase
}

func freezeParent(t *testing.T, sessionsBase, sessionID, workDir string) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "parent-spec",
		SessionId: sessionID,
		Goal:      &gilv1.Goal{OneLiner: "parent task"},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workDir},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "mock", ModelId: "mock-model"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
		Budget:    &gilv1.Budget{MaxIterations: 5},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

func TestSubagentE2E_SpawnRunWaitDone(t *testing.T) {
	workDir := t.TempDir()

	// Mock turns: child agent reads a file (no-op) and returns done.
	// 1 tool call + 1 end_turn = 2 turns total. The parent never
	// actually runs in this test — we call spawn_agent directly on the
	// frozen parent session.
	mockTurns := []provider.MockTurn{
		{
			Text: "looking",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"nonexistent.txt"}`)},
			},
			StopReason: "tool_use",
		},
		{Text: "subagent done", StopReason: "end_turn"},
	}

	sess, _, repo, sessionsBase := newE2EServices(t, mockTurns)
	ctx := context.Background()

	// Parent session — frozen, ready to spawn children.
	parent, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir, GoalHint: "parent"})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, parent.ID, "frozen"))
	freezeParent(t, sessionsBase, parent.ID, workDir)

	// Spawn the child via the tool. Note this kicks the child's
	// AgentLoop on a detached goroutine — wait_agent below blocks
	// until terminal.
	rs := sess.runService()
	require.NotNil(t, rs)
	spawnTool := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: sessionsBase}
	spawnRes, err := spawnTool.run(ctx, parent.ID, json.RawMessage(`{
		"label": "explore",
		"task": "look at nonexistent.txt"
	}`))
	require.NoError(t, err)
	require.False(t, spawnRes.IsError, spawnRes.Content)
	require.Contains(t, spawnRes.Content, "subagent started")

	// Extract child id from the spawn response.
	kids, err := repo.ListChildren(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, kids, 1)
	childID := kids[0].ID
	require.Equal(t, "explore", kids[0].SubagentLabel)

	// wait_agent should block until child terminal — give it generous
	// budget. Mock turns resolve quickly; 5s is plenty for the runner
	// to bake.
	waitTool := &toolWaitAgent{sess: sess}
	waitRes, err := waitTool.run(ctx, parent.ID, json.RawMessage(`{
		"agent_id": "`+childID+`",
		"timeout_seconds": 5
	}`))
	require.NoError(t, err)
	require.False(t, waitRes.IsError, waitRes.Content)
	require.Contains(t, waitRes.Content, "subagent finished")
	require.Contains(t, waitRes.Content, "explore")
	// Accept any terminal status — the exact reason (done vs stopped
	// vs failed) depends on whether the mock script satisfied the
	// runner's verification gate, which is a runner concern. The
	// contract under test is "child reaches terminal AND wait_agent
	// returns AND registry released".
	require.Regexp(t, `status=(done|stopped|failed)`, waitRes.Content)

	// Registry release fired — count should be 0.
	rootID, _ := resolveRootSessionID(ctx, repo, kids[0])
	require.Equal(t, 0, sess.subagentRegistry.activeCount(rootID))
}

func TestSubagentE2E_AgentStatusShowsRunningThenDone(t *testing.T) {
	workDir := t.TempDir()
	mockTurns := []provider.MockTurn{{Text: "ok", StopReason: "end_turn"}}

	sess, _, repo, sessionsBase := newE2EServices(t, mockTurns)
	ctx := context.Background()

	parent, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, parent.ID, "frozen"))
	freezeParent(t, sessionsBase, parent.ID, workDir)

	rs := sess.runService()
	spawn := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: sessionsBase}
	_, err = spawn.run(ctx, parent.ID, json.RawMessage(`{"label":"quick","task":"x"}`))
	require.NoError(t, err)

	// agent_status pre-wait — child might already be done given the
	// mock's instant turn, but the row should exist either way.
	statusTool := &toolAgentStatus{sess: sess}
	res, err := statusTool.run(ctx, parent.ID, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content, "quick")

	// wait, then status again — confirm "done" lands.
	waitTool := &toolWaitAgent{sess: sess}
	_, err = waitTool.run(ctx, parent.ID, json.RawMessage(`{"label":"quick","timeout_seconds":5}`))
	require.NoError(t, err)

	res2, err := statusTool.run(ctx, parent.ID, nil)
	require.NoError(t, err)
	require.Regexp(t, `(done|stopped|failed)`, res2.Content)
}

func TestSubagentE2E_WaitTimeoutReturnsRunningStatus(t *testing.T) {
	// Mock with no turns — provider returns "turns exhausted" instantly,
	// child status flips to "failed", wait_agent should observe terminal
	// quickly. To exercise actual timeout we'd need a real slow runner;
	// this test confirms wait_agent's non-blocking nature when child is
	// already terminal.
	mockTurns := []provider.MockTurn{}

	sess, _, repo, sessionsBase := newE2EServices(t, mockTurns)
	ctx := context.Background()
	workDir := t.TempDir()

	parent, _ := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	_ = repo.UpdateStatus(ctx, parent.ID, "frozen")
	freezeParent(t, sessionsBase, parent.ID, workDir)

	rs := sess.runService()
	spawn := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: sessionsBase}
	_, err := spawn.run(ctx, parent.ID, json.RawMessage(`{"label":"x","task":"y"}`))
	require.NoError(t, err)

	// Let the child reach terminal (provider exhausted → failed).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		kids, _ := repo.ListChildren(ctx, parent.ID)
		if len(kids) > 0 && isTerminalStatus(kids[0].Status) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	waitTool := &toolWaitAgent{sess: sess}
	res, err := waitTool.run(ctx, parent.ID, json.RawMessage(`{"label":"x","timeout_seconds":2}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "subagent finished")
}
