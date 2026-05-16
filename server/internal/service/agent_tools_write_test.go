package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/session"
)

// agent_tools_write_test.go covers the read_file/write_file/run_bash/
// grep/glob tools at unit-test granularity. Each test stands up a
// SQLite-backed session.Repo, creates a session pinned to a temp
// working directory, and exercises the tool directly. We deliberately
// don't go through the full SessionService.Prompt RPC — the tool
// surface is the contract worth pinning, and unit-level coverage runs
// orders of magnitude faster than spinning a daemon.

func newTestRepo(t *testing.T) *session.Repo {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	require.NoError(t, session.Migrate(db))
	t.Cleanup(func() { _ = db.Close() })
	return session.NewRepo(db)
}

func newTestSession(t *testing.T, repo *session.Repo, wd string) string {
	t.Helper()
	s, err := repo.Create(context.Background(), session.CreateInput{
		WorkingDir: wd,
		GoalHint:   "test",
	})
	require.NoError(t, err)
	return s.ID
}

func TestResolveInWD_RejectsEscape(t *testing.T) {
	wd := t.TempDir()
	_, err := resolveInWD(wd, "../../etc/passwd")
	require.Error(t, err, "must reject path that escapes working dir")
}

func TestResolveInWD_AcceptsInside(t *testing.T) {
	wd := t.TempDir()
	abs, err := resolveInWD(wd, "subdir/file.go")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(abs, wd))
}

func TestToolReadFile_RoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "hello.txt"), []byte("hi there\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolReadFile{repo: repo}
	res, err := tool.run(context.Background(), sid, json.RawMessage(`{"path":"hello.txt"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Equal(t, "hi there\n", res.Content)
}

func TestToolReadFile_RejectsEscape(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tool := &toolReadFile{repo: repo}
	res, _ := tool.run(context.Background(), sid, json.RawMessage(`{"path":"../../../etc/passwd"}`))
	require.True(t, res.IsError, "escape must be flagged as error")
}

func TestToolWriteFile_CreatesAndOverwrites(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tool := &toolWriteFile{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"sub/file.go","content":"package x\n"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	body, err := os.ReadFile(filepath.Join(wd, "sub", "file.go"))
	require.NoError(t, err)
	require.Equal(t, "package x\n", string(body))

	// Overwrite.
	res2, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"sub/file.go","content":"package y\n"}`))
	require.NoError(t, err)
	require.False(t, res2.IsError)
	body, _ = os.ReadFile(filepath.Join(wd, "sub", "file.go"))
	require.Equal(t, "package y\n", string(body))
}

func TestToolRunBash_ExitCodeAndOutput(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	sid := newTestSession(t, repo, wd)

	tool := &toolRunBash{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"cmd":"echo hello && pwd"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "hello")
	require.Contains(t, res.Content, wd)
	require.Contains(t, res.Content, "[exit 0]")
}

func TestToolRunBash_NonZeroExitFlaggedError(t *testing.T) {
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, t.TempDir())

	tool := &toolRunBash{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"cmd":"exit 7"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "[exit 7]")
}

func TestToolRunBash_TimeoutCapped(t *testing.T) {
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, t.TempDir())

	tool := &toolRunBash{repo: repo}
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"cmd":"sleep 5","timeout_sec":1}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "timeout")
}

