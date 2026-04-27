package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

// TestSessionList covers the list verb in its three rendering modes —
// default visual, --plain table, and --output json. It re-uses the
// in-process session server harness from new_test.go.
func TestSessionList_Visual(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	for i := 0; i < 2; i++ {
		_, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list", "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "01test", "expected short ULID prefix")
	require.Contains(t, out, "$0.00")
}

func TestSessionList_JSON(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	_, err = cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
	require.NoError(t, err)

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list", "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var parsed statusJSONReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
	require.Len(t, parsed.Sessions, 1)
}

// TestSessionRm_SingleID verifies the happy path for removing one
// session by id with --yes (no confirm prompt).
func TestSessionRm_SingleID(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"rm", created.ID, "--socket", sock, "--yes"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "removed session")
	// And the session is gone server-side.
	_, err = cli.GetSession(context.Background(), created.ID)
	require.Error(t, err)
}

// TestSessionRm_NotFound exercises the "fake-id" smoke case in the
// task spec: the CLI should exit non-zero and surface a NOT_FOUND
// shaped error message rather than panicking.
func TestSessionRm_NotFound(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"rm", "nope", "--socket", sock, "--yes"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestSessionRm_ConfirmDenyByDefault verifies that without --yes and
// no "y" on stdin, the operation is cancelled (the session survives).
func TestSessionRm_ConfirmDenyByDefault(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader("\n")) // empty answer = cancel
	cmd.SetArgs([]string{"rm", created.ID, "--socket", sock})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled")

	// Survives.
	_, err = cli.GetSession(context.Background(), created.ID)
	require.NoError(t, err)
}

// TestSessionRm_ConfirmAcceptsYes verifies the prompt accepts "y\n"
// and proceeds with deletion.
func TestSessionRm_ConfirmAcceptsYes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetArgs([]string{"rm", created.ID, "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	_, err = cli.GetSession(context.Background(), created.ID)
	require.Error(t, err)
}

// TestSessionRm_AllRequiresConfirm verifies --all with --yes deletes
// every session (and prints the batch summary line).
func TestSessionRm_All(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	for i := 0; i < 3; i++ {
		_, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"rm", "--all", "--socket", sock, "--yes"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Contains(t, buf.String(), "removed 3 sessions")

	list, err := cli.ListSessions(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, list)
}

// TestSessionRm_RejectsMixedFlags verifies the mutual-exclusion check
// at the CLI surface — passing both --status and --all should fail
// before any RPC is issued.
func TestSessionRm_RejectsMixedFlags(t *testing.T) {
	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"rm", "--status", "DONE", "--all", "--yes"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
}

// TestSessionRm_NoTargets verifies that calling rm with no positional
// arg and no filter flags errors out with a hint.
func TestSessionRm_NoTargets(t *testing.T) {
	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"rm"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no targets")
}

// TestSessionShow renders one session and asserts the metadata column
// includes the working dir and event count.
func TestSessionShow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/abs/wd"})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"show", created.ID, "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "Working dir")
	require.Contains(t, out, "/abs/wd")
	require.Contains(t, out, "Events")
}

// TestParseAge covers the day/hour suffix shortcuts plus an obvious
// invalid case.
func TestParseAge(t *testing.T) {
	d, err := parseAge("7d")
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, d)
	d, err = parseAge("24h")
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, d)
	_, err = parseAge("7")
	require.Error(t, err) // no unit
}

// TestFilterSessionsForRm exercises status / older-than / all branches
// of the filter helper directly so we do not need a populated daemon.
// We construct sessions with synthetic UpdatedAt and an events.jsonl
// file under a t.TempDir to drive the mtime side of older-than.
func TestFilterSessionsForRm(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "ses-old", "events"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ses-old", "events", "events.jsonl"),
		[]byte(`{"id":1}`+"\n"), 0o644))
	old := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "ses-old", "events", "events.jsonl"), old, old))

	list := []*sdk.Session{
		{ID: "ses-old", Status: "DONE", UpdatedAt: old},
		{ID: "ses-new", Status: "DONE", UpdatedAt: time.Now()},
		{ID: "ses-stuck", Status: "STUCK", UpdatedAt: time.Now()},
	}

	got := filterSessionsForRm(list, "DONE", "", false, dir)
	require.Len(t, got, 2)

	got = filterSessionsForRm(list, "", "7d", false, dir)
	require.Len(t, got, 1)
	require.Equal(t, "ses-old", got[0].ID)

	got = filterSessionsForRm(list, "", "", true, dir)
	require.Len(t, got, 3)
}

// TestHumanBytes covers the three thresholds.
func TestHumanBytes(t *testing.T) {
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "1.0 KB", humanBytes(1024))
	require.Equal(t, "1.0 MB", humanBytes(1024*1024))
}
