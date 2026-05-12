package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/mcpregistry"
)

// runMCPCmd executes a `gil mcp ...` command in-process and returns the
// combined stdout/stderr buffers and any error. We rebuild the cobra tree
// per invocation so flag state from one call does not leak into the next
// (Cobra mutates its receiver during parsing).
//
// Tests set GIL_HOME via t.Setenv before calling so the global registry
// path lands in the per-test tmpdir; defaultLayout() honours that override.
func runMCPCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := mcpCmd()
	root.SetArgs(args)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err = root.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

// withGilHome points $GIL_HOME at a fresh tmpdir for the duration of the
// test, returning the resulting global mcp.toml path so tests can read the
// file directly when they need to assert on-disk shape.
func withGilHome(t *testing.T) (gilHome, globalMCP string) {
	t.Helper()
	gilHome = t.TempDir()
	t.Setenv("GIL_HOME", gilHome)
	globalMCP = filepath.Join(gilHome, "config", "mcp.toml")
	return gilHome, globalMCP
}

// withCwd swaps the process cwd for the duration of the test, restoring
// the original on cleanup. Required because newRegistry(cwd) inspects the
// current directory to resolve the project scope, and the test runner's
// own cwd is rarely a writable workspace.
func withCwd(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestMCPAdd_StdioGlobal_ListShowsIt(t *testing.T) {
	_, mcpPath := withGilHome(t)
	withCwd(t, t.TempDir())

	stdout, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--", "echo", "hi")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(stdout, `Added MCP server "fs"`) {
		t.Errorf("unexpected stdout: %q", stdout)
	}

	if _, err := os.Stat(mcpPath); err != nil {
		t.Fatalf("expected %s to exist: %v", mcpPath, err)
	}

	listOut, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listOut, "fs") || !strings.Contains(listOut, "stdio") {
		t.Errorf("list missing fs/stdio: %q", listOut)
	}
	if !strings.Contains(listOut, "echo hi") {
		t.Errorf("expected command in target column: %q", listOut)
	}
	if !strings.Contains(listOut, "global") {
		t.Errorf("expected scope=global in list: %q", listOut)
	}
}

func TestMCPAdd_HTTPWithBearer_MaskedInList(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	_, _, err := runMCPCmd(t,
		"add", "issues",
		"--type", "http",
		"--url", "https://issues.example.com/mcp",
		"--bearer", "super-secret-token",
		"--description", "issue tracker")
	if err != nil {
		t.Fatalf("add http: %v", err)
	}

	listOut, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listOut, "issues") {
		t.Fatalf("expected issues row: %q", listOut)
	}
	if strings.Contains(listOut, "super-secret-token") {
		t.Errorf("bearer leaked into list output: %q", listOut)
	}
	if !strings.Contains(listOut, "(+bearer)") {
		t.Errorf("expected (+bearer) marker on http row: %q", listOut)
	}
	if !strings.Contains(listOut, "issue tracker") {
		t.Errorf("expected description in list: %q", listOut)
	}
}

func TestMCPRemove_OmitsAfterRemoval(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	if _, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--", "echo", "hi"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, _, err := runMCPCmd(t, "remove", "fs"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	listOut, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(listOut, "fs ") || strings.Contains(listOut, "\tfs\t") {
		t.Errorf("fs should be gone from list: %q", listOut)
	}
}

func TestMCPAdd_ProjectScope_RequiresGilDir(t *testing.T) {
	withGilHome(t)
	// A workspace WITHOUT .gil/ — Discover will return this dir but
	// IsConfigured returns false.
	ws := t.TempDir()
	withCwd(t, ws)

	_, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--project", "--", "echo", "hi")
	if err == nil {
		t.Fatal("expected error when .gil/ missing")
	}
	if !strings.Contains(err.Error(), ".gil") {
		t.Errorf("expected .gil hint in error, got: %v", err)
	}
}

func TestMCPAdd_ProjectScope_WorksWithGilDir(t *testing.T) {
	withGilHome(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".gil"), 0o700); err != nil {
		t.Fatal(err)
	}
	withCwd(t, ws)

	_, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--project", "--", "echo", "hi")
	if err != nil {
		t.Fatalf("add --project: %v", err)
	}
	listOut, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listOut, "project") {
		t.Errorf("expected scope=project in list: %q", listOut)
	}
}

