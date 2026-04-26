// Package daytona is the Daytona (https://daytona.io) cloud workspace driver.
//
// Phase 9 status: SCAFFOLD only. Provision returns ErrNotConfigured unless
// DAYTONA_API_KEY env var is set. Real provisioning requires Daytona's REST
// API client (Phase 10).
//
// Reference: https://www.daytona.io/docs/api/
package daytona

import (
	"context"
	"fmt"
	"os"

	"github.com/jedutools/gil/runtime/cloud"
)

const EnvAPIKey = "DAYTONA_API_KEY"

// Provider implements cloud.Provider for Daytona.
type Provider struct {
	APIBase string // defaults to "https://api.daytona.io" (placeholder; verify the actual URL)
}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "daytona" }

func (p *Provider) Available() bool {
	return os.Getenv(EnvAPIKey) != ""
}

func (p *Provider) Provision(ctx context.Context, opts cloud.ProvisionOptions) (*cloud.Sandbox, error) {
	if !p.Available() {
		return nil, fmt.Errorf("daytona: %w (need %s)", cloud.ErrNotConfigured, EnvAPIKey)
	}
	// Phase 9 stub: return a Wrapper that documents the integration shape.
	// Real Phase 10 impl: POST /workspaces with image+resources, poll for ready,
	// get exec endpoint, build a Wrapper that POSTs to that endpoint.
	workspaceName := "gil-" + opts.SessionID
	wrapper := &Wrapper{
		WorkspaceName: workspaceName,
	}
	teardown := func(ctx context.Context) error {
		// Real impl: DELETE /workspaces/<name>. Stub: nop.
		return nil
	}
	return &cloud.Sandbox{
		Wrapper:  wrapper,
		Teardown: teardown,
		Info: map[string]string{
			"provider":  "daytona",
			"workspace": workspaceName,
			"image":     opts.Image,
			"status":    "stub (Phase 9 scaffold; real provisioning Phase 10)",
		},
	}, nil
}

// Wrapper is a stub that produces a placeholder argv. Real impl will route
// through Daytona's exec API (likely an HTTP call, not exec.Command).
// For Phase 9 we use a `daytona` placeholder binary name.
type Wrapper struct {
	WorkspaceName string
	DaytonaBin    string // defaults to "daytona"
}

func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	bin := w.DaytonaBin
	if bin == "" {
		bin = "daytona"
	}
	out := []string{bin, "exec", w.WorkspaceName, "--"}
	out = append(out, cmd)
	out = append(out, args...)
	return out
}
