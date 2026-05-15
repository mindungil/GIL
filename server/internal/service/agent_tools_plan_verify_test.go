package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// agent_tools_plan_verify_test.go covers plan_steps + verify, including
// the system-managed status transitions that gate the discipline:
// agent cannot self-mark a step verified — only verify(stepID) on a
// passing acceptance_check command can.

func TestPlanStore_ReplacePreservesStatus(t *testing.T) {
	store := &planStore{items: make(map[string][]*planStep)}

	store.replace("s", []planStepInput{
		{Description: "build", AcceptanceCheck: "go build ./..."},
		{Description: "test", AcceptanceCheck: "go test ./..."},
	})
	require.NoError(t, store.markVerified("s", 1))
	require.NoError(t, store.markFailed("s", 2, "boom"))

	// Replace with same descs + extra new step. Existing statuses preserved.
	store.replace("s", []planStepInput{
		{Description: "build", AcceptanceCheck: "go build ./..."},
		{Description: "test", AcceptanceCheck: "go test ./..."},
		{Description: "lint", AcceptanceCheck: "golangci-lint run"},
	})
	snap := store.snapshot("s")
	require.Len(t, snap, 3)
	require.Equal(t, "verified", snap[0].Status)
	require.Equal(t, "failed", snap[1].Status)
	require.Equal(t, "boom", snap[1].LastFailure)
	require.Equal(t, "pending", snap[2].Status, "new step starts pending")
}

func TestPlanStore_TransitionRejectsBadID(t *testing.T) {
	store := &planStore{items: make(map[string][]*planStep)}
	store.replace("s", []planStepInput{{Description: "x", AcceptanceCheck: "true"}})
	require.Error(t, store.markVerified("s", 99))
	require.Error(t, store.markVerified("s", 0))
}

func TestToolPlanSteps_RejectsEmptyAcceptance(t *testing.T) {
	tool := &toolPlanSteps{}
	res, _ := tool.run(context.Background(), "sess",
		json.RawMessage(`{"items":[{"description":"x","acceptance_check":""}]}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "acceptance_check")
}

func TestToolPlanSteps_Renders(t *testing.T) {
	defer globalPlanStore.replace("sess-render", nil)
	tool := &toolPlanSteps{}
	res, err := tool.run(context.Background(), "sess-render",
		json.RawMessage(`{"items":[
			{"description":"build","acceptance_check":"go build ./..."},
			{"description":"test","acceptance_check":"go test ./..."}
		]}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "[ ] 1. build")
	require.Contains(t, res.Content, "check: go build ./...")
	require.Contains(t, res.Content, "[ ] 2. test")
}

func TestToolVerify_PassTransitionsStep(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)
	defer globalPlanStore.replace(sid, nil)

	// Plant a plan with a passing acceptance check.
	(&toolPlanSteps{}).run(context.Background(), sid,
		json.RawMessage(`{"items":[{"description":"echo","acceptance_check":"true"}]}`))

	tool := &toolVerify{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"description":"smoke","command":"sh -c 'exit 0'","step_id":1}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "[PASS]")
	require.Contains(t, res.Content, "step 1 → verified")

	snap := globalPlanStore.snapshot(sid)
	require.Equal(t, "verified", snap[0].Status)
}

func TestToolVerify_FailTransitionsStep(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)
	defer globalPlanStore.replace(sid, nil)

	(&toolPlanSteps{}).run(context.Background(), sid,
		json.RawMessage(`{"items":[{"description":"flaky","acceptance_check":"false"}]}`))

	tool := &toolVerify{repo: repo}
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"description":"smoke","command":"echo whoops 1>&2; exit 1","step_id":1}`))
	require.True(t, res.IsError, "verify failure must surface as IsError")
	require.Contains(t, res.Content, "[FAIL]")
	require.Contains(t, res.Content, "step 1 → failed")

	snap := globalPlanStore.snapshot(sid)
	require.Equal(t, "failed", snap[0].Status)
	require.NotEmpty(t, snap[0].LastFailure, "last failure must capture stderr tail")
}

func TestToolVerify_NoStepIDStillRuns(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tool := &toolVerify{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"description":"adhoc","command":"sh -c 'exit 0'"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotContains(t, res.Content, "step ", "no step_id means no transition message")
}

func TestParseTestFailures_Patterns(t *testing.T) {
	out := `running tests
