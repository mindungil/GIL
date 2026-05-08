package service

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTurnDiffTracker_RecordsModification(t *testing.T) {
	tr := newTurnDiffTracker()
	tmp := t.TempDir() + "/a.txt"
	require.NoError(t, writeAll(tmp, "line one\nline two\n"))

	tr.recordPreWrite("sess-1", "a.txt", tmp)
	tr.recordPostWrite("sess-1", "a.txt", "line one\nLINE TWO\n", true)

	body, files, added, removed := drainSummary(tr, "sess-1")
	require.Equal(t, 1, files)
	require.Equal(t, 1, added)
	require.Equal(t, 1, removed)
	require.Contains(t, body, "-line two")
	require.Contains(t, body, "+LINE TWO")
	require.Contains(t, body, "a/a.txt")
	require.Contains(t, body, "b/a.txt")
}

func TestTurnDiffTracker_NewFile(t *testing.T) {
	tr := newTurnDiffTracker()
	// Pre-write hook called with absPath that doesn't exist — original
	// is empty, OriginalExisted=false, so render targets /dev/null.
	tr.recordPreWrite("s", "new.go", "/nonexistent/should/not/be/here.go")
	tr.recordPostWrite("s", "new.go", "package x\n", true)

	body, _, _, _ := drainSummary(tr, "s")
	require.Contains(t, body, "/dev/null")
	require.Contains(t, body, "+package x")
}

func TestTurnDiffTracker_FirstObservationWins(t *testing.T) {
	tr := newTurnDiffTracker()
	tmp := t.TempDir() + "/a.txt"
	require.NoError(t, writeAll(tmp, "ORIG\n"))

	tr.recordPreWrite("s", "a.txt", tmp)
	tr.recordPostWrite("s", "a.txt", "FIRST_EDIT\n", true)

	// Even after a second pre-write call, original stays as ORIG.
	require.NoError(t, writeAll(tmp, "FIRST_EDIT\n"))
	tr.recordPreWrite("s", "a.txt", tmp)
	tr.recordPostWrite("s", "a.txt", "SECOND_EDIT\n", true)

	body, _, _, _ := drainSummary(tr, "s")
	require.Contains(t, body, "-ORIG")
	require.Contains(t, body, "+SECOND_EDIT")
	require.NotContains(t, body, "FIRST_EDIT", "intermediate state must be collapsed")
}

func TestTurnDiffTracker_Reset(t *testing.T) {
	tr := newTurnDiffTracker()
	tr.recordPreWrite("s", "a", "/dev/null")
	tr.recordPostWrite("s", "a", "x", true)
	tr.reset("s")

	files, polluted := tr.snapshot("s")
	require.Empty(t, files)
	require.False(t, polluted)
}

func TestTurnDiffTracker_PollutedNoFiles(t *testing.T) {
	tr := newTurnDiffTracker()
	tr.markExternal("s")
	body, files, _, _ := drainSummary(tr, "s")
	require.Equal(t, 0, files)
	require.Contains(t, body, "run_bash executed")
}

func TestTurnDiffTracker_PollutedWithFiles(t *testing.T) {
	tr := newTurnDiffTracker()
	tr.recordPreWrite("s", "a.go", "/no/such/path")
	tr.recordPostWrite("s", "a.go", "x\n", true)
	tr.markExternal("s")

	body, files, _, _ := drainSummary(tr, "s")
	require.Equal(t, 1, files)
	require.Contains(t, body, "+x")
	require.Contains(t, body, "run_bash executed")
}

func TestTurnDiffTracker_NoChangeIsEmpty(t *testing.T) {
	tr := newTurnDiffTracker()
	tmp := t.TempDir() + "/a.txt"
	require.NoError(t, writeAll(tmp, "same\n"))

	tr.recordPreWrite("s", "a.txt", tmp)
	tr.recordPostWrite("s", "a.txt", "same\n", true)

	body, files, _, _ := drainSummary(tr, "s")
	require.Equal(t, 0, files, "touched-but-unchanged file must not surface as a diff")
	require.Empty(t, strings.TrimSpace(body))
}

func TestTurnDiffTracker_PerSessionIsolation(t *testing.T) {
	tr := newTurnDiffTracker()
	tr.recordPreWrite("s1", "a.txt", "/no")
	tr.recordPostWrite("s1", "a.txt", "S1\n", true)

	tr.recordPreWrite("s2", "b.txt", "/no")
	tr.recordPostWrite("s2", "b.txt", "S2\n", true)

	body1, _, _, _ := drainSummary(tr, "s1")
	body2, _, _, _ := drainSummary(tr, "s2")

	require.Contains(t, body1, "S1")
	require.NotContains(t, body1, "S2")
	require.Contains(t, body2, "S2")
	require.NotContains(t, body2, "S1")
}

func writeAll(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// drainSummary is a test helper that takes a snapshot and renders it
// the same way show_diff would.
func drainSummary(tr *turnDiffTracker, sid string) (body string, files, added, removed int) {
	snap, polluted := tr.snapshot(sid)
	return renderTrackerSummary(snap, polluted)
}
