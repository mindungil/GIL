package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRun_PrintsResultFromStub(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := runCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1", "--socket", sock, "--provider", "mock"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "Status:")
	require.Contains(t, output, "Iterations:")
}
