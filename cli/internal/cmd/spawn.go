package cmd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// ensureDaemon checks if the gild daemon socket is responsive. If not,
// it spawns a background gild process and waits up to 5s for the socket
// to appear. Designed to be called by every CLI command before dialing.
func ensureDaemon(socket, base string) error {
	// Already alive?
	if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil
	}

	gild, err := exec.LookPath("gild")
	if err != nil {
		return fmt.Errorf("gild binary not found in PATH (required for auto-spawn): %w", err)
	}
	return ensureDaemonAt(socket, base, gild)
}

// ensureDaemonAt is like ensureDaemon but takes an explicit path to gild.
// Useful for tests.
func ensureDaemonAt(socket, base, gildPath string) error {
	// Already alive?
	if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil
	}

	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("ensureDaemon mkdir base: %w", err)
	}
	cmd := exec.Command(gildPath, "--foreground", "--base", base)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ensureDaemon spawn: %w", err)
	}
	// Detach so the daemon survives this CLI process.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("ensureDaemon release: %w", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("gild did not become ready within 5s after spawn")
}
