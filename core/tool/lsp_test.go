package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mindungil/gil/core/lsp"
	"github.com/stretchr/testify/require"
)

// osLookPath is a tiny shim around exec.LookPath so the smoke-test gating
// above reads cleanly.
func osLookPath(name string) (string, error) { return exec.LookPath(name) }

// newTestLSP wires the LSP tool against an empty Manager and the given
// workspace dir. Tests don't actually spawn gopls — they exercise the
// branches around argument parsing, file lookup, missing-server hints,
// and operation dispatch (which then surfaces "no language server for
// this file type" because the manager has empty Configs).
func newTestLSP(t *testing.T, dir string) *LSP {
	t.Helper()
	m := lsp.NewManager(dir)
	// Strip every default config so ClientFor always returns ErrNoServer —
	// good for testing the agent-friendly fallback messages without
	// depending on whether gopls / pyright are installed on this host.
	m.Configs = map[string]lsp.ServerConfig{}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return &LSP{Manager: m, WorkingDir: dir}
}

func runLSP(t *testing.T, l *LSP, args map[string]any) Result {
	t.Helper()
	b, err := json.Marshal(args)
	require.NoError(t, err)
	r, err := l.Run(context.Background(), b)
	require.NoError(t, err)
	return r
}

func TestLSP_MissingOperation(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "operation is required")
}

func TestLSP_UnknownOperation(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{"operation": "no_such_op"})
	require.True(t, r.IsError)
}

func TestLSP_MissingFile(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{"operation": "hover"})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "file is required")
}

func TestLSP_FileDoesNotExist(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{
		"operation": "hover",
		"file":      "/nope/does/not/exist.go",
		"line":      1,
		"column":    1,
	})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "cannot read file")
}

func TestLSP_MissingPosition(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644))
	l := newTestLSP(t, dir)
	r := runLSP(t, l, map[string]any{
		"operation": "hover",
		"file":      "x.go",
	})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "line and column are required")
}

func TestLSP_NoServerForExtension(t *testing.T) {
	// Manager has no .go config (we wiped Configs), so this should hit
	// the "no language server configured for this file type" hint.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644))
	l := newTestLSP(t, dir)
	r := runLSP(t, l, map[string]any{
		"operation": "hover",
		"file":      "x.go",
		"line":      1,
		"column":    1,
	})
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "no language server configured")
	require.Contains(t, r.Content, "grep/repomap")
}

func TestLSP_ServerUnavailable_ReturnsInstallHint(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644))
	m := lsp.NewManager(dir)
	// Configure .go but mark the binary as unavailable.
	cfg := lsp.DefaultServerConfigs()[".go"]
	cfg.Available = func() bool { return false }
	m.Configs = map[string]lsp.ServerConfig{".go": cfg}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	l := &LSP{Manager: m, WorkingDir: dir}
	r := runLSP(t, l, map[string]any{
		"operation": "hover",
		"file":      "x.go",
		"line":      1,
		"column":    1,
	})
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "not installed")
	require.Contains(t, r.Content, "gopls", "should mention which server to install")
}

func TestLSP_RenameRequiresNewName(t *testing.T) {
	// Even before reaching the server, missing new_name should be caught.
	// We need a file that DOES have a configured server so we get past the
	// "no server" early-out into the per-operation switch. Use mock
	// configs so this test doesn't depend on installed binaries.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644))

	m := lsp.NewManager(dir)
	// Empty configs -> ErrNoServer path -> rename hits unavailableResult
	// before validating new_name. Skip this test variant; instead just
	// verify the documented behaviour via a direct args check.
	l := &LSP{Manager: m, WorkingDir: dir}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	_ = l // silence unused; kept for symmetry with other tests

	// Rename's new_name validation lives AFTER ClientFor succeeds, so to
	// exercise it we need a real client. That requires gopls. Skip if
	// not available.
	if _, err := osLookPath("gopls"); err != nil {
		t.Skip("skipping rename arg validation (no gopls available)")
	}
	m2 := lsp.NewManager(dir)
	t.Cleanup(func() { _ = m2.Shutdown(context.Background()) })
	l2 := &LSP{Manager: m2, WorkingDir: dir}
	r := runLSP(t, l2, map[string]any{
		"operation": "rename",
		"file":      "x.go",
		"line":      1,
		"column":    1,
		// no new_name
	})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "new_name is required")
}

func TestLSP_WorkspaceSymbols_RequiresQuery(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{"operation": "workspace_symbols"})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "query is required")
}

func TestLSP_WorkspaceSymbols_NoWarmClient(t *testing.T) {
	l := newTestLSP(t, t.TempDir())
	r := runLSP(t, l, map[string]any{"operation": "workspace_symbols", "query": "Foo"})
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "no LSP server is warm")
}

func TestLSP_NilManager_IsError(t *testing.T) {
	l := &LSP{}
	r := runLSP(t, l, map[string]any{"operation": "hover", "file": "x.go", "line": 1, "column": 1})
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "not configured")
}

func TestLSP_Schema_DescribesAllOps(t *testing.T) {
	l := &LSP{Manager: lsp.NewManager(t.TempDir())}
	schema := string(l.Schema())
	for _, op := range []string{
		"hover", "definition", "references", "rename",
		"completion", "document_symbols", "workspace_symbols",
		"signature_help", "diagnostics",
	} {
		require.Contains(t, schema, op, "schema should advertise %s", op)
	}
}

// --- gopls smoke (skipped when gopls absent) -------------------------------

func TestLSP_Hover_GoplsSmoke(t *testing.T) {
	if _, err := osLookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH")
	}
	dir := t.TempDir()
	// Two files: one defining Hello, one calling it. Hover on the call site
	// should include "Hello" type info.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module gilsmoke\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib.go"),
		[]byte("package gilsmoke\n\n// Hello says hi.\nfunc Hello(name string) string { return \"hi \" + name }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "use.go"),
		[]byte("package gilsmoke\n\nfunc Use() string { return Hello(\"world\") }\n"), 0o644))

	m := lsp.NewManager(dir)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	l := &LSP{Manager: m, WorkingDir: dir}

	// Position the cursor on "Hello" in use.go (line 3, column ~28 after
	// "return " — we land somewhere inside the identifier; gopls doesn't
	// care about exact column as long as we're inside the token).
	r := runLSP(t, l, map[string]any{
		"operation": "hover",
		"file":      "use.go",
		"line":      3,
		"column":    28,
	})
	require.False(t, r.IsError, "got error: %s", r.Content)
	require.Contains(t, r.Content, "Hello")
}

func TestLSP_Definition_GoplsSmoke(t *testing.T) {
	if _, err := osLookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module gilsmoke\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib.go"),
		[]byte("package gilsmoke\n\nfunc Hello() string { return \"hi\" }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "use.go"),
		[]byte("package gilsmoke\n\nfunc Use() string { return Hello() }\n"), 0o644))

	m := lsp.NewManager(dir)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	l := &LSP{Manager: m, WorkingDir: dir}

	// Cursor on Hello in use.go at line 3.
	r := runLSP(t, l, map[string]any{
		"operation": "definition",
		"file":      "use.go",
		"line":      3,
		"column":    28,
	})
	require.False(t, r.IsError, "got error: %s", r.Content)
	require.Contains(t, r.Content, "lib.go", "definition should land in lib.go, got: %s", r.Content)
}
