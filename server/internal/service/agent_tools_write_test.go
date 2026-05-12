package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
