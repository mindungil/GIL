//go:build !darwin

package local

// SeatbeltSandbox is a no-op stub on non-Darwin platforms so cross-platform
// code can reference the type without build failures. Wrap returns the
// command unchanged. The Mode field is honored only for type compatibility.
type SeatbeltSandbox struct {
	WorkspaceDir    string
	Mode            Mode
	SandboxExecPath string
}

// Wrap on non-Darwin returns the command unchanged (no sandboxing).
func (s *SeatbeltSandbox) Wrap(cmd string, args ...string) []string {
	out := make([]string, 0, 1+len(args))
	return append(append(out, cmd), args...)
}

// SeatbeltAvailable always returns false on non-Darwin platforms.
func SeatbeltAvailable() bool { return false }
