package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/paths"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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

func TestSessionList_PlainSkipsMetadataLoads(t *testing.T) {
	clearSessionMetaCache()
	t.Cleanup(clearSessionMetaCache)

	var specCalls int32
	var latestCalls int32
	oldLoadSpec := loadFrozenSpecForSession
	oldLoadLatest := loadLatestEventSummary
	t.Cleanup(func() {
		loadFrozenSpecForSession = oldLoadSpec
		loadLatestEventSummary = oldLoadLatest
	})
	loadFrozenSpecForSession = func(sessionDir string) (*gilv1.FrozenSpec, error) {
		atomic.AddInt32(&specCalls, 1)
		return &gilv1.FrozenSpec{Goal: &gilv1.Goal{OneLiner: "unused"}}, nil
	}
	loadLatestEventSummary = func(path string) (string, time.Time) {
		atomic.AddInt32(&latestCalls, 1)
		return "tool_call", time.Unix(123, 0)
	}

	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	_, err = cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list", "--socket", sock, "--plain"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Equal(t, int32(0), atomic.LoadInt32(&specCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&latestCalls))
}

func TestLastEventSummary_ReadsTailLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var buf bytes.Buffer
	for i := 0; i < 1000; i++ {
		line := map[string]any{
			"type":      "tool_call",
			"timestamp": time.Unix(int64(i), 0).UTC().Format(time.RFC3339Nano),
			"other":     i,
		}
		if i == 999 {
			line["type"] = "run_done"
		}
		body, err := json.Marshal(line)
		require.NoError(t, err)
		buf.Write(body)
		buf.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	typ, ts := lastEventSummary(path)
	require.Equal(t, "run_done", typ)
	require.Equal(t, time.Unix(999, 0).UTC(), ts)
}

func TestCountEvents_PrefersSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("one\n\ntwo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "events.count"), []byte("42\n"), 0o644))

	require.Equal(t, 42, countEvents(path))
}

func TestLastEventSummary_PrefersSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"old","timestamp":"2024-01-01T00:00:00Z"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "events.last"), []byte(`{"type":"fresh","timestamp":"2024-01-02T00:00:00Z"}`+"\n"), 0o644))

	typ, ts := lastEventSummary(path)
	require.Equal(t, "fresh", typ)
	require.Equal(t, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), ts)
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
	workdir := initGitRepo(t, "feat/session-show-git", true)
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: workdir})
	require.NoError(t, err)
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	rolloutDir := filepath.Join(filepath.Dir(layout.SessionsDir()), "rollouts")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolloutDir, created.ID+".jsonl"), []byte(`{"id":1,"timestamp":"2025-06-01T12:00:00Z","source":2,"kind":3,"type":"tool_call","data":"{}"}`+"\n"), 0o644))

	var buf bytes.Buffer
	cmd := sessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"show", created.ID, "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "Working dir")
	require.Contains(t, out, workdir)
	require.Contains(t, out, "Git")
	require.Contains(t, out, "feat/session-show-git · dirty")
	require.Contains(t, out, "Events")
	require.Contains(t, out, "Latest")
}

func TestReadSpecPreview_PrefersSummarySidecar(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	summaryPath := filepath.Join(dir, "spec.summary.json")
	require.NoError(t, os.WriteFile(specPath, []byte(`{"goal":{"one_liner":"full spec"}}`), 0o644))
	require.NoError(t, os.WriteFile(summaryPath, []byte(`{"goal":{"one_liner":"summary spec"}}`), 0o644))

	got := readSpecPreview(specPath)
	require.Contains(t, got, "summary spec")
	require.NotContains(t, got, "full spec")
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
