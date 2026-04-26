// Package local provides OS sandbox helpers for tool execution on the
// machine running gild. Currently implements bubblewrap-based isolation
// for Linux. Other OS (macOS Seatbelt) will land in a separate file.
package local

import "os/exec"

// Mode controls the filesystem access policy of a Sandbox.
type Mode int

const (
	// ModeReadOnly: agent can read everything but write nowhere.
	// Tool calls that try to write fail at the bwrap layer.
	ModeReadOnly Mode = iota
	// ModeWorkspaceWrite: agent can write only inside WorkspaceDir;
	// the rest of the filesystem is read-only. Default for run engine.
	ModeWorkspaceWrite
	// ModeFullAccess: no sandbox; pass-through. Used when the operator
	// has explicitly granted unrestricted access.
	ModeFullAccess
)

// String returns a human-readable name for the Mode.
func (m Mode) String() string {
	switch m {
	case ModeReadOnly:
		return "read_only"
	case ModeWorkspaceWrite:
		return "workspace_write"
	case ModeFullAccess:
		return "full_access"
	default:
		return "unknown"
	}
}

// Sandbox builds bwrap argument lists for wrapping commands.
type Sandbox struct {
	// WorkspaceDir is the directory the agent works in. Required for
	// ModeWorkspaceWrite (writable bind), informational for others.
	WorkspaceDir string
	Mode         Mode
	// BwrapPath is the bwrap binary; defaults to "bwrap" (PATH lookup).
	BwrapPath string
	// ExtraReadOnlyBinds are additional --ro-bind src dst pairs (src=dst when one element).
	ExtraReadOnlyBinds [][2]string
}

// bwrapBin returns the effective bwrap binary path (defaulting to "bwrap").
func (s *Sandbox) bwrapBin() string {
	if s.BwrapPath != "" {
		return s.BwrapPath
	}
	return "bwrap"
}

// Wrap returns the full command line that runs `cmd args...` inside the
// sandbox. For ModeFullAccess returns [cmd, args...] unchanged. For other
// modes returns [bwrap, <bwrap-args>..., "--", cmd, args...].
//
// The returned slice is safe to pass to exec.Command(slice[0], slice[1:]...).
func (s *Sandbox) Wrap(cmd string, args ...string) []string {
	if s.Mode == ModeFullAccess {
		result := make([]string, 1+len(args))
		result[0] = cmd
		copy(result[1:], args)
		return result
	}

	// Base bwrap flags shared by all sandboxed modes.
	bwrapArgs := []string{
		s.bwrapBin(),
		"--unshare-user", "--unshare-pid", "--unshare-uts", "--unshare-ipc",
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}

	// In ModeWorkspaceWrite, add a writable bind for WorkspaceDir if set.
	if s.Mode == ModeWorkspaceWrite && s.WorkspaceDir != "" {
		bwrapArgs = append(bwrapArgs, "--bind", s.WorkspaceDir, s.WorkspaceDir)
	}

	// Append any extra read-only binds.
	for _, pair := range s.ExtraReadOnlyBinds {
		bwrapArgs = append(bwrapArgs, "--ro-bind", pair[0], pair[1])
	}

	// Separator so bwrap stops parsing flags.
	bwrapArgs = append(bwrapArgs, "--")

	// Append the target command and its arguments.
	bwrapArgs = append(bwrapArgs, cmd)
	bwrapArgs = append(bwrapArgs, args...)

	return bwrapArgs
}

// Available returns true if bwrap is callable on this machine. Used by
// callers to fall back to ModeFullAccess gracefully when sandboxing isn't
// installed.
func Available() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}
