package ssh

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Syncer runs rsync over an SSH transport configured via Wrapper.
//
// Push sends LocalDir up to host:RemoteDir; Pull brings host:RemoteDir
// back down to LocalDir. Uses rsync's --delete by default so removals
// on one side propagate.
type Syncer struct {
	Wrapper   *Wrapper // Required; provides Host/Port/KeyPath
	LocalDir  string   // Required; absolute path on this host
	RemoteDir string   // Required; absolute path on the remote
	RsyncBin  string   // defaults to "rsync"
	// ExtraArgs are appended after the standard rsync args. Useful for
	// ["--exclude=.git/", "--exclude=node_modules/"]. Empty by default.
	ExtraArgs []string
}

// SyncAvailable reports whether rsync is in PATH.
func SyncAvailable() bool {
	_, err := exec.LookPath("rsync")
	return err == nil
}

// Push uploads LocalDir to host:RemoteDir. Trailing slashes are added so
// rsync copies CONTENTS (not the directory itself).
func (s *Syncer) Push(ctx context.Context) error {
	return s.run(ctx, s.localTrailing(), s.remoteTrailing())
}

// Pull downloads host:RemoteDir to LocalDir.
func (s *Syncer) Pull(ctx context.Context) error {
	return s.run(ctx, s.remoteTrailing(), s.localTrailing())
}

func (s *Syncer) run(ctx context.Context, src, dst string) error {
	bin := s.RsyncBin
	if bin == "" {
		bin = "rsync"
	}
	if s.Wrapper == nil || s.Wrapper.Host == "" {
		return fmt.Errorf("ssh.Syncer: Wrapper.Host required")
	}
	if s.LocalDir == "" || s.RemoteDir == "" {
		return fmt.Errorf("ssh.Syncer: LocalDir and RemoteDir required")
	}
	args := []string{"-az", "--delete"}
	args = append(args, "-e", s.sshShell())
	args = append(args, s.ExtraArgs...)
	args = append(args, src, dst)

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync %s → %s: %w (output: %s)", src, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sshShell builds the -e value for rsync ("ssh -p PORT -i KEY ...").
func (s *Syncer) sshShell() string {
	var parts []string
	bin := s.Wrapper.SSHBin
	if bin == "" {
		bin = "ssh"
	}
	parts = append(parts, bin)
	if s.Wrapper.KeyPath != "" {
		parts = append(parts, "-i", s.Wrapper.KeyPath)
	}
	if s.Wrapper.Port > 0 {
		parts = append(parts, "-p", strconv.Itoa(s.Wrapper.Port))
	}
	return strings.Join(parts, " ")
}

func (s *Syncer) localTrailing() string {
	if strings.HasSuffix(s.LocalDir, "/") {
		return s.LocalDir
	}
	return s.LocalDir + "/"
}

func (s *Syncer) remoteTrailing() string {
	p := s.RemoteDir
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return s.Wrapper.Host + ":" + p
}
