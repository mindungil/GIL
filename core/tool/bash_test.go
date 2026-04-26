package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBash_Echo(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	require.NoError(t, err)
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "exit=0")
	require.Contains(t, r.Content, "hi")
}

func TestBash_FailingCommand(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"exit 7"}`))
	require.NoError(t, err)
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "exit=7")
}

func TestBash_Timeout(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir(), Timeout: 100 * time.Millisecond}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"sleep 1"}`))
	require.NoError(t, err)
	// CommandContext kills after timeout; exit code != 0
	require.True(t, r.IsError)
}

func TestBash_EmptyCommand(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":""}`))
	require.NoError(t, err)
	require.True(t, r.IsError)
	require.Contains(t, r.Content, "empty")
}

func TestBash_BadJSON(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	_, err := b.Run(context.Background(), json.RawMessage(`not json`))
	require.Error(t, err)
}

func TestBash_NameAndSchema(t *testing.T) {
	b := &Bash{WorkingDir: "/tmp"}
	require.Equal(t, "bash", b.Name())
	require.NotEmpty(t, b.Description())
	require.Contains(t, string(b.Schema()), "command")
}

func TestBash_TruncatesLargeOutput(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir()}
	// Generate >8KB stdout
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"yes hello | head -c 20000"}`))
	require.NoError(t, err)
	require.Contains(t, r.Content, "(truncated)")
}

func TestBash_NilWrapper_RunsDirectly(t *testing.T) {
	b := &Bash{WorkingDir: t.TempDir(), Wrapper: nil}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	require.NoError(t, err)
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "hello")
}

// fakeWrapper records the Wrap call and replaces the command with /bin/echo.
type fakeWrapper struct {
	called   bool
	lastCmd  string
	lastArgs []string
}

func (f *fakeWrapper) Wrap(cmd string, args ...string) []string {
	f.called = true
	f.lastCmd = cmd
	f.lastArgs = append([]string(nil), args...)
	// Replace the command: just echo the original cmd name so we can verify it.
	return []string{"/bin/echo", "wrapped:", cmd}
}

func TestBash_WrapperIsCalled(t *testing.T) {
	fw := &fakeWrapper{}
	b := &Bash{WorkingDir: t.TempDir(), Wrapper: fw}
	r, err := b.Run(context.Background(), json.RawMessage(`{"command":"unused"}`))
	require.NoError(t, err)
	require.True(t, fw.called, "Wrap should have been called")
	require.Equal(t, "bash", fw.lastCmd, "Wrap should receive 'bash' as cmd")
	require.False(t, r.IsError)
	require.Contains(t, r.Content, "wrapped: bash")
}
