package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

func TestGoalCmd_ShowsFrozenGoalAndLatestActivity(t *testing.T) {
	t.Setenv("GIL_HOME", t.TempDir())
	sock, cleanup := startGildForTest(t)
	defer cleanup()
	workdir := initGitRepo(t, "feat/goal-aware-status", true)

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: workdir})
	require.NoError(t, err)

	layout, err := paths.FromEnv()
	require.NoError(t, err)
	store := specstore.NewStore(filepath.Join(layout.SessionsDir(), created.ID))
	require.NoError(t, store.Save(&gilv1.FrozenSpec{
		Goal: &gilv1.Goal{
			OneLiner:               "add a safer goal command",
			Detailed:               "surface the frozen goal prominently",
			Tasks:                  []string{"show the one-liner", "show tasks", "show latest activity"},
			SuccessCriteriaNatural: []string{"one-liner visible", "tasks visible", "latest visible"},
			NonGoals:               []string{"do not hide the goal behind generic session noise"},
		},
	}))
	require.NoError(t, store.Freeze())

	rolloutDir := filepath.Join(filepath.Dir(layout.SessionsDir()), "rollouts")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolloutDir, created.ID+".jsonl"), []byte(`{"id":1,"timestamp":"2025-06-01T12:00:00Z","source":2,"kind":3,"type":"tool_call","data":"{}"}`+"\n"), 0o644))

	var buf bytes.Buffer
	cmd := goalCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{created.ID, "--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "Goal for "+created.ID)
	require.Contains(t, out, "add a safer goal command")
	require.Contains(t, out, "show the one-liner")
	require.Contains(t, out, "latest visible")
	require.Contains(t, out, "Git: feat/goal-aware-status · dirty")
	require.Contains(t, out, "Latest activity: tool_call")
	require.Contains(t, out, "Workspace diff:")
	require.Contains(t, out, "2 files, +3/-1")
}

func TestGoalCmd_RejectsMissingSpec(t *testing.T) {
	t.Setenv("GIL_HOME", t.TempDir())
	sock, cleanup := startGildForTest(t)
	defer cleanup()
	workdir := initGitRepo(t, "feat/goal-aware-status", false)

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	created, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: workdir})
	require.NoError(t, err)

	var buf bytes.Buffer
	cmd := goalCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{created.ID, "--socket", sock})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "load frozen spec")
}
