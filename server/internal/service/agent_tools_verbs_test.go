package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
)

// agent_tools_verbs_test.go covers the §2.6 verb-tool wave at unit
// granularity. The contract worth pinning per tool is (a) it produces
// a sensible result for typical input, (b) it does not crash on
// degenerate input, (c) state-mutating tools roundtrip through the
// reader tool in the same registry.

// --- working set --------------------------------------------------

func TestToolAddToWorkingSet_AddsAndReportsDuplicates(t *testing.T) {
	// iter56a: workingset validates paths via session's working dir.
	sess, _ := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, t.TempDir())
	add := &toolAddToWorkingSet{sess: sess}
	res, err := add.run(context.Background(), sid, json.RawMessage(`{"paths":["a.go","b.go"]}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "added 2")
	require.Contains(t, res.Content, "a.go")

	// Re-adding b.go reports duplicate; c.go is new.
	res2, err := add.run(context.Background(), sid, json.RawMessage(`{"paths":["b.go","c.go"]}`))
	require.NoError(t, err)
	require.Contains(t, res2.Content, "added 1")
	require.Contains(t, res2.Content, "already present")
	require.Contains(t, res2.Content, "b.go")
}

func TestToolAddToWorkingSet_RejectsBadJSON(t *testing.T) {
	sess, _ := newTestSessionService(t)
	add := &toolAddToWorkingSet{sess: sess}
	res, err := add.run(context.Background(), "s1", json.RawMessage(`not json`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestToolAddToWorkingSet_EmptyPathsIsNoOpNotError(t *testing.T) {
	sess, _ := newTestSessionService(t)
	add := &toolAddToWorkingSet{sess: sess}
	res, err := add.run(context.Background(), "s1", json.RawMessage(`{"paths":[]}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no paths")
}

func TestToolListWorkingSet_SortedAndStable(t *testing.T) {
	sess, _ := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, t.TempDir())
	add := &toolAddToWorkingSet{sess: sess}
	list := &toolListWorkingSet{sess: sess}

	_, _ = add.run(context.Background(), sid, json.RawMessage(`{"paths":["z.go","a.go","m.go"]}`))
	res, err := list.run(context.Background(), sid, json.RawMessage(`{}`))
	require.NoError(t, err)
	// Sorted output is the user-facing stability contract.
	require.Contains(t, res.Content, "a.go\n  m.go\n  z.go")
}

func TestToolListWorkingSet_EmptySessionIsEmpty(t *testing.T) {
	sess, _ := newTestSessionService(t)
	list := &toolListWorkingSet{sess: sess}
	res, err := list.run(context.Background(), "fresh-sid", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, res.Content, "empty")
}

func TestToolDropFromWorkingSet_RoundtripWithList(t *testing.T) {
	sess, _ := newTestSessionService(t)
	sid := newTestSession(t, sess.repo, t.TempDir())
	add := &toolAddToWorkingSet{sess: sess}
	drop := &toolDropFromWorkingSet{sess: sess}
	list := &toolListWorkingSet{sess: sess}

	_, _ = add.run(context.Background(), sid, json.RawMessage(`{"paths":["a.go","b.go","c.go"]}`))
	_, _ = drop.run(context.Background(), sid, json.RawMessage(`{"paths":["b.go","ghost.go"]}`))

	res, _ := list.run(context.Background(), sid, json.RawMessage(`{}`))
	require.Contains(t, res.Content, "a.go")
	require.NotContains(t, res.Content, "b.go")
	require.Contains(t, res.Content, "c.go")
}

func TestWorkingSet_PerSessionIsolation(t *testing.T) {
	// Two sessions must not see each other's working sets.
	// iter56a: add_to_workingset now validates paths via the session's
	// working dir, so the test must register real sessions instead of
	// passing bare "s1"/"s2" strings.
	sess, _ := newTestSessionService(t)
	wd := t.TempDir()
	s1 := newTestSession(t, sess.repo, wd)
	s2 := newTestSession(t, sess.repo, wd)
	add := &toolAddToWorkingSet{sess: sess}
	list := &toolListWorkingSet{sess: sess}

	_, _ = add.run(context.Background(), s1, json.RawMessage(`{"paths":["s1-only.go"]}`))
	_, _ = add.run(context.Background(), s2, json.RawMessage(`{"paths":["s2-only.go"]}`))

	r1, _ := list.run(context.Background(), s1, json.RawMessage(`{}`))
	r2, _ := list.run(context.Background(), s2, json.RawMessage(`{}`))
	require.Contains(t, r1.Content, "s1-only.go")
	require.NotContains(t, r1.Content, "s2-only.go")
	require.Contains(t, r2.Content, "s2-only.go")
	require.NotContains(t, r2.Content, "s1-only.go")
}

