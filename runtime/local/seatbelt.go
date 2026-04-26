//go:build darwin

package local

import (
	"os/exec"
	"strings"
)

// MacOSSandboxExecPath is the hardcoded path to /usr/bin/sandbox-exec.
// Lifted from Codex (codex-rs/sandboxing/src/seatbelt.rs:28
// MACOS_PATH_TO_SEATBELT_EXECUTABLE): a malicious sandbox-exec earlier on
// PATH would defeat the sandbox, so we only use the system binary at a
// fixed location. If /usr/bin/sandbox-exec is tampered with, the attacker
// already has root.
const MacOSSandboxExecPath = "/usr/bin/sandbox-exec"

// SeatbeltSandbox builds sandbox-exec argument lists. Mirrors the bwrap
// Sandbox API so callers can pick one or the other based on runtime.GOOS.
//
// Three modes:
//
//	ModeReadOnly        - deny-default + allow file-read* on /
//	ModeWorkspaceWrite  - same + allow file-write* on WorkspaceDir
//	ModeFullAccess      - pass-through (no sandbox-exec wrapping)
//
// Policy structure lifted from Codex create_seatbelt_command_args
// (codex-rs/sandboxing/src/seatbelt.rs:561) — base policy + file-read
// section + file-write section + dynamic params via -D<K>=<V>.
type SeatbeltSandbox struct {
	WorkspaceDir    string
	Mode            Mode
	SandboxExecPath string // defaults to MacOSSandboxExecPath
}

// Wrap produces the full command line for executing cmd args... inside
// sandbox-exec under the configured policy. Returns the original command
// unchanged for ModeFullAccess.
func (s *SeatbeltSandbox) Wrap(cmd string, args ...string) []string {
	if s.Mode == ModeFullAccess {
		out := make([]string, 0, 1+len(args))
		return append(append(out, cmd), args...)
	}
	bin := s.SandboxExecPath
	if bin == "" {
		bin = MacOSSandboxExecPath
	}
	policy := s.buildPolicy()
	out := []string{bin, "-p", policy}
	if s.Mode == ModeWorkspaceWrite && s.WorkspaceDir != "" {
		// Pass WORKSPACE_DIR as a -D parameter so the policy can reference
		// (param "WORKSPACE_DIR"). Codex pattern at seatbelt.rs:684-688.
		out = append(out, "-D", "WORKSPACE_DIR="+s.WorkspaceDir)
	}
	out = append(out, "--", cmd)
	out = append(out, args...)
	return out
}

// SeatbeltAvailable reports whether sandbox-exec is callable. macOS only;
// returns false on every other OS via the build-tagged stub file.
func SeatbeltAvailable() bool {
	if _, err := exec.LookPath(MacOSSandboxExecPath); err == nil {
		return true
	}
	// sandbox-exec may live elsewhere on test rigs; defensive PATH lookup
	if _, err := exec.LookPath("sandbox-exec"); err == nil {
		return true
	}
	return false
}

// buildPolicy returns the .sbpl profile string for the configured mode.
// The base policy is a small subset of Codex's seatbelt_base_policy.sbpl
// — we keep only the rules needed for typical agent tools (process exec,
// signals, fork, sysctl-read for stat-like ops). Network is denied
// implicitly by deny-default; we don't open it (Phase 6 scope).
func (s *SeatbeltSandbox) buildPolicy() string {
	var sb strings.Builder
	sb.WriteString(seatbeltBasePolicy)
	sb.WriteString("\n")
	if s.Mode == ModeReadOnly || s.Mode == ModeWorkspaceWrite {
		sb.WriteString("(allow file-read*)\n")
	}
	if s.Mode == ModeWorkspaceWrite && s.WorkspaceDir != "" {
		sb.WriteString(`(allow file-write*
  (subpath (param "WORKSPACE_DIR")))
`)
	}
	return sb.String()
}

// seatbeltBasePolicy is the deny-default base lifted from Codex's
// seatbelt_base_policy.sbpl (first ~30 lines: the universal allow rules
// every agent tool needs). The full Codex policy includes ~100 sysctl-read
// allowlists, network policy, and Chrome-derived hardening; we omit those
// for Phase 6 simplicity. Phase 7 may inline the full policy if needed.
const seatbeltBasePolicy = `(version 1)
; lifted from codex/codex-rs/sandboxing/src/seatbelt_base_policy.sbpl
; deny-default; allow only what the agent needs to run common tools

(deny default)

; child processes inherit the policy
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))

; minimal sysctl-read allowlist (CPU info needed by many tools)
(allow sysctl-read
  (sysctl-name "hw.ncpu")
  (sysctl-name "hw.activecpu")
  (sysctl-name "hw.physicalcpu")
  (sysctl-name "hw.logicalcpu")
  (sysctl-name "hw.machine")
  (sysctl-name "hw.model")
  (sysctl-name "kern.osrelease")
  (sysctl-name "kern.osversion")
  (sysctl-name "kern.argmax"))

; allow writing to /dev/null (common pattern: cmd >/dev/null)
(allow file-write-data
  (require-all
    (path "/dev/null")
    (vnode-type CHARACTER-DEVICE)))

; mach lookup needed for many system services
(allow mach-lookup)
`
