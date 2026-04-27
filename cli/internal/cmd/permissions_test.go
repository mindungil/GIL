package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/permission"
)

// permissionsTestEnv isolates the persistent store under a temp dir and
// returns the resolved store path so tests can pre-populate the file
// before invoking the CLI commands. Setting GIL_HOME makes
// permissionsStorePath() resolve to <gilHome>/state/permissions.toml,
// which is the same shape the daemon uses.
func permissionsTestEnv(t *testing.T) string {
	t.Helper()
	gilHome := t.TempDir()
	t.Setenv("GIL_HOME", gilHome)
	t.Setenv("NO_COLOR", "1")
	statePath := filepath.Join(gilHome, "state")
	require.NoError(t, os.MkdirAll(statePath, 0o700))
	return filepath.Join(statePath, "permissions.toml")
}

// TestPermissionsList_Empty exercises the empty-store branch — we
// expect a friendly hint, not an error.
func TestPermissionsList_Empty(t *testing.T) {
	_ = permissionsTestEnv(t)

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Contains(t, buf.String(), "No persisted rules")
}

// TestPermissionsList_RendersProjects writes two project sections to
// the store and asserts both render with their respective patterns.
func TestPermissionsList_RendersProjects(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/home/user/projects/foo", "always_allow", "git status"))
	require.NoError(t, store.Append("/home/user/projects/foo", "always_allow", "ls *"))
	require.NoError(t, store.Append("/home/user/projects/foo", "always_deny", "rm -rf *"))
	require.NoError(t, store.Append("/home/user/projects/bar", "always_allow", "cat README.md"))

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "/home/user/projects/foo")
	require.Contains(t, out, "/home/user/projects/bar")
	require.Contains(t, out, "git status")
	require.Contains(t, out, "rm -rf *")
	require.Contains(t, out, "cat README.md")
}

// TestPermissionsList_FilterByProject asserts --project narrows to one
// section.
func TestPermissionsList_FilterByProject(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/p/foo", "always_allow", "git status"))
	require.NoError(t, store.Append("/p/bar", "always_allow", "cat README.md"))

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list", "--project", "/p/foo"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	out := buf.String()
	require.Contains(t, out, "/p/foo")
	require.NotContains(t, out, "/p/bar")
}

// TestPermissionsList_JSON checks the structured output shape carries
// every project as expected.
func TestPermissionsList_JSON(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/p/foo", "always_allow", "git status"))

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var got struct {
		Projects []permissionsListRow `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got.Projects, 1)
	require.Equal(t, "/p/foo", got.Projects[0].Project)
	require.Equal(t, []string{"git status"}, got.Projects[0].Allow)
}

// TestPermissionsRemove_Allow asserts removal of an allow rule, and
// that re-running remove for the same pattern surfaces "no such rule".
func TestPermissionsRemove_Allow(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/p/foo", "always_allow", "git status"))

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"remove", "git status", "--allow", "--project", "/p/foo"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Contains(t, buf.String(), "removed")

	// And it's gone.
	rules, err := store.Load("/p/foo")
	require.NoError(t, err)
	require.Empty(t, rules.AlwaysAllow)

	// Idempotent at the store level — but the CLI surfaces an error.
	buf.Reset()
	cmd2 := permissionsCmd()
	cmd2.SetOut(&buf)
	cmd2.SetErr(&buf)
	cmd2.SetArgs([]string{"remove", "git status", "--allow", "--project", "/p/foo"})
	err = cmd2.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no allow rule")
}

// TestPermissionsRemove_RejectsBothListFlags verifies the
// allow-XOR-deny check at the CLI surface.
func TestPermissionsRemove_RejectsBothListFlags(t *testing.T) {
	_ = permissionsTestEnv(t)
	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"remove", "git status", "--allow", "--deny", "--project", "/p/foo"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
}

// TestPermissionsClear removes every rule under the chosen project
// and asserts the store ends up empty.
func TestPermissionsClear(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/p/foo", "always_allow", "git status"))
	require.NoError(t, store.Append("/p/foo", "always_deny", "rm -rf *"))

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"clear", "--project", "/p/foo", "--yes"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Contains(t, buf.String(), "cleared 2 rules")

	rules, err := store.Load("/p/foo")
	require.NoError(t, err)
	require.Empty(t, rules.AlwaysAllow)
	require.Empty(t, rules.AlwaysDeny)
}

// TestPermissionsClear_DenyByDefault asserts the y/N prompt cancels
// when the user does not type "y".
func TestPermissionsClear_DenyByDefault(t *testing.T) {
	storePath := permissionsTestEnv(t)
	store := &permission.PersistentStore{Path: storePath}
	require.NoError(t, store.Append("/p/foo", "always_allow", "git status"))

	var buf bytes.Buffer
	cmd := permissionsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader("\n"))
	cmd.SetArgs([]string{"clear", "--project", "/p/foo"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled")

	rules, err := store.Load("/p/foo")
	require.NoError(t, err)
	require.Equal(t, []string{"git status"}, rules.AlwaysAllow)
}
