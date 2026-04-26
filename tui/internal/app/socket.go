package app

import (
	"os"
	"path/filepath"
)

// DefaultSocket returns the default UDS path used by gild.
func DefaultSocket() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp/gil/gild.sock"
	}
	return filepath.Join(home, ".gil", "gild.sock")
}
