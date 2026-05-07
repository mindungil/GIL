package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolEditFile_UniqueMatch(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("package x\nfunc Old() {}\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolEditFile{repo: repo}
	res, err := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.go","old_text":"func Old() {}","new_text":"func New() {}"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)

	body, _ := os.ReadFile(filepath.Join(wd, "a.go"))
	require.Contains(t, string(body), "func New()")
	require.NotContains(t, string(body), "func Old()")
}

func TestToolEditFile_AmbiguousMatchRejected(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"),
		[]byte("foo\nfoo\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolEditFile{repo: repo}
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.go","old_text":"foo","new_text":"bar"}`))
	require.True(t, res.IsError, "ambiguous match must be rejected")
	require.Contains(t, res.Content, "matches 2")
}

func TestToolEditFile_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	wd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wd, "a.go"), []byte("package x\n"), 0o644))
	sid := newTestSession(t, repo, wd)

	tool := &toolEditFile{repo: repo}
	res, _ := tool.run(context.Background(), sid,
		json.RawMessage(`{"path":"a.go","old_text":"NEVER_HERE","new_text":""}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "not found")
}

func TestToolTodoWrite_ReplacesAndRenders(t *testing.T) {
	tool := &toolTodoWrite{}
	sid := "session-todo-1"
	defer globalTodoStore.replace(sid, nil)

	args := `{"items":[
		{"text":"design","status":"completed"},
		{"text":"build","status":"in_progress"},
		{"text":"verify","status":"pending"}
	]}`
	res, err := tool.run(context.Background(), sid, json.RawMessage(args))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "[x] 1. design")
	require.Contains(t, res.Content, "[~] 2. build")
	require.Contains(t, res.Content, "[ ] 3. verify")

	// Persisted across calls within the session.
	snap := globalTodoStore.snapshot(sid)
	require.Len(t, snap, 3)
	require.Equal(t, "in_progress", snap[1].Status)
}

func TestToolTodoWrite_RejectsBadStatus(t *testing.T) {
	tool := &toolTodoWrite{}
	res, _ := tool.run(context.Background(), "any",
		json.RawMessage(`{"items":[{"text":"x","status":"oopsie"}]}`))
	require.True(t, res.IsError)
}

func TestToolWebFetch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello from gil-test"))
	}))
	defer srv.Close()

	tool := &toolWebFetch{}
	args, _ := json.Marshal(struct {
		URL string `json:"url"`
	}{URL: srv.URL})
	res, err := tool.run(context.Background(), "", args)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	require.Contains(t, res.Content, "hello from gil-test")
	require.Contains(t, res.Content, "200")
}

func TestToolWebFetch_RejectsNonHTTPScheme(t *testing.T) {
	tool := &toolWebFetch{}
	res, _ := tool.run(context.Background(), "",
		json.RawMessage(`{"url":"file:///etc/passwd"}`))
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "http(s)")
}

func TestToolWebFetch_404IsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	tool := &toolWebFetch{}
	args, _ := json.Marshal(struct {
		URL string `json:"url"`
	}{URL: srv.URL})
	res, _ := tool.run(context.Background(), "", args)
	require.True(t, res.IsError, "4xx should flag IsError so the agent retries / pivots")
	require.Contains(t, res.Content, "404")
}

func TestResolveAgent_DefaultAndExplore(t *testing.T) {
	a, err := resolveAgent("")
	require.NoError(t, err)
	require.Equal(t, "default", a.Name)
	require.Empty(t, a.Tools, "default agent has full toolset (empty whitelist)")

	e, err := resolveAgent("explore")
	require.NoError(t, err)
	require.Equal(t, "explore", e.Name)
	require.Contains(t, e.Tools, "read_file")
	require.NotContains(t, e.Tools, "write_file", "explore agent must not have write tools")
	require.NotContains(t, e.Tools, "run_bash")
}

func TestResolveAgent_UnknownErrors(t *testing.T) {
	_, err := resolveAgent("nonexistent-xyz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown agent")
}

func TestRegistry_FilterByName(t *testing.T) {
	full := &chatToolRegistry{tools: []chatTool{
		&toolReadFile{}, &toolWriteFile{}, &toolRunBash{},
	}}
	filtered := full.filterByName([]string{"read_file"})
	require.Len(t, filtered.tools, 1)
	require.Equal(t, "read_file", filtered.tools[0].name())

	// Empty allow returns full registry.
	full2 := full.filterByName(nil)
	require.Len(t, full2.tools, 3)

	// Unknown names silently dropped.
	odd := full.filterByName([]string{"read_file", "no_such_tool"})
	require.Len(t, odd.tools, 1, "unknown tool names should be dropped, not error")
	require.Equal(t, strings.ToLower("read_file"), odd.tools[0].name())
}
