package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInterview_RunsAndExitsOnSlashDone(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := interviewCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("I want to build X\n/done\n"))
	cmd.SetArgs([]string{"sess-1", "--socket", sock, "--provider", "mock"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "First message:")
	require.Contains(t, output, "Agent: What do you want?")
}

func TestInterview_ReadsReply(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := interviewCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("initial\nmy reply\n/done\n"))
	cmd.SetArgs([]string{"sess-1", "--socket", sock})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "Agent: got: my reply")
}
