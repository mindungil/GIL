// Package modal is the Modal (https://modal.com) cloud VM driver.
//
// Phase 9 status: SCAFFOLD only. Provision returns ErrNotConfigured unless
// MODAL_TOKEN_ID and MODAL_TOKEN_SECRET env vars are set, AND the modal
// CLI is in PATH. Even when configured, the current Provision implementation
// shells out to `modal run` as a placeholder — actual VM provisioning
// requires a Modal app definition (Phase 10).
//
// Reference: https://modal.com/docs/guide/sandbox
package modal

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/jedutools/gil/runtime/cloud"
)

const (
	EnvTokenID     = "MODAL_TOKEN_ID"
	EnvTokenSecret = "MODAL_TOKEN_SECRET"
)

// Provider implements cloud.Provider for Modal.
type Provider struct {
	ModalBin string // defaults to "modal"
}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "modal" }

func (p *Provider) Available() bool {
	if os.Getenv(EnvTokenID) == "" || os.Getenv(EnvTokenSecret) == "" {
		return false
	}
	bin := p.ModalBin
	if bin == "" {
		bin = "modal"
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

func (p *Provider) Provision(ctx context.Context, opts cloud.ProvisionOptions) (*cloud.Sandbox, error) {
	if !p.Available() {
		return nil, fmt.Errorf("modal: %w (need %s, %s, modal CLI)", cloud.ErrNotConfigured, EnvTokenID, EnvTokenSecret)
	}
	// Placeholder: real provisioning requires a Modal Sandbox app definition.
	// For Phase 9 we return a Wrapper that produces the argv that WOULD be
	// run if a real Modal CLI sandbox-exec command existed. This documents
	// the integration shape without actually deploying.
	bin := p.ModalBin
	if bin == "" {
		bin = "modal"
	}
	sandboxName := "gil-" + opts.SessionID
	wrapper := &Wrapper{
		ModalBin:    bin,
		SandboxName: sandboxName,
	}
	teardown := func(ctx context.Context) error {
		// Real impl: `modal sandbox stop <name>`. Stub: just return nil.
		return nil
	}
	return &cloud.Sandbox{
		Wrapper:  wrapper,
		Teardown: teardown,
		Info: map[string]string{
			"provider": "modal",
			"sandbox":  sandboxName,
			"image":    opts.Image,
			"status":   "stub (Phase 9 scaffold; real provisioning Phase 10)",
		},
	}, nil
}

// Wrapper builds modal-exec argv. Stub for Phase 9.
type Wrapper struct {
	ModalBin    string // path to modal CLI
	SandboxName string // gil-<sessionID>
}

func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	bin := w.ModalBin
	if bin == "" {
		bin = "modal"
	}
	// Placeholder for `modal sandbox exec <name> -- cmd args...`.
	// Real Modal CLI subcommand TBD in Phase 10.
	out := []string{bin, "sandbox", "exec", w.SandboxName, "--"}
	out = append(out, cmd)
	out = append(out, args...)
	return out
}
