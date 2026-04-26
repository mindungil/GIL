package uds

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Listen creates a Unix Domain Socket listener at path with mode 0600.
// The parent directory is created (mode 0755) if missing. If another process
// is already listening on path, Listen returns an error (no silent takeover);
// stale socket files (no listener) are removed automatically.
//
// Mode 0600 is set after net.Listen, leaving a brief TOCTOU window. This is
// acceptable for gil's single-user daemon model.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("uds.Listen mkdir parent: %w", err)
	}

	// If a socket already exists at path AND another process is listening on it,
	// fail loudly instead of silently taking it over. Stale socket files (no
	// listener) are still removed below.
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("socket %q already in use by another process", path)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("uds.Listen remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("uds.Listen bind: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("uds.Listen chmod: %w", err)
	}
	return lis, nil
}

// RemoveSocket removes the socket file at path. Used during daemon shutdown
// to clean up. Missing files are not treated as error.
func RemoveSocket(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("uds.RemoveSocket: %w", err)
	}
	return nil
}
