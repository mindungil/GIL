package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/permission"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// makeAskingSpec builds a frozen spec that gates bash via
// ASK_DESTRUCTIVE_ONLY autonomy. The destructive bash patterns
// (rm *, mv *, sudo *, ...) trigger DecisionDeny — but a non-matching
// bash like `git status` falls through to "allow". So we deliberately
// use ASK_PER_ACTION for the test: every tool that isn't on the
// read-only allow list (read_file, memory_load, repomap, compact_now)
// falls through to DecisionAsk, which is what we want to exercise.
func makeAskingSpec(t *testing.T, sessionsBase, sessionID, workingDir string) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-spec-perm",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner:               "list files",
			SuccessCriteriaNatural: []string{"command ran"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"bash"}},
		Verification: &gilv1.Verification{
			// No-op verification so success only depends on the bash run.
			Checks: []*gilv1.Check{
				{Name: "noop", Kind: gilv1.CheckKind_SHELL, Command: "true"},
			},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workingDir},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "mock", ModelId: "mock-model"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_ASK_PER_ACTION},
		Budget:    &gilv1.Budget{MaxIterations: 5},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

// TestRunService_AnswerPermission_AlwaysAllow_PersistsAndAutoAllowsNextRun
// is the full-loop integration test for the persistence path: a run
// with ASK_PER_ACTION asks the user about a `bash ls`, the user picks
// PERMISSION_DECISION_ALLOW_ALWAYS, which (a) unblocks the current
// run AND (b) writes the rule into the on-disk PersistentStore so the
// next run with the same workspace + same command does not ask again.
//
// This is the "I approved this once and never want to see it again"
// flow promised by the TUI modal.
func TestRunService_AnswerPermission_AlwaysAllow_PersistsAndAutoAllowsNextRun(t *testing.T) {
	workDir := t.TempDir()

	// Isolate XDG so the persistent store lives under a per-test dir.
	gilHome := t.TempDir()
	t.Setenv("GIL_HOME", gilHome)

	// Mock turn: agent calls bash with a known command, then ends.
	bashCmd := `ls /tmp`
	mockTurns := []provider.MockTurn{
		{Text: "running ls", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"` + bashCmd + `"}`)},
		}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeAskingSpec(t, sessionsBase, s.ID, workDir)

	// Run Start in a goroutine so we can intercept the permission_ask.
	type runRes struct {
		resp *gilv1.StartRunResponse
		err  error
	}
	resCh := make(chan runRes, 1)
	go func() {
		resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
		resCh <- runRes{resp, err}
	}()

	// Poll pendingAsks until the AskCallback registers our request.
	var (
		reqID string
		ask   *pendingAsk
	)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		for r, a := range svc.pendingAsks[s.ID] {
			reqID = r
			ask = a
		}
		svc.mu.Unlock()
		if reqID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, reqID, "AskCallback should have registered a pending request")
	require.Equal(t, "bash", ask.tool)
	require.Equal(t, bashCmd, ask.key)

	// User picks ALWAYS_ALLOW.
	answerResp, err := svc.AnswerPermission(ctx, &gilv1.AnswerPermissionRequest{
		SessionId: s.ID,
		RequestId: reqID,
		Decision:  gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ALWAYS,
	})
	require.NoError(t, err)
	require.True(t, answerResp.Delivered)

	// Run completes successfully.
	select {
	case r := <-resCh:
		require.NoError(t, r.err)
		require.Equal(t, "done", r.resp.Status)
	case <-time.After(15 * time.Second):
		t.Fatal("run did not complete after AnswerPermission")
	}

	// On-disk persistent store should now have the rule under the
	// project's absolute path.
	storePath := filepath.Join(gilHome, "state", "permissions.toml")
	store := &permission.PersistentStore{Path: storePath}
	abs, err := filepath.Abs(workDir)
	require.NoError(t, err)
	rules, err := store.Load(abs)
	require.NoError(t, err)
	require.NotNil(t, rules, "ALLOW_ALWAYS should have written a rule")
	require.Contains(t, rules.AlwaysAllow, bashCmd)

	// Second run: same session would be re-frozen in real life; for a
	// pure unit-test we instead build a fresh EvaluatorWithStore with
	// ASK_PER_ACTION and verify the persistent layer short-circuits the
	// Ask before the runner ever calls AskCallback.
	specEval := permission.FromAutonomy(gilv1.AutonomyDial_ASK_PER_ACTION)
	ev := &permission.EvaluatorWithStore{
		Inner:       specEval,
		Store:       store,
		ProjectPath: abs,
	}
	require.Equal(t, permission.DecisionAllow, ev.Evaluate("bash", bashCmd),
		"persistent always_allow should auto-allow on the next run without prompting")
}

// TestRunService_AnswerPermission_LegacyAllowField_StillWorks pins the
// backwards-compatibility contract: clients that only send req.Allow
// (and leave req.Decision UNSPECIFIED) keep getting the once-tier
// behaviour they always had. The phase07 e2e + the existing CLI rely
// on this.
func TestRunService_AnswerPermission_LegacyAllowField_StillWorks(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	sessID := "legacy-session"
	reqID := "legacy-req"

	ch := make(chan bool, 1)
	svc.mu.Lock()
	svc.pendingAsks[sessID] = map[string]*pendingAsk{reqID: {ch: ch, tool: "bash", key: "echo hi"}}
	svc.mu.Unlock()

	// No Decision set → server falls back to req.Allow.
	resp, err := svc.AnswerPermission(context.Background(), &gilv1.AnswerPermissionRequest{
		SessionId: sessID,
		RequestId: reqID,
		Allow:     true,
	})
	require.NoError(t, err)
	require.True(t, resp.Delivered)

	select {
	case allow := <-ch:
		require.True(t, allow, "legacy allow=true should unblock with allow=true")
	case <-time.After(time.Second):
		t.Fatal("answer never reached the channel")
	}

	// Setup a tempdir GIL_HOME so we can verify NO persistent rule
	// was written (legacy path is once-tier).
	gilHome := t.TempDir()
	t.Setenv("GIL_HOME", gilHome)
	storePath := filepath.Join(gilHome, "state", "permissions.toml")
	// File should not exist (no persistence happened).
	store := &permission.PersistentStore{Path: storePath}
	rules, err := store.Load("/no/such/project")
	require.NoError(t, err)
	require.Nil(t, rules)
}

// TestResolveDecision pins the wire-to-internal mapping so future enum
// additions show up as test failures rather than silently undefined.
func TestResolveDecision(t *testing.T) {
	cases := []struct {
		name      string
		dec       gilv1.PermissionDecision
		allow     bool
		wantAllow bool
		wantPD    permission.PersistDecision
	}{
		{"allow_once", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ONCE, false, true, permission.PersistAllowOnce},
		{"allow_session", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_SESSION, false, true, permission.PersistAllowSession},
		{"allow_always", gilv1.PermissionDecision_PERMISSION_DECISION_ALLOW_ALWAYS, false, true, permission.PersistAllowAlways},
		{"deny_once", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ONCE, true, false, permission.PersistDenyOnce},
		{"deny_session", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_SESSION, true, false, permission.PersistDenySession},
		{"deny_always", gilv1.PermissionDecision_PERMISSION_DECISION_DENY_ALWAYS, true, false, permission.PersistDenyAlways},
		{"unspecified_allow", gilv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED, true, true, permission.PersistAllowOnce},
		{"unspecified_deny", gilv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED, false, false, permission.PersistDenyOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAllow, gotPD := resolveDecision(tc.dec, tc.allow)
			require.Equal(t, tc.wantAllow, gotAllow)
			require.Equal(t, tc.wantPD, gotPD)
		})
	}
}