--- FAIL: TestFoo (0.01s)
ok  example/ok  (cached)
FAILED tests/auth_test.py::test_login
  ✕ user can sign up
something else`
	failures := parseTestFailures(out)
	require.Len(t, failures, 3)
	require.Contains(t, failures, "go: TestFoo")
	require.Contains(t, failures, "pytest: tests/auth_test.py::test_login")
	require.Contains(t, failures, "jest: user can sign up")
}

func TestIsWeakVerifyCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantBad bool
	}{
		{"bare cat", "cat foo.go", true},
		{"bare ls", "ls -la", true},
		{"bare echo", "echo hi", true},
		{"bare pwd", "pwd", true},
		{"bare true", "true", true},
		{"bare stat", "stat foo.go", true},
		{"bare head", "head -10 foo.go", true},
		{"bare tail", "tail -20 foo.log", true},
		{"bare file", "file foo.go", true},
		{"leading whitespace", "   cat foo.go", true},
		{"build is fine", "go build ./...", false},
		{"test is fine", "go test ./...", false},
		{"compound — cat then build", "cat foo.go && go build ./...", false},
		{"compound — head then test", "head foo.go && go test", false},
		{"pipe to test runner", "find . -name '*.go' | xargs go vet", false},
		{"explicit assertion script", "./scripts/check.sh", false},
		{"cat alone with redirect", "cat > foo.txt", false}, // it's writing, not just inspecting
		{"empty after trim", "   ", true},                    // already rejected upstream but be conservative
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isWeakVerifyCommand(tc.cmd)
			if got != tc.wantBad {
				t.Fatalf("isWeakVerifyCommand(%q) = %v, want %v", tc.cmd, got, tc.wantBad)
			}
		})
	}
}

func TestToolVerify_WeakCommand_Rejects(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)
	tool := &toolVerify{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"description":"check","command":"cat main.go"}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
	if !strings.Contains(res.Content, "too weak") {
		t.Fatalf("error message missing 'too weak': %s", res.Content)
	}
}

func TestToolVerify_CompoundCommand_OK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, dir)
	tool := &toolVerify{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"description":"build","command":"cat main.go && go build ./..."}`))
	// We don't care whether `go build` succeeds (no go.mod in tempdir).
	// We only care that the schema guard didn't reject before exec.
	if res.IsError && strings.Contains(res.Content, "too weak") {
		t.Fatalf("compound command rejected as weak: %+v", res)
	}
}

func TestPlanStepsThenAgentCannotMarkVerified(t *testing.T) {
	// Reproduces the discipline contract: even if the agent re-sends
	// the plan, the system never lets it set status=verified directly.
	// (plan_steps schema doesn't accept status — this is enforced by
	// the schema itself, but we assert it via a round-trip.)
	defer globalPlanStore.replace("sess-disc", nil)
	tool := &toolPlanSteps{}
	res, _ := tool.run(context.Background(), "sess-disc",
		json.RawMessage(`{"items":[{"description":"x","acceptance_check":"true","status":"verified"}]}`))
	// Schema says additionalProperties=false; json.Unmarshal still
	// succeeds because Go ignores unknown fields. The store sees only
	// {description, acceptance_check} so the step is pending.
	require.False(t, res.IsError)
	snap := globalPlanStore.snapshot("sess-disc")
	require.Len(t, snap, 1)
	require.Equal(t, "pending", snap[0].Status, "agent CANNOT bypass verify by sending status in plan_steps")
}
