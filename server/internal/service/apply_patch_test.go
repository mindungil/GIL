package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// apply_patch_test.go covers the parser and applier directly, then a
// few tool-layer tests through toolApplyPatch.run() to confirm
// end-to-end wiring (path resolution, tracker hookup, error mapping).

// --- parser ----------------------------------------------------------

func TestParsePatch_AddFile(t *testing.T) {
	body := "*** Begin Patch\n*** Add File: hello.txt\n+Hello\n+World\n*** End Patch\n"
	ops, err := parsePatch(body)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, "add", ops[0].kind)
	require.Equal(t, "hello.txt", ops[0].path)
	require.Equal(t, []string{"Hello", "World"}, ops[0].addLines)
}

func TestParsePatch_DeleteFile(t *testing.T) {
	body := "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\n"
	ops, err := parsePatch(body)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, "delete", ops[0].kind)
	require.Equal(t, "gone.txt", ops[0].path)
}

func TestParsePatch_UpdateSimple(t *testing.T) {
	body := "*** Begin Patch\n" +
		"*** Update File: a.go\n" +
		"@@ func Foo()\n" +
		" pre\n" +
		"-old\n" +
		"+new\n" +
		" post\n" +
		"*** End Patch\n"
	ops, err := parsePatch(body)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, "update", ops[0].kind)
	require.Len(t, ops[0].hunks, 1)
	h := ops[0].hunks[0]
	require.Equal(t, "func Foo()", h.header)
	require.Len(t, h.lines, 4)
	require.Equal(t, byte(' '), h.lines[0].op)
	require.Equal(t, "pre", h.lines[0].text)
	require.Equal(t, byte('-'), h.lines[1].op)
	require.Equal(t, byte('+'), h.lines[2].op)
}

func TestParsePatch_MultiOp(t *testing.T) {
	body := "*** Begin Patch\n" +
		"*** Add File: a.txt\n+hi\n" +
		"*** Delete File: b.txt\n" +
		"*** Update File: c.go\n@@\n-x\n+y\n" +
		"*** End Patch\n"
	ops, err := parsePatch(body)
	require.NoError(t, err)
	require.Len(t, ops, 3)
	require.Equal(t, "add", ops[0].kind)
	require.Equal(t, "delete", ops[1].kind)
	require.Equal(t, "update", ops[2].kind)
}

func TestParsePatch_RejectsNoBegin(t *testing.T) {
	_, err := parsePatch("*** Add File: x\n*** End Patch\n")
	require.Error(t, err)
}

func TestParsePatch_RejectsNoEnd(t *testing.T) {
	_, err := parsePatch("*** Begin Patch\n*** Add File: x\n+hi\n")
	require.Error(t, err)
}

func TestParsePatch_RejectsBadHunkLine(t *testing.T) {
	body := "*** Begin Patch\n*** Update File: a\n@@\nWHAT\n*** End Patch\n"
	_, err := parsePatch(body)
	require.Error(t, err)
}

// --- applier ---------------------------------------------------------

func TestApplyHunks_SingleReplace(t *testing.T) {
	src := "alpha\nbeta\ngamma\n"
	hunks := []patchHunk{{
		lines: []patchLine{
			{op: ' ', text: "alpha"},
			{op: '-', text: "beta"},
			{op: '+', text: "BETA"},
			{op: ' ', text: "gamma"},
		},
	}}
	out, added, removed, err := applyHunks(src, hunks)
	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, 1, removed)
	require.Equal(t, "alpha\nBETA\ngamma\n", out)
}

func TestApplyHunks_AmbiguousMatchRejected(t *testing.T) {
	src := "x\nx\n"
	hunks := []patchHunk{{lines: []patchLine{{op: '-', text: "x"}, {op: '+', text: "y"}}}}
	_, _, _, err := applyHunks(src, hunks)
	require.Error(t, err)
}

func TestApplyHunks_NotFoundRejected(t *testing.T) {
	hunks := []patchHunk{{lines: []patchLine{{op: '-', text: "nope"}, {op: '+', text: "y"}}}}
	_, _, _, err := applyHunks("only one line\n", hunks)
	require.Error(t, err)
}

