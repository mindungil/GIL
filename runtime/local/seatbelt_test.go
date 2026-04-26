//go:build darwin

package local

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeatbelt_FullAccess_PassesThrough(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeFullAccess}
	out := s.Wrap("echo", "hi")
	require.Equal(t, []string{"echo", "hi"}, out)
}

func TestSeatbelt_ReadOnly_HasFileReadButNotFileWrite(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeReadOnly}
	out := s.Wrap("echo", "hi")
	require.GreaterOrEqual(t, len(out), 5)
	require.Equal(t, MacOSSandboxExecPath, out[0])
	require.Equal(t, "-p", out[1])
	policy := out[2]
	require.Contains(t, policy, "(deny default)")
	require.Contains(t, policy, "(allow file-read*)")
	require.NotContains(t, policy, "(allow file-write*")
	// Trailing layout: ... "--", "echo", "hi"
	require.Contains(t, out, "--")
}

func TestSeatbelt_WorkspaceWrite_AddsWriteRuleAndParam(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeWorkspaceWrite, WorkspaceDir: "/work"}
	out := s.Wrap("ls", "-la")
	policy := out[2]
	require.Contains(t, policy, "(allow file-write*")
	require.Contains(t, policy, `(subpath (param "WORKSPACE_DIR"))`)
	require.Contains(t, out, "-D")
	// Find -D arg
	var hasD bool
	for i, a := range out {
		if a == "-D" && i+1 < len(out) && out[i+1] == "WORKSPACE_DIR=/work" {
			hasD = true
		}
	}
	require.True(t, hasD)
}

func TestSeatbelt_WorkspaceWrite_EmptyWorkspace_OmitsParam(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeWorkspaceWrite, WorkspaceDir: ""}
	out := s.Wrap("ls")
	for _, a := range out {
		require.NotEqual(t, "-D", a)
	}
}

func TestSeatbelt_BasePolicyContainsCodexLiftedRules(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeReadOnly}
	out := s.Wrap("ls")
	policy := out[2]
	// Sanity that the lift is present
	require.Contains(t, policy, "(allow process-exec)")
	require.Contains(t, policy, "(allow process-fork)")
	require.Contains(t, policy, "(sysctl-name \"hw.ncpu\")")
	require.Contains(t, policy, "/dev/null")
}

func TestSeatbelt_CustomSandboxExecPath(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeReadOnly, SandboxExecPath: "/opt/sandbox-exec"}
	out := s.Wrap("ls")
	require.Equal(t, "/opt/sandbox-exec", out[0])
}

func TestSeatbelt_DoubleDashSeparator(t *testing.T) {
	s := &SeatbeltSandbox{Mode: ModeReadOnly}
	out := s.Wrap("echo", "ok")
	// Find -- and verify everything after is the command
	var sepIdx = -1
	for i, a := range out {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, sepIdx, 0)
	require.Equal(t, "echo", out[sepIdx+1])
	require.Equal(t, "ok", out[sepIdx+2])
}

func TestSeatbeltAvailable_ReturnsBoolNoPanic(t *testing.T) {
	// Just that it doesn't panic. Result depends on the host (should be true on macOS dev machines).
	require.NotPanics(t, func() { _ = SeatbeltAvailable() })
}

// Optional: smoke test that actually invokes /usr/bin/sandbox-exec if present.
// Uses ModeReadOnly + a benign /bin/echo.
func TestSeatbelt_SmokeExec_IfAvailable(t *testing.T) {
	if !SeatbeltAvailable() {
		t.Skip("sandbox-exec not available on this host")
	}
	s := &SeatbeltSandbox{Mode: ModeReadOnly}
	argv := s.Wrap("/bin/echo", "hello")
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stderr: %s", string(out))
	require.Contains(t, string(out), "hello")
}
