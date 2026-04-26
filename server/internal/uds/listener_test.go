package uds

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListener_AcceptsConnection(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	lis, err := Listen(sockPath)
	require.NoError(t, err)
	defer lis.Close()

	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			t.Logf("accept: %v", err)
			return
		}
		connCh <- c
	}()

	c, err := net.Dial("unix", sockPath)
	require.NoError(t, err)
	defer c.Close()

	select {
	case got := <-connCh:
		require.NotNil(t, got)
		got.Close()
	case <-time.After(time.Second):
		t.Fatal("did not accept connection within 1s")
	}
}

func TestListener_SocketMode(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	lis, err := Listen(sockPath)
	require.NoError(t, err)
	defer lis.Close()

	stat, err := os.Stat(sockPath)
	require.NoError(t, err)
	mode := stat.Mode().Perm()
	require.Equal(t, os.FileMode(0o600), mode)
}

func TestListener_RemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	// Create a stale socket file (just a regular file for testing)
	err := os.WriteFile(sockPath, []byte("stale"), 0o644)
	require.NoError(t, err)

	// Listen should remove it and create a new socket
	lis, err := Listen(sockPath)
	require.NoError(t, err)
	defer lis.Close()

	// Verify the socket now exists and has correct mode
	stat, err := os.Stat(sockPath)
	require.NoError(t, err)
	mode := stat.Mode().Perm()
	require.Equal(t, os.FileMode(0o600), mode)
}

func TestListener_FailsIfSocketInUse(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	lis1, err := Listen(sockPath)
	require.NoError(t, err)
	defer lis1.Close()

	// Second Listen on the same path must fail (no silent takeover)
	lis2, err := Listen(sockPath)
	require.Error(t, err)
	require.Nil(t, lis2)
}

func TestRemoveSocket_Idempotent(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	// Removing nonexistent path is OK
	require.NoError(t, RemoveSocket(sockPath))

	// Create a regular file, RemoveSocket removes it
	require.NoError(t, os.WriteFile(sockPath, []byte("x"), 0o644))
	require.NoError(t, RemoveSocket(sockPath))
	_, err := os.Stat(sockPath)
	require.True(t, errors.Is(err, os.ErrNotExist))
}
