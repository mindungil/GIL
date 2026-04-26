package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/sdk"
)

func TestStatus_ListsSessions(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	// Pre-create 2 sessions
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
	cmd.SetArgs([]string{"--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "CREATED")
	// 2 lines + header = at least 3 lines
	lines := bytes.Count([]byte(out), []byte("\n"))
	require.GreaterOrEqual(t, lines, 3)
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
