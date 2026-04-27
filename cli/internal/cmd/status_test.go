package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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
	cmd.SetArgs([]string{"--socket", sock})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "01test", "expected short ULID prefix")
	require.Contains(t, out, "$0.00", "expected cost column placeholder")
	// Two cards = two short-ULID lines.
	require.GreaterOrEqual(t, bytes.Count([]byte(out), []byte("01test")), 2)
}

func TestStatus_JSONOutput(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()
	for i := 0; i < 2; i++ {
		_, err := cli.CreateSession(context.Background(), sdk.CreateOptions{WorkingDir: "/x"})
		require.NoError(t, err)
	}

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
	require.Len(t, parsed.Sessions, 2)
	for _, s := range parsed.Sessions {
		require.NotEmpty(t, s.ID)
		require.Equal(t, "CREATED", s.Status)
		require.Equal(t, "/x", s.WorkingDir)
	}
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
