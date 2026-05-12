package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mindungil/gil/core/cliutil"
)

// ensureDaemon checks if the gild daemon socket is responsive. If not,
// it spawns a background gild process and waits up to 5s for the socket
// to appear. Designed to be called by every CLI command before dialing.
//
// The base argument is the directory we will mkdir on behalf of gild
// before exec'ing it. With the new XDG layout this is normally the
// State dir (where the socket lives); under GIL_HOME it is the
// single-tree root. Either way ensureDaemon does not pass --base /
// --home to the spawned gild — gild re-derives its own layout from
// GIL_HOME/XDG so both processes stay in sync without duplicating the
// resolution logic.
func ensureDaemon(socket, base string) error {
	// Already alive?
	if c, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil
	}

	gild, err := lookupGild()
	if err != nil {
		return cliutil.Wrap(err,
			"daemon helper 'gild' is not installed",
			`install the gil release bundle, or build with "make build install"`)
	}
	return ensureDaemonAt(socket, base, gild)
}

// lookupGild resolves the gild binary path. Tries PATH first, then falls
// back to <cwd>/bin/gild and <gil-binary-dir>/gild for dev-mode (built but
// not installed). Mirrors the same fallback used by `gil doctor`.
func lookupGild() (string, error) {
	if p, err := exec.LookPath("gild"); err == nil {
		return p, nil
	}
	// dev fallback 1: <cwd>/bin/gild (when running from repo root)
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "bin", "gild")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	// dev fallback 2: sibling of the running gil binary (e.g. ./bin/gild next to ./bin/gil)
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "gild")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("gild not found on PATH, in ./bin, or beside the gil binary")
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
		return cliutil.Wrap(err,
			fmt.Sprintf("cannot prepare gil data directory %q", base),
			`check filesystem permissions, or set --base to a writable path`)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("ensureDaemon open /dev/null: %w", err)
	}
	defer devnull.Close()
	// We deliberately do not forward --base / --home: gild derives its
	// layout the same way the CLI does (GIL_HOME ?? XDG defaults), so
	// passing the env through (which exec inherits by default) is
	// enough to keep the two processes pointing at the same socket.
	cmd := exec.Command(gildPath, "--foreground")
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		return cliutil.Wrap(err,
			"could not start the gild daemon",
			`check that "gild" is on PATH and executable`)
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
	return cliutil.New(
		"daemon not running",
		`start it manually with "gild --foreground &", then retry`)
}
