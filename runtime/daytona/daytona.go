// Package daytona is the Daytona (https://daytona.io) cloud workspace driver.
//
// Approach: real REST API calls via runtime/daytona.Client. Provider.Provision
// creates a workspace, waits for it to reach "ready", and returns a Sandbox
// whose Wrapper implements core/tool.RemoteExecutor — so the bash tool sends
// a single POST per command and receives stdout/stderr/exit in one round-trip
// (no exec.Cmd, no SSH/CLI shell-out).
//
// Lifecycle:
//
//	Provision(opts)
//	  → Client.CreateWorkspace(name="gil-<sessionID>", image=opts.Image)
//	  → Client.WaitReady(id, 500ms)               // gated by ctx
//	  → returns Sandbox{Wrapper, Teardown}
//	Wrapper.ExecRemote(ctx, "bash", ["-c", cmd])
//	  → Client.Exec(id, cmd, args, "/workspace") → {stdout, stderr, exit}
//	Teardown(ctx)
//	  → Client.Delete(id)                         // 404 is OK
//
// Env vars:
//
//	DAYTONA_API_KEY  — required for Available()
//	DAYTONA_API_BASE — optional; defaults to https://api.daytona.io
//	                   (tests inject httptest.NewServer URLs here)
//
// Reference: https://www.daytona.io/docs/api/
//
// Wrapper.Wrap is preserved (for logs/debugging) but is unreachable on the
// hot path because the bash tool prefers RemoteExecutor when available.
package daytona

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/mindungil/gil/runtime/cloud"
)

const (
	// EnvAPIKey is the env var that gates Available(). Required for Provision.
	EnvAPIKey = "DAYTONA_API_KEY"
	// EnvAPIBase optionally overrides the base URL (used by tests for httptest).
	EnvAPIBase = "DAYTONA_API_BASE"
)

// readyPollInterval is how often Provider.Provision polls GetWorkspace
// while waiting for "ready". Tests can override Provider.PollInterval to
// shrink this (e.g., 5ms) for fast lifecycle checks.
const readyPollInterval = 500 * time.Millisecond

// Provider implements cloud.Provider for Daytona.
//
// APIBase / APIKey override the env-derived defaults; HTTP overrides the
// http.Client used by the underlying REST Client (tests inject one with
// short timeouts pointing at httptest.NewServer).
type Provider struct {
	APIBase      string        // empty → $DAYTONA_API_BASE → DefaultBaseURL
	APIKey       string        // empty → $DAYTONA_API_KEY
	HTTP         *http.Client  // empty → http.Client with 60s timeout
	PollInterval time.Duration // empty → readyPollInterval
}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "daytona" }

// resolveAPIKey returns the explicit key or falls back to env.
func (p *Provider) resolveAPIKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	return os.Getenv(EnvAPIKey)
}

// resolveBase returns the explicit base URL, env override, or default.
func (p *Provider) resolveBase() string {
	if p.APIBase != "" {
		return p.APIBase
	}
	if env := os.Getenv(EnvAPIBase); env != "" {
		return env
	}
	return DefaultBaseURL
}

// Available reports whether the driver has credentials. Cheap — env-only
// check, no network calls. (REST connectivity is verified in Provision.)
func (p *Provider) Available() bool {
	return p.resolveAPIKey() != ""
}

// Provision creates a Daytona workspace, polls until it's ready, and
// returns a Sandbox whose Wrapper routes every bash command through the
// REST Exec endpoint via the RemoteExecutor interface.
func (p *Provider) Provision(ctx context.Context, opts cloud.ProvisionOptions) (*cloud.Sandbox, error) {
	if !p.Available() {
		return nil, fmt.Errorf("daytona: %w (need %s)", cloud.ErrNotConfigured, EnvAPIKey)
	}

	client := NewClient(p.resolveBase(), p.resolveAPIKey(), p.HTTP)
	workspaceName := "gil-" + opts.SessionID

	ws, err := client.CreateWorkspace(ctx, workspaceName, opts.Image)
	if err != nil {
		return nil, fmt.Errorf("daytona: create workspace %s: %w", workspaceName, err)
	}
	if ws.ID == "" {
		// Some servers return name-as-ID, some auto-generate. Fall back to name.
		ws.ID = workspaceName
	}

	// Poll until ready. CreateWorkspace may already return "ready" if the
	// backend warms quickly; WaitReady handles both cases.
	if ws.Status != "ready" && ws.Status != "running" {
		ready, err := client.WaitReady(ctx, ws.ID, p.pollInterval())
		if err != nil {
			// Best-effort cleanup so a half-provisioned workspace doesn't leak.
			_ = client.Delete(context.Background(), ws.ID)
			return nil, fmt.Errorf("daytona: wait ready %s: %w", ws.ID, err)
		}
		ws = ready
	}

	wrapper := &Wrapper{
		Client:        client,
		WorkspaceID:   ws.ID,
		WorkspaceName: workspaceName,
		Cwd:           "/workspace",
	}
	teardown := func(tdCtx context.Context) error {
		if err := client.Delete(tdCtx, ws.ID); err != nil {
			return fmt.Errorf("daytona: delete workspace %s: %w", ws.ID, err)
		}
		return nil
	}
	return &cloud.Sandbox{
		Wrapper:  wrapper,
		Teardown: teardown,
		Info: map[string]string{
			"provider":     "daytona",
			"workspace":    workspaceName,
			"workspace_id": ws.ID,
			"image":        opts.Image,
			"status":       ws.Status,
			"api_base":     client.BaseURL,
		},
	}, nil
}

func (p *Provider) pollInterval() time.Duration {
	if p.PollInterval > 0 {
		return p.PollInterval
	}
	return readyPollInterval
}

// Wrapper routes commands into a Daytona workspace via the REST Exec
// endpoint. Implements both core/tool.CommandWrapper (for legacy/logging)
// and core/tool.RemoteExecutor (for the actual hot path).
type Wrapper struct {
	Client        *Client
	WorkspaceID   string
	WorkspaceName string
	Cwd           string // defaults to "/workspace"
}

// Wrap returns a documentary argv describing what would have been called
// if Daytona had a CLI. Kept so observability/logging continue to work,
// but it is NOT fed into exec.Cmd because the bash tool prefers
// RemoteExecutor (ExecRemote) whenever the wrapper implements it.
func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	out := []string{"daytona", "exec", w.WorkspaceName, "--"}
	out = append(out, cmd)
	out = append(out, args...)
	return out
}

// ExecRemote sends the command to the Daytona workspace via REST and
// returns the full result in one call. This is the hot path used by the
// bash tool when the wrapper satisfies core/tool.RemoteExecutor.
func (w *Wrapper) ExecRemote(ctx context.Context, cmd string, args []string) (string, string, int, error) {
	cwd := w.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	res, err := w.Client.Exec(ctx, w.WorkspaceID, cmd, args, cwd)
	if err != nil {
		return "", "", -1, err
	}
	return res.Stdout, res.Stderr, res.Exit, nil
}