// --- stop_run -----------------------------------------------------

func TestToolStopRun_NoRunServiceIsError(t *testing.T) {
	stop := &toolStopRun{rs: nil}
	res, err := stop.run(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestToolStopRun_NoActiveRunReportsCleanly(t *testing.T) {
	// RequestStop on a session with no registered cancel func should
	// return false, surfaced as a non-error informational message.
	repo := newTestRepo(t)
	base := t.TempDir()
	_ = base
	rs := NewRunService(repo, base, nil)
	stop := &toolStopRun{rs: rs}
	res, err := stop.run(context.Background(), "no-such-session", nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no run in flight")
}

// --- show_instructions / export_session / reset_session ----------

func TestToolShowInstructions_ListsToolFamilies(t *testing.T) {
	sess, _ := newTestSessionService(t)
	show := &toolShowInstructions{sess: sess}
	res, err := show.run(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	// Spot-check that key categories are surfaced so a future
	// reordering doesn't accidentally drop the contract.
	require.Contains(t, res.Content, "subagent")
	require.Contains(t, res.Content, "verify")
	require.Contains(t, res.Content, "workingset")
	require.Contains(t, res.Content, "natural language")
}

func TestToolExportSession_EmptyHistoryReports(t *testing.T) {
	sess, _ := newTestSessionService(t)
	exp := &toolExportSession{sess: sess}
	res, err := exp.run(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.Contains(t, res.Content, "no conversation history")
}

func TestToolResetSession_ClearsHistory(t *testing.T) {
	sess, _ := newTestSessionService(t)
	// Seed history via chatHistory directly so the test doesn't
	// require a full Prompt round-trip.
	hist := sess.chatHistory()
	hist.append("s1", anyTestMessage("user", "hi"))
	hist.append("s1", anyTestMessage("assistant", "hello"))
	require.Len(t, hist.get("s1"), 2)

	reset := &toolResetSession{sess: sess}
	res, err := reset.run(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.Contains(t, res.Content, "cleared")
	require.Empty(t, hist.get("s1"))
}

func TestToolExportSession_AfterTurns(t *testing.T) {
	sess, _ := newTestSessionService(t)
	hist := sess.chatHistory()
	hist.append("s1", anyTestMessage("user", "what files?"))
	hist.append("s1", anyTestMessage("assistant", "i'll check"))

	exp := &toolExportSession{sess: sess}
	res, err := exp.run(context.Background(), "s1", nil)
	require.NoError(t, err)
	require.Contains(t, res.Content, "session: s1")
	require.Contains(t, res.Content, "turns: 2")
	require.Contains(t, res.Content, "what files?")
}

// --- list_checkpoints / restore_checkpoint -----------------------

func TestToolListCheckpoints_NoSessionIsError(t *testing.T) {
	repo := newTestRepo(t)
	base := t.TempDir()
	list := &toolListCheckpoints{repo: repo, base: base}
	res, err := list.run(context.Background(), "ghost-sid", nil)
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestToolListCheckpoints_EmptyForFreshSession(t *testing.T) {
	repo := newTestRepo(t)
	base := t.TempDir()
	sid := newTestSession(t, repo, t.TempDir())
	list := &toolListCheckpoints{repo: repo, base: base}
	res, err := list.run(context.Background(), sid, nil)
	require.NoError(t, err)
	// Fresh session has no shadow-git history; either the list is
	// empty OR the list failed because shadow has no commits. Both
	// outcomes are acceptable user feedback; the tool just must not
	// crash.
	require.NotEmpty(t, res.Content)
}

func TestToolRestoreCheckpoint_NoRunServiceIsError(t *testing.T) {
	r := &toolRestoreCheckpoint{rs: nil}
	res, err := r.run(context.Background(), "s1", json.RawMessage(`{"step":1}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestToolRestoreCheckpoint_RejectsBadJSON(t *testing.T) {
	repo := newTestRepo(t)
	rs := NewRunService(repo, t.TempDir(), nil)
	r := &toolRestoreCheckpoint{rs: rs}
	res, err := r.run(context.Background(), "s1", json.RawMessage(`not json`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

// --- helpers ------------------------------------------------------

// anyTestMessage builds a provider.Message for seeding chatHistory in
// tests. The verb tools never inspect ToolCalls/Results payloads, only
// the Role + Content surface, so the minimal field set suffices.
func anyTestMessage(role, content string) provider.Message {
	return provider.Message{Role: provider.Role(role), Content: content}
}