func TestMCPAdd_ConflictingNamesSameScope(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	if _, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--", "echo", "hi"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--", "echo", "hi-again")
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	// UserError.Error() returns Msg only; the underlying registry error
	// (which carries "already exists") is reachable via Unwrap.
	if !strings.Contains(err.Error(), "could not add") {
		t.Errorf("expected 'could not add' in error, got: %v", err)
	}
	var inner = err
	for inner != nil {
		if strings.Contains(inner.Error(), "already exists") {
			break
		}
		u, ok := inner.(interface{ Unwrap() error })
		if !ok {
			t.Errorf("no 'already exists' anywhere in error chain: %v", err)
			break
		}
		inner = u.Unwrap()
	}
}

func TestMCPAdd_HTTPMissingURL(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	_, _, err := runMCPCmd(t, "add", "issues", "--type", "http", "--bearer", "tok")
	if err == nil {
		t.Fatal("expected error when --url missing for http")
	}
}

func TestMCPAdd_StdioMissingCommand(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	_, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio")
	if err == nil {
		t.Fatal("expected error when stdio command missing")
	}
}

func TestMCPRemove_NotFound(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())
	_, _, err := runMCPCmd(t, "remove", "ghost")
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestMCPList_Empty(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())
	stdout, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "No MCP servers configured") {
		t.Errorf("expected empty hint, got: %q", stdout)
	}
}

func TestMCPLogin_StubMessage(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())
	stdout, _, err := runMCPCmd(t, "login", "fs")
	if err != nil {
		t.Fatalf("login stub: %v", err)
	}
	if !strings.Contains(stdout, "Phase 13") {
		t.Errorf("expected Phase 13 hint, got: %q", stdout)
	}
	stdout2, _, err := runMCPCmd(t, "logout", "fs")
	if err != nil {
		t.Fatalf("logout stub: %v", err)
	}
	if !strings.Contains(stdout2, "Phase 13") {
		t.Errorf("expected Phase 13 hint, got: %q", stdout2)
	}
}

func TestMCPList_JSONOutput(t *testing.T) {
	withGilHome(t)
	withCwd(t, t.TempDir())

	// Add one stdio + one http with bearer to exercise both shapes.
	if _, _, err := runMCPCmd(t, "add", "fs", "--type", "stdio", "--", "echo", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runMCPCmd(t,
		"add", "issues",
		"--type", "http",
		"--url", "https://issues.example.com/mcp",
		"--bearer", "super-secret",
	); err != nil {
		t.Fatal(err)
	}

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	stdout, _, err := runMCPCmd(t, "list")
	if err != nil {
		t.Fatalf("list --output json: %v", err)
	}
	var parsed mcpListJSON
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if len(parsed.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(parsed.Servers))
	}
	if parsed.GlobalPath == "" {
		t.Errorf("expected global_path to be populated")
	}
	if strings.Contains(stdout, "super-secret") {
		t.Errorf("bearer token leaked into JSON output: %s", stdout)
	}
	var stdio, http *mcpServerJSON
	for i := range parsed.Servers {
		switch parsed.Servers[i].Name {
		case "fs":
			stdio = &parsed.Servers[i]
		case "issues":
			http = &parsed.Servers[i]
		}
	}
	if stdio == nil || stdio.Type != "stdio" || stdio.Command != "echo" {
		t.Errorf("missing/incorrect stdio entry: %+v", stdio)
	}
	if http == nil || http.Type != "http" || !http.HasBearer {
		t.Errorf("missing/incorrect http entry: %+v", http)
	}
}

// TestMCPRoundtrip_DiskShape verifies the on-disk TOML keeps the bearer
// token (chmod 0600 enforcement is already covered by mcpregistry tests).
func TestMCPRoundtrip_DiskShape(t *testing.T) {
	_, mcpPath := withGilHome(t)
	withCwd(t, t.TempDir())

	if _, _, err := runMCPCmd(t,
		"add", "issues",
		"--type", "http",
		"--url", "https://example.com/mcp",
		"--bearer", "tok-1234"); err != nil {
		t.Fatalf("add: %v", err)
	}

	reg := &mcpregistry.Registry{GlobalPath: mcpPath}
	got, err := reg.LoadScope(mcpregistry.ScopeGlobal)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	srv, ok := got["issues"]
	if !ok {
		t.Fatalf("missing entry in %v", got)
	}
	if srv.Auth != "bearer:tok-1234" {
		t.Errorf("auth not persisted as bearer:tok-1234, got %q", srv.Auth)
	}
}
