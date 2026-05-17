package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/specstore"
)

// agent_tools_lifecycle_test.go covers freeze_spec / start_run /
// apply_diff at unit granularity. We don't go through the full
// SessionService.Prompt path — that would require a provider factory,
// runner loop, and a real LLM (or a heavy mock). The contract worth
// pinning is "tool inputs → on-disk spec + session-status transitions",
// which is fully observable at this layer.

func newTestSessionService(t *testing.T) (*SessionService, string) {
	t.Helper()
	repo := newTestRepo(t)
	base := t.TempDir()
	s := NewSessionService(repo, nil).WithSessionsBase(base)
	return s, base
}

func TestToolFreezeSpec_HappyPath(t *testing.T) {
	wd := t.TempDir()
	sess, base := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, wd)

	tool := &toolFreezeSpec{sess: sess, base: base}
	res, err := tool.run(context.Background(), sid, json.RawMessage(`{
		"goal": {
			"one_liner": "add a Hello() function returning 'hi'",
			"success_criteria": ["go test ./... passes"]
		},
		"verification": {
			"checks": [
				{"name": "build", "command": "go build ./..."},
				{"name": "test", "command": "go test ./..."}
			]
		},
		"autonomy": "ask_destructive_only"
	}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "spec frozen")
	require.Contains(t, res.Content, "goal: add a Hello()")

	// spec.yaml + spec.lock land on disk under base/<sid>/.
	store := specstore.NewStore(filepath.Join(base, sid))
	require.True(t, store.IsFrozen())
	fs, lerr := store.Load()
	require.NoError(t, lerr)
	require.NotNil(t, fs.Goal)
	require.Equal(t, "add a Hello() function returning 'hi'", fs.Goal.OneLiner)
	require.Len(t, fs.Verification.Checks, 2)

	// Session status flips to frozen so RunService.Start accepts it.
	got, gerr := sess.repo.Get(context.Background(), sid)
	require.NoError(t, gerr)
	require.Equal(t, "frozen", got.Status)
}

// iter127a: agents repeatedly emit `{"goal": "bare string", "one_liner": "..."}`
// — they misread the schema's nested `required: [one_liner]` as a top-
// level requirement. Accept the bare-string Goal and the top-level
// one_liner as equivalents instead of failing with an opaque
// unmarshal error.
func TestToolFreezeSpec_TolerantArgShapes(t *testing.T) {
	wd := t.TempDir()
	sess, base := newTestSessionService(t)
	tool := &toolFreezeSpec{sess: sess, base: base}
	ctx := context.Background()

	// Variant A: bare-string Goal.
	sid := newTestSession(t, sess.repo, wd)
	res, err := tool.run(ctx, sid, json.RawMessage(`{"goal": "fix main.go"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "goal: fix main.go")

	// Variant B: object Goal empty + top-level one_liner.
	wd2 := t.TempDir()
	sid2 := newTestSession(t, sess.repo, wd2)
	res2, err := tool.run(ctx, sid2, json.RawMessage(`{"goal": {}, "one_liner": "fix it"}`))
	require.NoError(t, err)
	require.False(t, res2.IsError, res2.Content)
	require.Contains(t, res2.Content, "goal: fix it")

	// Variant C: bare-string Goal + top-level one_liner (Goal wins because it's set first).
	wd3 := t.TempDir()
	sid3 := newTestSession(t, sess.repo, wd3)
	res3, err := tool.run(ctx, sid3, json.RawMessage(`{"goal": "use this one", "one_liner": "fallback"}`))
	require.NoError(t, err)
	require.False(t, res3.IsError, res3.Content)
	require.Contains(t, res3.Content, "goal: use this one")
}

func TestToolFreezeSpec_RequiresGoalOneLiner(t *testing.T) {
	wd := t.TempDir()
	sess, base := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, wd)

	tool := &toolFreezeSpec{sess: sess, base: base}
	res, _ := tool.run(context.Background(), sid, json.RawMessage(`{"goal":{}}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "one_liner")

	// Session status untouched on error.
	got, _ := sess.repo.Get(context.Background(), sid)
	require.NotEqual(t, "frozen", got.Status)
}

func TestToolFreezeSpec_AlreadyFrozenRejects(t *testing.T) {
	wd := t.TempDir()
	sess, base := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, wd)

	tool := &toolFreezeSpec{sess: sess, base: base}
	first, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"goal":{"one_liner":"first"}}`))
	require.NoError(t, err)
	require.False(t, first.IsError, first.Content)

	second, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"goal":{"one_liner":"second"}}`))
	require.NoError(t, err)
	require.True(t, second.IsError, "re-freeze must reject")
	require.Contains(t, second.Content, "already frozen")
}

func TestToolFreezeSpec_AutonomyDialParse(t *testing.T) {
	cases := []struct {
		in       string
		wantUnset bool
	}{
		{"plan_only", false},
		{"ask_destructive_only", false},
		{"full", false},
		{"", true},
		{"garbage", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			wd := t.TempDir()
			sess, base := newTestSessionService(t)
			sid := newTestSession(t, sess.repo, wd)
			tool := &toolFreezeSpec{sess: sess, base: base}
			payload := `{"goal":{"one_liner":"x"},"autonomy":"` + tc.in + `"}`
			res, _ := tool.run(context.Background(), sid, json.RawMessage(payload))
			require.False(t, res.IsError, res.Content)

			store := specstore.NewStore(filepath.Join(base, sid))
			fs, _ := store.Load()
			if tc.wantUnset {
				require.Nil(t, fs.Risk, "unset autonomy must not allocate RiskProfile")
			} else {
				require.NotNil(t, fs.Risk)
			}
		})
	}
}

func TestToolStartRun_RejectsUnfrozenSession(t *testing.T) {
	wd := t.TempDir()
	sess, base := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, wd)

	rs := NewRunService(sess.repo, base, nil)
	tool := &toolStartRun{rs: rs}
	res, err := tool.run(context.Background(), sid, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, res.IsError, "must reject when session not frozen")
	require.Contains(t, res.Content, "frozen")
	require.Contains(t, res.Content, "freeze_spec first")
}

func TestToolStartRun_RejectsUnknownSession(t *testing.T) {
	sess, base := newTestSessionService(t)
	rs := NewRunService(sess.repo, base, nil)
	tool := &toolStartRun{rs: rs}
	res, err := tool.run(context.Background(), "nonexistent-id", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "session lookup failed")
}

func TestToolApplyDiff_ChatModeNoEdits(t *testing.T) {
	tool := &toolApplyDiff{rs: nil, tracker: newTurnDiffTracker()}
	res, err := tool.run(context.Background(), "sess-empty", nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no edits to apply")
}

func TestToolApplyDiff_ChatModeReportsTrackerEdits(t *testing.T) {
	tracker := newTurnDiffTracker()
	tracker.recordPostWrite("sess-x", "main.go", "package main\n", true)

	tool := &toolApplyDiff{rs: nil, tracker: tracker}
	res, err := tool.run(context.Background(), "sess-x", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "already applied")
	require.Contains(t, res.Content, "main.go")
}
