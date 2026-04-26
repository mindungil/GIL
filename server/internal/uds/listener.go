package uds

import (
	"errors"
	"net"
	"os"
	"path/filepath"
)

// Listen creates a Unix Domain Socket listener at path with mode 0600.
// The parent directory is created (mode 0755) if missing, and any pre-existing
// socket at path is removed first.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, err
	}
	return lis, nil
}
