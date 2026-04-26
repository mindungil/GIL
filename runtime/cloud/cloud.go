// Package cloud defines the shared interface for cloud VM workspace
// backends (Modal, Daytona, future). Concrete drivers live in sibling
// packages (runtime/modal, runtime/daytona). All drivers must:
//
//  1. Gate on credentials (env vars) at Provision time, returning
//     ErrNotConfigured when not available.
//  2. Return a Sandbox whose Wrapper satisfies core/tool.CommandWrapper
//     so the existing Bash tool routes commands transparently.
//  3. Provide a Teardown closure that cleans up any cloud resources.
//
// Phase 9 ships Provider + interface + two stub drivers. Real provisioning
// happens in Phase 10+ when the user supplies credentials.
package cloud

import (
	"context"
	"errors"
)

// ErrNotConfigured indicates the driver's required credentials are missing.
// RunService treats this as a precondition failure (returns FailedPrecondition).
var ErrNotConfigured = errors.New("cloud: driver not configured (missing credentials)")

// CommandWrapper is duplicated from core/tool to avoid a runtime → core
// dependency. core/tool.CommandWrapper is structurally identical; cloud
// drivers' Wrappers naturally satisfy both.
type CommandWrapper interface {
	Wrap(cmd string, args ...string) []string
}

// RemoteExecutor is the structural twin of core/tool.RemoteExecutor —
// also duplicated to avoid runtime → core. HTTP-bound drivers (Daytona)
// implement this in addition to CommandWrapper so the bash tool can
// bypass exec.Cmd and use a single REST round-trip per command.
type RemoteExecutor interface {
	ExecRemote(ctx context.Context, cmd string, args []string) (stdout, stderr string, exit int, err error)
}

// ProvisionOptions configures a Provision call.
type ProvisionOptions struct {
	Image        string // language-stack image, e.g., "python:3.12-slim"
	WorkspaceDir string // local dir to mount/sync (provider-specific semantics)
	Memory       string // e.g., "4Gi"
	CPU          string // e.g., "2"
	SessionID    string // gil session ID; used as a stable resource name suffix
}

// Sandbox is what Provision returns: a wrapper that routes commands into
// the cloud VM, plus a Teardown to call when the run ends.
type Sandbox struct {
	Wrapper  CommandWrapper
	Teardown func(context.Context) error
	Info     map[string]string // provider-specific (vm_id, region, dashboard URL, etc.)
}

// Provider is implemented by each cloud driver.
type Provider interface {
	Name() string
	// Available reports whether the driver's credentials are present.
	// Cheap (env var check), no network calls.
	Available() bool
	// Provision creates a fresh sandbox VM. May make network calls; respect
	// the context for cancellation/timeout.
	Provision(ctx context.Context, opts ProvisionOptions) (*Sandbox, error)
}
