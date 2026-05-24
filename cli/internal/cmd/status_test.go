package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

func TestStatus_ListsSessions_Plain(t *testing.T) {
	// The legacy text table is now opt-in via --plain. We assert
	// against it in the plain mode so this test stays a guard for
	// scripts that depend on the exact column order.
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
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock, "--plain"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "CREATED")
	require.Contains(t, out, "ID")
	require.Contains(t, out, "STATUS")
	lines := bytes.Count([]byte(out), []byte("\n"))
	require.GreaterOrEqual(t, lines, 3)
}

// TestStatus_ListsSessions_Visual covers the new default rendering —
// no "CREATED" word (we represent it visually with the idle glyph),
// but the short ULID prefix and the empty bar should appear.
func TestStatus_ListsSessions_Visual(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // strip ANSI so substring asserts are stable
	root := t.TempDir()
	t.Setenv("GIL_HOME", root)
	sock, cleanup := startGildForTest(t)
	defer cleanup()
	workdir := initGitRepo(t, "feat/status-goal", true)

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	var firstID string
	for i := 0; i < 2; i++ {
		wdir := "/x"
		if i == 0 {
			wdir = workdir
		}
		sess, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: wdir})
		require.NoError(t, err)
		if i == 0 {
			firstID = sess.ID
		}
	}
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	require.NotEmpty(t, firstID)
	storeDir := filepath.Join(layout.SessionsDir(), firstID)
	store := specstore.NewStore(storeDir)
	require.NoError(t, os.MkdirAll(storeDir, 0o700))
	require.NoError(t, store.Save(&gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "Implement goal-aware status cards"},
	}))
	rolloutDir := filepath.Join(filepath.Dir(layout.SessionsDir()), "rollouts")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rolloutDir, firstID+".jsonl"), []byte(`{"id":1,"timestamp":"2025-06-01T12:00:00Z","source":2,"kind":3,"type":"tool_call","data":"{}"}`+"\n"), 0o644))

	var buf bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "01test", "expected short ULID prefix")
	require.Contains(t, out, "$0.00", "expected cost column placeholder")
	require.Contains(t, out, "Implement goal-aware status cards")
	require.Contains(t, out, "latest tool_call")
	require.Contains(t, out, "git feat/status-goal · dirty")
	// Two cards = two short-ULID lines.
	require.GreaterOrEqual(t, bytes.Count([]byte(out), []byte("01test")), 2)
}

func TestStatus_JSONOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	root := t.TempDir()
	t.Setenv("GIL_HOME", root)
	sock, cleanup := startGildForTest(t)
	defer cleanup()
	workdir := initGitRepo(t, "feat/status-json", false)

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	sess, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: workdir})
	require.NoError(t, err)
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	storeDir := filepath.Join(layout.SessionsDir(), sess.ID)
	require.NoError(t, os.MkdirAll(storeDir, 0o700))
	require.NoError(t, specstore.NewStore(storeDir).Save(&gilv1.FrozenSpec{
		Goal: &gilv1.Goal{OneLiner: "Implement goal-aware status cards"},
	}))
	rolloutDir := filepath.Join(filepath.Dir(layout.SessionsDir()), "rollouts")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rolloutDir, sess.ID+".jsonl"), []byte(`{"id":1,"timestamp":"2025-06-01T12:00:00Z","source":2,"kind":3,"type":"tool_call","data":"{}"}`+"\n"), 0o644))

	// Drive --output via the package-level flag — the in-process tests
	// instantiate statusCmd() directly rather than going through Root(),
	// so the persistent flag is not auto-registered. Setting the var
	// gives the same effect.
	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	var buf bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var parsed statusJSONReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed), "stdout not JSON: %s", buf.String())
	require.Len(t, parsed.Sessions, 1)
	require.Equal(t, sess.ID, parsed.Sessions[0].ID)
	require.Equal(t, "CREATED", parsed.Sessions[0].Status)
	require.Equal(t, workdir, parsed.Sessions[0].WorkingDir)
	require.Equal(t, "Implement goal-aware status cards", parsed.Sessions[0].FrozenGoal)
	require.Equal(t, "feat/status-json · clean", parsed.Sessions[0].GitSummary)
	require.Equal(t, "tool_call", parsed.Sessions[0].LatestType)
	require.Equal(t, "2025-06-01T12:00:00Z", parsed.Sessions[0].LatestAt)
}

func TestStatus_RejectsNonPositiveLimit(t *testing.T) {
	var buf bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--limit", "0"})
	require.Error(t, cmd.ExecuteContext(context.Background()))

	cmd2 := statusCmd()
	cmd2.SetOut(&buf)
	cmd2.SetErr(&buf)
	cmd2.SetArgs([]string{"--limit", "-5"})
	require.Error(t, cmd2.ExecuteContext(context.Background()))
}

func TestStatus_PlainSkipsMetadataLoads(t *testing.T) {
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
	cmd := statusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock, "--plain"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Equal(t, int32(0), atomic.LoadInt32(&specCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&latestCalls))
}