func TestApplyHunks_MultiHunkSequential(t *testing.T) {
	src := "a\nb\nc\nd\ne\n"
	hunks := []patchHunk{
		{lines: []patchLine{{op: ' ', text: "a"}, {op: '-', text: "b"}, {op: '+', text: "B"}}},
		{lines: []patchLine{{op: '-', text: "d"}, {op: '+', text: "D"}, {op: ' ', text: "e"}}},
	}
	out, added, removed, err := applyHunks(src, hunks)
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\nD\ne\n", out)
	require.Equal(t, 2, added)
	require.Equal(t, 2, removed)
}

func TestApplyHunks_PureInsertion(t *testing.T) {
	src := "header\nfooter\n"
	hunks := []patchHunk{{
		lines: []patchLine{
			{op: ' ', text: "header"},
			{op: '+', text: "middle"},
			{op: ' ', text: "footer"},
		},
	}}
	out, added, removed, err := applyHunks(src, hunks)
	require.NoError(t, err)
	require.Equal(t, "header\nmiddle\nfooter\n", out)
	require.Equal(t, 1, added)
	require.Equal(t, 0, removed)
}

// --- end-to-end via tool ---------------------------------------------

func TestToolApplyPatch_AtomicAcrossFiles(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("alpha\nbeta\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "b.go"),
		[]byte("one\ntwo\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tr := newTurnDiffTracker()
	tool := &toolApplyPatch{repo: repo, tracker: tr}
	patch := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n alpha\n-beta\n+BETA\n" +
		"*** Update File: b.go\n@@\n-one\n+ONE\n two\n" +
		"*** End Patch\n"
	body, _ := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: patch})
	res, err := tool.run(context.Background(), sid, body)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	a, _ := os.ReadFile(filepath.Join(wd, "a.go"))
	require.Equal(t, "alpha\nBETA\n", string(a))
	b, _ := os.ReadFile(filepath.Join(wd, "b.go"))
	require.Equal(t, "ONE\ntwo\n", string(b))

	diffBody, files, _, _ := drainSummary(tr, sid)
	require.Equal(t, 2, files)
	require.Contains(t, diffBody, "BETA")
	require.Contains(t, diffBody, "ONE")
}

func TestToolApplyPatch_FailsAtomically(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("good\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "b.go"),
		[]byte("untouched\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolApplyPatch{repo: repo}
	// Second hunk targets a path/content that doesn't match — entire
	// patch must reject without touching either file.
	patch := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n-good\n+GOOD\n" +
		"*** Update File: b.go\n@@\n-NEVER_HERE\n+x\n" +
		"*** End Patch\n"
	body, _ := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: patch})
	res, _ := tool.run(context.Background(), sid, body)
	require.True(t, res.IsError, "atomicity: failed hunk must reject the whole patch")

	a, _ := os.ReadFile(filepath.Join(wd, "a.go"))
	require.Equal(t, "good\n", string(a), "a.go must not be touched even though its hunk validated")
	b, _ := os.ReadFile(filepath.Join(wd, "b.go"))
	require.Equal(t, "untouched\n", string(b))
}

func TestToolApplyPatch_AddAndDelete(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "old.txt"),
		[]byte("bye\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolApplyPatch{repo: repo}
	patch := "*** Begin Patch\n" +
		"*** Add File: new.txt\n+hello\n+world\n" +
		"*** Delete File: old.txt\n" +
		"*** End Patch\n"
	body, _ := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: patch})
	res, err := tool.run(context.Background(), sid, body)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	newBody, _ := os.ReadFile(filepath.Join(wd, "new.txt"))
	require.Equal(t, "hello\nworld\n", string(newBody))
	_, err = os.Stat(filepath.Join(wd, "old.txt"))
	require.True(t, os.IsNotExist(err), "deleted file must be gone")
}

func TestToolApplyPatch_RejectsEscape(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tool := &toolApplyPatch{repo: repo}
	patch := "*** Begin Patch\n*** Add File: ../escape.txt\n+x\n*** End Patch\n"
	body, _ := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: patch})
	res, _ := tool.run(context.Background(), sid, body)
	require.True(t, res.IsError, "escaping path must be rejected")
}
