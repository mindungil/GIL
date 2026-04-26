package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/jedutools/gil/core/cliutil"
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

	gild, err := exec.LookPath("gild")
	if err != nil {
		return cliutil.Wrap(err,
			"daemon helper 'gild' is not installed",
			`install the gil release bundle, or build with "make build install"`)
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
