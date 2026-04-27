package mcpregistry

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_EmptyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{
		GlobalPath:  filepath.Join(dir, "global.toml"),
		ProjectPath: filepath.Join(dir, "project.toml"),
	}
	got, err := r.Load()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestLoad_EmptyPathsSafe(t *testing.T) {
	r := &Registry{}
	got, err := r.Load()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestAddGlobal_ThenLoad(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}

	srv := Server{
		Name:        "fs",
		Type:        "stdio",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "/workspace"},
		Description: "Filesystem MCP",
	}
	require.NoError(t, r.AddGlobal(srv))

	got, err := r.Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "fs", got["fs"].Name)
	require.Equal(t, "stdio", got["fs"].Type)
	require.Equal(t, "npx", got["fs"].Command)
	require.Equal(t, []string{"-y", "@modelcontextprotocol/server-filesystem", "/workspace"}, got["fs"].Args)
	require.Equal(t, "Filesystem MCP", got["fs"].Description)
}

func TestAddGlobal_DuplicateRejected(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}

	srv := Server{Name: "fs", Type: "stdio", Command: "npx"}
	require.NoError(t, r.AddGlobal(srv))
	err := r.AddGlobal(srv)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func TestProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".gil")
	require.NoError(t, os.MkdirAll(projDir, 0o700))

	r := &Registry{
		GlobalPath:  filepath.Join(dir, "global.toml"),
		ProjectPath: filepath.Join(projDir, "mcp.toml"),
	}

	require.NoError(t, r.AddGlobal(Server{
		Name: "fs", Type: "stdio", Command: "npx", Args: []string{"global"},
	}))
	require.NoError(t, r.AddProject(Server{
		Name: "fs", Type: "stdio", Command: "npx", Args: []string{"project"},
	}))

	got, err := r.Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []string{"project"}, got["fs"].Args, "project should win over global")
}

func TestAddProject_RequiresProjectDir(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{
		ProjectPath: filepath.Join(dir, "no-such-dir", ".gil", "mcp.toml"),
	}
	err := r.AddProject(Server{Name: "fs", Type: "stdio", Command: "npx"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gil init")
}

func TestRemove_Global(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))

	require.NoError(t, r.Remove("fs", ScopeGlobal))

	got, err := r.Load()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRemove_Project(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".gil")
	require.NoError(t, os.MkdirAll(projDir, 0o700))

	r := &Registry{
		GlobalPath:  filepath.Join(dir, "global.toml"),
		ProjectPath: filepath.Join(projDir, "mcp.toml"),
	}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))
	require.NoError(t, r.AddProject(Server{Name: "fs", Type: "stdio", Command: "npx", Args: []string{"p"}}))

	require.NoError(t, r.Remove("fs", ScopeProject))

	got, err := r.Load()
	require.NoError(t, err)
	require.Len(t, got, 1, "global entry should remain")
	require.Empty(t, got["fs"].Args)
}

func TestRemove_AutoPicksFirst(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".gil")
	require.NoError(t, os.MkdirAll(projDir, 0o700))

	r := &Registry{
		GlobalPath:  filepath.Join(dir, "global.toml"),
		ProjectPath: filepath.Join(projDir, "mcp.toml"),
	}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))
	require.NoError(t, r.AddProject(Server{Name: "fs", Type: "stdio", Command: "npx", Args: []string{"p"}}))

	// auto picks global first per the documented contract.
	require.NoError(t, r.Remove("fs", ScopeAuto))

	gl, err := r.LoadScope(ScopeGlobal)
	require.NoError(t, err)
	require.Empty(t, gl)
	pr, err := r.LoadScope(ScopeProject)
	require.NoError(t, err)
	require.Len(t, pr, 1)
}

func TestRemove_NotFoundError(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))

	err := r.Remove("nope", ScopeGlobal)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestRemove_AutoNoneMatches(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))

	err := r.Remove("nope", ScopeAuto)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestValidate_StdioRequiresCommand(t *testing.T) {
	require.Error(t, Validate(Server{Name: "x", Type: "stdio"}))
}

func TestValidate_StdioRejectsURL(t *testing.T) {
	err := Validate(Server{Name: "x", Type: "stdio", Command: "npx", URL: "https://"})
	require.Error(t, err)
}

func TestValidate_HTTPRequiresURL(t *testing.T) {
	require.Error(t, Validate(Server{Name: "x", Type: "http"}))
}

func TestValidate_HTTPRejectsCommand(t *testing.T) {
	err := Validate(Server{Name: "x", Type: "http", URL: "https://x", Command: "npx"})
	require.Error(t, err)
}

func TestValidate_TypeRequired(t *testing.T) {
	require.Error(t, Validate(Server{Name: "x"}))
}

func TestValidate_UnknownType(t *testing.T) {
	require.Error(t, Validate(Server{Name: "x", Type: "ws", URL: "ws://"}))
}

func TestValidate_AuthFormat(t *testing.T) {
	require.NoError(t, Validate(Server{Name: "x", Type: "http", URL: "https://x"}))
	require.NoError(t, Validate(Server{Name: "x", Type: "http", URL: "https://x", Auth: "bearer:abc123"}))
	err := Validate(Server{Name: "x", Type: "http", URL: "https://x", Auth: "basic:bob:pass"})
	require.Error(t, err)
}

func TestFileMode0600AfterAdd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "global.toml")
	r := &Registry{GlobalPath: path}
	require.NoError(t, r.AddGlobal(Server{Name: "fs", Type: "stdio", Command: "npx"}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestConcurrentAdd_LastWriterWinsNoCorruption(t *testing.T) {
	// We don't claim true atomicity here (no lock layer); the property
	// being asserted is "the file remains parseable and contains some
	// subset of the writes" — which the tmp+rename strategy guarantees
	// even when calls race. Without rename, a partial write would be
	// observable as a parse error on the next Load.
	dir := t.TempDir()
	path := filepath.Join(dir, "global.toml")

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			r := &Registry{GlobalPath: path}
			s := Server{
				Name:    "srv-" + string(rune('a'+i)),
				Type:    "stdio",
				Command: "echo",
				Args:    []string{"hi"},
			}
			// Ignore "already exists" — the property under test is
			// "no parse error from a half-written file".
			_ = r.AddGlobal(s)
		}()
	}
	wg.Wait()

	r := &Registry{GlobalPath: path}
	got, err := r.Load()
	require.NoError(t, err)
	// At least one writer succeeded.
	require.NotEmpty(t, got)
}

func TestRoundTrip_HTTPWithAuth(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{GlobalPath: filepath.Join(dir, "global.toml")}

	require.NoError(t, r.AddGlobal(Server{
		Name:        "search",
		Type:        "http",
		URL:         "https://example.com/mcp",
		Auth:        "bearer:sk-test-1234",
		Description: "Web search MCP",
	}))

	got, err := r.Load()
	require.NoError(t, err)
	require.Equal(t, "http", got["search"].Type)
	require.Equal(t, "https://example.com/mcp", got["search"].URL)
	require.Equal(t, "bearer:sk-test-1234", got["search"].Auth)
	require.Equal(t, "Web search MCP", got["search"].Description)
}
