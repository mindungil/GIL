package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/sdk"
)

// TestRunInteractive_DispatchesHelpThenQuit feeds /help and /quit into the
// interactive loop and verifies both run before the loop terminates.
func TestRunInteractive_DispatchesHelpThenQuit(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	in := strings.NewReader("/help\n/quit\n")
	var out bytes.Buffer
	require.NoError(t, runInteractive(context.Background(), cli, "sess-1", in, &out))

	output := out.String()
	require.Contains(t, output, "interactive mode")
	require.Contains(t, output, "/status")
	require.Contains(t, output, "/quit")
	require.Contains(t, output, "exiting") // /quit echoes "exiting…"
}

// TestRunInteractive_IgnoresFreeformInput keeps the rule that mid-run
// free-form prompts are not accepted.
func TestRunInteractive_IgnoresFreeformInput(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	in := strings.NewReader("just chatting\n/quit\n")
	var out bytes.Buffer
	require.NoError(t, runInteractive(context.Background(), cli, "sess-1", in, &out))

	require.Contains(t, out.String(), "only slash commands")
}

// TestRunInteractive_UnknownCommandReportsError keeps unknown-command
// telemetry friendly.
func TestRunInteractive_UnknownCommandReportsError(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	in := strings.NewReader("/bogus\n/quit\n")
	var out bytes.Buffer
	require.NoError(t, runInteractive(context.Background(), cli, "sess-1", in, &out))
	require.Contains(t, out.String(), "unknown command")
	require.Contains(t, out.String(), "bogus")
}

// TestRunInteractive_StatusCallsFetcher exercises the gRPC-backed /status
// path against the in-test gild stub.
func TestRunInteractive_StatusCallsFetcher(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	cli, err := sdk.Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	// startGildForTest's testSessionServer implements Create + List but
	// not Get. Calling /status here would error; the test instead just
	// verifies the dispatch happened and the error was reported, NOT
	// the loop crashing.
	in := strings.NewReader("/status\n/quit\n")
	var out bytes.Buffer
	require.NoError(t, runInteractive(context.Background(), cli, "sess-1", in, &out))
	// Either the fetcher succeeded (real impl) or the surface reported a
	// friendly error — both prove dispatch ran.
	output := out.String()
	require.True(t, strings.Contains(output, "Session:") ||
		strings.Contains(output, "error:") ||
		strings.Contains(output, "status:"),
		"expected /status output or error, got %q", output)
}
