package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvents_TailReportsUnimplemented(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := eventsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1", "--socket", sock, "--tail"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "Phase 5")
}

func TestEvents_NoTailFlag_Errors(t *testing.T) {
	var out bytes.Buffer
	cmd := eventsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--tail")
}
