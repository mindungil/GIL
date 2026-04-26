package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResume_PrintsResumedAgentTurn(t *testing.T) {
	// Use the existing test stub which has Start emitting stage + agent turn.
	// For this test the inline testInterviewServer's Start is the same as resume
	// (it doesn't differentiate by FirstInput).
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := resumeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1", "--socket", sock, "--provider", "mock"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.True(t, strings.Contains(output, "Agent:") || strings.Contains(output, "stage"),
		"expected stage or agent turn in output, got: %s", output)
}
