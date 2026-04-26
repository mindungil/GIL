package cmd

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureDaemon_NoOpIfSocketAlive(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "gild.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer l.Close()

	// gildPath는 더미 — ensureDaemonAt이 socket 살아있다 판단하면 spawn 안 함
	require.NoError(t, ensureDaemonAt(sock, dir, "/nonexistent-gild"))
}

func TestEnsureDaemon_FailsIfGildMissing(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "gild.sock")
	err := ensureDaemonAt(sock, dir, "/nonexistent-gild-binary")
	require.Error(t, err)
}
