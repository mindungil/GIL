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

func TestResumeCmd_ShowsGoalPreflight(t *testing.T) {
	t.Setenv("GIL_HOME", t.TempDir())
	sock, cleanup := startGildForTest(t)
	defer cleanup()
	workdir := initGitRepo(t, "feat/resume-preflight", false)

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
			OneLiner:               "resume should remind me of the goal",
			Detailed:               "re-show the goal before restarting the run",
			Tasks:                  []string{"surface the goal", "surface the diff", "resume the run"},
			SuccessCriteriaNatural: []string{"goal visible"},
			NonGoals:               []string{"resume blind"},
		},
	}))
	require.NoError(t, store.Freeze())
	rolloutDir := filepath.Join(filepath.Dir(layout.SessionsDir()), "rollouts")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolloutDir, created.ID+".jsonl"), []byte(`{"id":1,"timestamp":"2025-06-01T12:00:00Z","source":2,"kind":3,"type":"tool_call","data":"{}"}`+"\n"), 0o644))

	var buf bytes.Buffer
	cmd := resumeCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{created.ID, "--socket", sock, "--attach"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "Goal: resume should remind me of the goal")
	require.Contains(t, out, "surface the diff")
	require.Contains(t, out, "Git: feat/resume-preflight · clean")
	require.Contains(t, out, "Latest activity: tool_call")
	require.Contains(t, out, "Workspace diff:")
	require.Contains(t, out, "Status:     done")
}
