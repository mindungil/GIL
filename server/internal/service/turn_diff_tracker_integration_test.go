package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// turn_diff_tracker_integration_test.go pins the contract that the
// write tools (write_file, edit_file, run_bash) populate the diff
// tracker correctly. show_diff doesn't appear here because it lives
// behind the chatTool.run() interface and is exercised at the same
// layer as the other tools — see TestToolShowDiff_* in service_test.go.

func TestWriteFile_RecordsToTracker(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.txt"),
		[]byte("original content\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolWriteFile{repo: repo, tracker: tr}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.txt","content":"new content\n"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	body, files, added, removed := drainSummary(tr, sid)
	require.Equal(t, 1, files)
	require.GreaterOrEqual(t, added, 1)
	require.GreaterOrEqual(t, removed, 1)
	require.Contains(t, body, "+new content")
	require.Contains(t, body, "-original content")
}

func TestEditFile_RecordsToTracker(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("package x\n\nfunc Old() {}\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolEditFile{repo: repo, tracker: tr}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.go","old_text":"func Old() {}","new_text":"func New() {}"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	body, files, _, _ := drainSummary(tr, sid)
	require.Equal(t, 1, files)
	require.Contains(t, body, "-func Old()")
	require.Contains(t, body, "+func New()")
}

func TestRunBash_MarksTrackerPolluted(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolRunBash{repo: repo, tracker: tr}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"cmd":"true"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	_, polluted := tr.snapshot(sid)
	require.True(t, polluted, "run_bash must flip the polluted flag")
}

func TestWriteFile_NewFile_RecordsAsCreate(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolWriteFile{repo: repo, tracker: tr}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"created.go","content":"package new\n"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	body, files, _, _ := drainSummary(tr, sid)
	require.Equal(t, 1, files)
	require.Contains(t, body, "/dev/null", "newly created file should diff against /dev/null")
	require.Contains(t, body, "+package new")
}

func TestShowDiff_DrainsTracker(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.txt"),
		[]byte("hello\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	wf := &toolWriteFile{repo: repo, tracker: tr}
	_, err := wf.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.txt","content":"world\n"}`))
	require.NoError(t, err)

	// show_diff should pull from the tracker, not call out to RunService
	// (which is nil here — covers the chat-only session case).
	sd := &toolShowDiff{rs: nil, tracker: tr}
	res, err := sd.run(context.Background(), sid, nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "turn-scoped")
	require.Contains(t, res.Content, "-hello")
	require.Contains(t, res.Content, "+world")
}

func TestShowDiff_EmptyTrackerEmptyResponse(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)
	_ = repo
	_ = sid

	tr := newTurnDiffTracker()
	sd := &toolShowDiff{rs: nil, tracker: tr}
	res, err := sd.run(context.Background(), sid, nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no changes")
}

func TestWriteFile_TwoEdits_OriginalCollapsed(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.txt"),
		[]byte("v0\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolWriteFile{repo: repo, tracker: tr}

	_, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.txt","content":"v1\n"}`))
	require.NoError(t, err)
	_, err = tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.txt","content":"v2\n"}`))
	require.NoError(t, err)

	body, _, _, _ := drainSummary(tr, sid)
	require.Contains(t, body, "-v0", "original v0 must remain the baseline")
	require.Contains(t, body, "+v2", "current must be v2")
	require.NotContains(t, body, "v1", "intermediate v1 must be collapsed away")
}
