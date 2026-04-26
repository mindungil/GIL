package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpec_PrintsJSON(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	cmd := specCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1", "--socket", sock})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "test-spec-id")
}

func TestSpecFreeze_PrintsHash(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var out bytes.Buffer
	// Note: specFreezeCmd is a subcommand of specCmd. Direct test:
	cmd := specFreezeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"sess-1", "--socket", sock})

	require.NoError(t, cmd.ExecuteContext(context.Background()))

	output := out.String()
	require.Contains(t, output, "Frozen:")
	require.Contains(t, output, "test-spec-id")
}
