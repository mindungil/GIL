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