func TestToolGrep_FindsMatch(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("package x\nfunc Hello() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "b.go"),
		[]byte("package x\nfunc World() {}\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolGrep{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"pattern":"func Hello"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "a.go")
	require.NotContains(t, res.Content, "b.go:") // World is in b.go
}

func TestToolGrep_NoMatches(t *testing.T) {
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, t.TempDir())

	tool := &toolGrep{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"pattern":"NEVERFOUND_ZZZZ"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no matches")
}

func TestToolGlob_BasicAndRecursive(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"), []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(wd, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "sub", "b.go"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "sub", "c.txt"), []byte(""), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolGlob{repo: repo}

	// Non-recursive — only top-level a.go.
	res, err := tool.run(context.Background(), sid, json.RawMessage(`{"pattern":"*.go"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "a.go")
	require.NotContains(t, res.Content, "sub/b.go")

	// Recursive — both .go files.
	res2, err := tool.run(context.Background(), sid, json.RawMessage(`{"pattern":"**/*.go"}`))
	require.NoError(t, err)
	require.False(t, res2.IsError, res2.Content)
	require.Contains(t, res2.Content, "a.go")
	require.Contains(t, res2.Content, "sub/b.go")
	require.NotContains(t, res2.Content, "c.txt")
}

// --- C3 readonly target tests ----------------------------------------

func TestRejectReadonlyTarget_FileWritable_OK(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "writable.txt")
	if err := os.WriteFile(p, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rejectReadonlyTarget(p); err != nil {
		t.Fatalf("writable file rejected: %v", err)
	}
}

func TestRejectReadonlyTarget_FileReadonly_Reject(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ro.txt")
	if err := os.WriteFile(p, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	err := rejectReadonlyTarget(p)
	if err == nil {
		t.Fatalf("expected reject, got nil")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error message missing 'read-only': %v", err)
	}
}

func TestRejectReadonlyTarget_FileMissing_OK(t *testing.T) {
	// Creating a new file under a writable parent is fine.
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")
	if err := rejectReadonlyTarget(p); err != nil {
		t.Fatalf("missing file (create case) rejected: %v", err)
	}
}

func TestToolWriteFile_ReadonlyTarget_Rejects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, dir)
	tool := &toolWriteFile{repo: repo}
	res, _ := tool.run(context.Background(), sid, json.RawMessage(`{"path":"locked.go","content":"package x\nvar X = 1\n"}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got result %+v", res)
	}
	// File content unchanged.
	body, _ := os.ReadFile(target)
	if string(body) != "package x\n" {
		t.Fatalf("file mutated despite readonly: %q", body)
	}
}

func TestToolEditFile_ReadonlyTarget_Rejects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	if err := os.WriteFile(target, []byte("package x\nvar A = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, dir)
	tool := &toolEditFile{repo: repo}
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"path":"locked.go","old_text":"var A = 1","new_text":"var A = 2"}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "package x\nvar A = 1\n" {
		t.Fatalf("file mutated despite readonly: %q", body)
	}
}

func TestToolApplyPatch_ReadonlyTarget_Rejects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	repo := newTestRepo(t)
	sid := newTestSession(t, repo, dir)
	tool := &toolApplyPatch{repo: repo}
	// Simple Update File patch attempting to add a line.
	patch := "*** Begin Patch\n*** Update File: locked.go\n@@\n package x\n+var Z = 1\n*** End Patch\n"
	res, _ := tool.run(t.Context(), sid, json.RawMessage(`{"patch":`+strconv.Quote(patch)+`}`))
	if !res.IsError {
		t.Fatalf("expected IsError, got %+v", res)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "package x\n" {
		t.Fatalf("file mutated despite readonly: %q", body)
	}
}

// --- C3 P32 amendment: chmod via run_bash also gated -----------------

func TestRejectRunBashChmodOnReadonly_AddsWriteSymbolic_Reject(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	require.NoError(t, os.Chmod(target, 0o444))

	cases := []string{
		"chmod +w locked.go",
		"chmod u+w locked.go",
		"chmod a+w locked.go",
		"chmod ug+w locked.go",
		"chmod +rw locked.go",
		"chmod +wx locked.go",
		"chmod +w locked.go && grep foo locked.go",
		"chmod 644 locked.go",
		"chmod 0664 locked.go",
		"chmod 755 locked.go",
		"chmod -R +w locked.go",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			err := rejectRunBashChmodOnReadonly(c, dir)
			require.Error(t, err, "should reject: %s", c)
			require.Contains(t, err.Error(), "read-only")
		})
	}
}

func TestRejectRunBashChmodOnReadonly_AbsolutePath_Reject(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	require.NoError(t, os.Chmod(target, 0o444))

	cmd := "chmod +w " + target
	err := rejectRunBashChmodOnReadonly(cmd, dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

func TestRejectRunBashChmodOnReadonly_NonChmodPasses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	require.NoError(t, os.Chmod(target, 0o444))

	cases := []string{
		"ls -la",
		"go build ./...",
		"cat locked.go",
		"grep foo locked.go",
		"echo chmod +w fake.go", // chmod is just an arg, not the leading cmd
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			err := rejectRunBashChmodOnReadonly(c, dir)
			require.NoError(t, err, "non-chmod cmd should pass: %s", c)
		})
	}
}

func TestRejectRunBashChmodOnReadonly_RemoveWritePasses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "writable.go")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	// Target is writable; chmod -w shouldn't trip this gate either way,
	// but specifically: removing-write modes should pass since they
	// don't grant write.
	cases := []string{
		"chmod -w writable.go",
		"chmod 444 writable.go",
		"chmod 0444 writable.go",
		"chmod 555 writable.go",
		"chmod a-w writable.go",
		"chmod u-w writable.go",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			err := rejectRunBashChmodOnReadonly(c, dir)
			require.NoError(t, err, "non-write-adding chmod should pass: %s", c)
		})
	}
}

func TestRejectRunBashChmodOnReadonly_WritableTargetPasses(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "writable.go")
	require.NoError(t, os.WriteFile(target, []byte("ok"), 0o644))
	// Target writable; chmod +w on it is a no-op but we don't reject
	// since the user hasn't marked it readonly.
	err := rejectRunBashChmodOnReadonly("chmod +w writable.go", dir)
	require.NoError(t, err)
}

func TestToolRunBash_ChmodOnReadonly_Rejects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.go")
	require.NoError(t, os.WriteFile(target, []byte("package x\n"), 0o644))
	require.NoError(t, os.Chmod(target, 0o444))

	repo := newTestRepo(t)
	sid := newTestSession(t, repo, dir)
	tool := &toolRunBash{repo: repo}
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"cmd":"chmod +w locked.go && grep foo locked.go"}`))
	require.True(t, res.IsError, "should be IsError, got %+v", res)
	require.Contains(t, res.Content, "read-only")

	// File mode should be unchanged.
	info, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o444), info.Mode().Perm(),
		"chmod must NOT have run despite the run_bash call")
}

func TestIsNumericModeAddsOwnerWrite(t *testing.T) {
	cases := []struct {
		tok    string
		wantOK bool
	}{
		{"644", true},
		{"755", true},
		{"0644", true},
		{"0755", true},
		{"666", true},
		{"777", true},
		{"764", true},
		{"444", false},
		{"555", false},
		{"0444", false},
		{"0555", false},
		{"445", false},
		{"abc", false},
		{"12", false},
		{"12345", false},
		{"858", false}, // 8 isn't octal
	}
	for _, tc := range cases {
		t.Run(tc.tok, func(t *testing.T) {
			got := isNumericModeAddsOwnerWrite(tc.tok)
			require.Equal(t, tc.wantOK, got, "isNumericModeAddsOwnerWrite(%q)", tc.tok)
		})
	}
}
