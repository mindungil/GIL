// Package ssh provides a CommandWrapper that routes commands to a remote
// host via OpenSSH. Mirrors runtime/local.Sandbox and runtime/docker.Wrapper
// so core/tool.Bash can use any of them based on spec.workspace.backend.
//
// File ops are NOT routed (Phase 8 limitation): write_file and read_file
// continue to operate on the LOCAL workspace dir. Remote file synchronization
// (rsync-based) is deferred to Phase 9. For Phase 8, treat SSH as "the
// agent's terminal commands run remotely; its file edits live locally and
// the user is responsible for sync."
package ssh

import (
	"os/exec"
	"strconv"
	"strings"
)

// Wrapper builds `ssh` argument lists.
type Wrapper struct {
	Host    string  // user@host (required for Wrap to do anything meaningful)
	Port    int     // optional; emits -p <port> when > 0
	KeyPath string  // optional; emits -i <path> when non-empty
	SSHBin  string  // defaults to "ssh"
	// ExtraArgs are appended after -i/-p but before the host. Useful for
	// "-o StrictHostKeyChecking=no" etc. Caller-supplied; gil doesn't add any.
	ExtraArgs []string
}

// Wrap returns the argv that runs `cmd args...` on the remote host. When
// Host is empty, returns the command unchanged (passthrough; useful for
// graceful degradation in tests).
//
// Layout: [ssh [-i key] [-p port] [extra...] host cmd args...]
//
// Note: cmd and args are joined into a single shell-string by ssh. We
// quote each arg defensively to handle spaces/quotes. This is suitable
// for the controlled set of commands gil's Bash tool produces (the agent
// already wraps user input via "bash -c <command>"; we wrap that ENTIRE
// string as one quoted arg).
func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	if w.Host == "" {
		out := make([]string, 0, 1+len(args))
		return append(append(out, cmd), args...)
	}
	bin := w.SSHBin
	if bin == "" {
		bin = "ssh"
	}
	out := []string{bin}
	if w.KeyPath != "" {
		out = append(out, "-i", w.KeyPath)
	}
	if w.Port > 0 {
		out = append(out, "-p", strconv.Itoa(w.Port))
	}
	out = append(out, w.ExtraArgs...)
	out = append(out, w.Host)
	// Combine cmd + args into one quoted shell string for ssh
	out = append(out, shellJoin(append([]string{cmd}, args...)))
	return out
}

// shellJoin joins parts as a single POSIX shell command string. Each part
// is single-quoted (with embedded ' escaped) so the remote shell sees them
// as literal arguments.
func shellJoin(parts []string) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(quoteShell(p))
	}
	return sb.String()
}

func quoteShell(s string) string {
	if s == "" {
		return "''"
	}
	// If safe (alphanumeric + a few common chars), no quoting
	if safeShell(s) {
		return s
	}
	// Otherwise, wrap in single quotes and escape embedded ' as '\''
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func safeShell(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=':
		default:
			return false
		}
	}
	return true
}

// Available reports whether the ssh binary is callable.
func Available() bool {
	_, err := exec.LookPath("ssh")
	return err == nil
}

// ParseTarget parses a spec.workspace.path SSH target spec into the
// Wrapper fields. Accepts:
//
//	user@host
//	user@host:port
//	user@host/key/path
//	user@host:port/key/path
//
// Returns Host, Port (0 if unspecified), KeyPath (empty if unspecified).
// Defensive: on any parse failure, returns the entire input as Host with
// Port=0 and KeyPath="" so the caller still gets something usable.
func ParseTarget(spec string) (host string, port int, keyPath string) {
	rest := spec
	// Detect / for keypath split — but only if the / appears AFTER the host
	// section. Find the first '@' to anchor.
	atIdx := strings.IndexByte(rest, '@')
	if atIdx < 0 {
		// No user@ prefix — treat as just hostname
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx > 0 {
			keyPath = rest[slashIdx:]
			rest = rest[:slashIdx]
		}
		if colonIdx := strings.LastIndexByte(rest, ':'); colonIdx > 0 {
			if p, err := strconv.Atoi(rest[colonIdx+1:]); err == nil {
				port = p
				rest = rest[:colonIdx]
			}
		}
		host = rest
		return
	}
	// Has user@ — find next slash (which would start keypath)
	afterAt := rest[atIdx+1:]
	if slashIdx := strings.IndexByte(afterAt, '/'); slashIdx >= 0 {
		keyPath = afterAt[slashIdx:]
		rest = rest[:atIdx+1] + afterAt[:slashIdx]
	}
	// Now find :port (if any)
	if colonIdx := strings.LastIndexByte(rest, ':'); colonIdx > atIdx {
		if p, err := strconv.Atoi(rest[colonIdx+1:]); err == nil {
			port = p
			rest = rest[:colonIdx]
		}
	}
	host = rest
	return
}
