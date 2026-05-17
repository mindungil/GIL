package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// agent_tools_subagent_test.go — S5/S6/S7 gate behavior. The full
// spawn → run → wait → release loop needs a provider factory + real
// runner, which is out of scope here; this exercises argument
// validation, registry interaction, and error paths the LLM will hit
// most often.

func TestToolSpawnAgent_RequiresLabelAndTask(t *testing.T) {
	sess, base := newTestSessionService(t)
	rs := NewRunService(sess.repo, base, nil)
	tool := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: base}

	wd := t.TempDir()
	sid := newTestSession(t, sess.repo, wd)

	res, _ := tool.run(context.Background(), sid, json.RawMessage(`{"task":"x"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "label")

	res, _ = tool.run(context.Background(), sid, json.RawMessage(`{"label":"x"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "task")
}

// iter102a: control chars in args.Label must be stripped before storage.
// A label of pure control bytes survives TrimSpace (ESC is not unicode
// whitespace) but becomes empty after sanitizeHintControlChars; the
// empty-after path is the cleanest observable that the sanitizer ran.
func TestToolSpawnAgent_RejectsControlCharOnlyLabel(t *testing.T) {
	sess, base := newTestSessionService(t)
	rs := NewRunService(sess.repo, base, nil)
	tool := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: base}

	wd := t.TempDir()
	sid := newTestSession(t, sess.repo, wd)

	// Label is only control bytes (ESC + SOH + DEL); sanitizer empties it.
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"label":"\u001b\u0001\u007f","task":"x"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "empty after stripping control chars")
}

func TestToolSpawnAgent_RejectsUnfrozenParent(t *testing.T) {
	sess, base := newTestSessionService(t)
	rs := NewRunService(sess.repo, base, nil)
	tool := &toolSpawnAgent{sess: sess, rs: rs, registry: sess.subagentRegistry, base: base}

	wd := t.TempDir()
	sid := newTestSession(t, sess.repo, wd)

	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"label":"x","task":"do something"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "freeze_spec first")
}

func TestToolWaitAgent_RequiresIDOrLabel(t *testing.T) {
	sess, _ := newTestSessionService(t)
	tool := &toolWaitAgent{sess: sess}
	res, _ := tool.run(context.Background(), "sess-x", json.RawMessage(`{}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "agent_id or label")
}

func TestToolWaitAgent_ResolvesByLabel(t *testing.T) {
	sess, _ := newTestSessionService(t)
	parentID := newTestSession(t, sess.repo, t.TempDir())

	// Create a child manually (bypass spawn_agent for unit-test
	// granularity; the spawn_agent flow needs frozen parent + runner).
	child, err := sess.repo.Create(context.Background(), sessionCreateChild(parentID, "explore-auth"))
	require.NoError(t, err)
	require.NoError(t, sess.repo.UpdateStatus(context.Background(), child.ID, "done"))

	tool := &toolWaitAgent{sess: sess}
	res, _ := tool.run(context.Background(), parentID,
		json.RawMessage(`{"label":"explore-auth","timeout_seconds":2}`))
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "explore-auth")
	require.Contains(t, res.Content, "status=done")
}

func TestToolWaitAgent_UnknownLabel(t *testing.T) {
	sess, _ := newTestSessionService(t)
	parentID := newTestSession(t, sess.repo, t.TempDir())

	tool := &toolWaitAgent{sess: sess}
	res, _ := tool.run(context.Background(), parentID,
		json.RawMessage(`{"label":"nope"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "no child with label")
}

func TestToolAgentStatus_EmptyShowsHint(t *testing.T) {
	sess, _ := newTestSessionService(t)
	parentID := newTestSession(t, sess.repo, t.TempDir())

	tool := &toolAgentStatus{sess: sess}
	res, _ := tool.run(context.Background(), parentID, nil)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no subagents")
}

func TestToolAgentStatus_ListsChildren(t *testing.T) {
	sess, _ := newTestSessionService(t)
	parentID := newTestSession(t, sess.repo, t.TempDir())

	c1, err := sess.repo.Create(context.Background(), sessionCreateChild(parentID, "explore"))
	require.NoError(t, err)
	c2, err := sess.repo.Create(context.Background(), sessionCreateChild(parentID, "build"))
	require.NoError(t, err)
	require.NoError(t, sess.repo.UpdateStatus(context.Background(), c1.ID, "running"))
	require.NoError(t, sess.repo.UpdateStatus(context.Background(), c2.ID, "done"))

	tool := &toolAgentStatus{sess: sess}
	res, _ := tool.run(context.Background(), parentID, nil)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "explore")
	require.Contains(t, res.Content, "build")
	require.Contains(t, res.Content, "running")
	require.Contains(t, res.Content, "done")
}

// sessionCreateChild is a tiny helper to stamp parent linkage on a
// new session for tests that bypass the full spawn_agent flow.
func sessionCreateChild(parentID, label string) session.CreateInput {
	return session.CreateInput{
		WorkingDir:      "/tmp",
		GoalHint:        "test",
		ParentSessionID: parentID,
		SubagentDepth:   1,
		SubagentLabel:   label,
	}
}

// iter133a: session.TotalTokens / TotalCostUSD on the persisted row
// are not maintained by the run path, so reading them returned
// "tokens=0 cost=$0.0000" even after a real run. renderSubagentFinal
// now consults the live trackers; the row values stay as a fallback.
type fakeProgress struct{ tokens int64 }

func (f fakeProgress) Progress(string) (int32, int64, bool) { return 0, f.tokens, true }

type fakeBudget struct{ cost float64 }

func (f fakeBudget) Budget(string) (float64, bool, string, bool) {
	return f.cost, false, "", true
}

func TestRenderSubagentFinal_ReadsLiveTrackers(t *testing.T) {
	s := session.Session{
		ID: "abc", SubagentLabel: "explorer", Status: "done",
		TotalTokens: 0, TotalCostUSD: 0,
	}
	got := renderSubagentFinal(s, fakeProgress{tokens: 1234}, fakeBudget{cost: 0.0123})
	require.Contains(t, got, "tokens=1234")
	require.Contains(t, got, "cost=$0.0123")
}

func TestRenderSubagentFinal_FallsBackToRowWhenTrackersAbsent(t *testing.T) {
	s := session.Session{
		ID: "abc", SubagentLabel: "explorer", Status: "done",
		TotalTokens: 999, TotalCostUSD: 0.5,
	}
	got := renderSubagentFinal(s, nil, nil)
	require.Contains(t, got, "tokens=999")
	require.Contains(t, got, "cost=$0.5000")
}
