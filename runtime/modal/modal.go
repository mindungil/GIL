// Package modal is the Modal (https://modal.com) cloud sandbox driver.
//
// Approach: shell out to the `modal` CLI against an ephemeral Python
// manifest that we generate per session (see client.go). Modal exposes its
// sandbox API only through the Python SDK; there is no Go client. Driving
// it through `modal run <script>::<func>` keeps gil pure-Go and lets us
// verify the integration shape under a fake CLI binary in tests.
//
// Lifecycle:
//
//	Provision(opts)
//	  → AppName    = gil-<sessionID>
//	  → ManifestPath = $TMPDIR/gil-modal-<sessionID>.py   (written to disk)
//	  → returns Sandbox{Wrapper, Teardown}
//	Wrapper.Wrap(cmd, args)
//	  → [modal, run, <manifest>::exec_in_sandbox, --cmd, <cmd>, --args, <json>]
//	Teardown(ctx)
//	  → exec [modal, app, stop, gil-<sessionID>]; then rm manifest
//
// Env vars:
//
//	MODAL_TOKEN_ID, MODAL_TOKEN_SECRET — required for Available()
//	MODAL_BIN                          — optional override; default "modal"
//
// Reference:
//   - https://modal.com/docs/guide/sandbox
//   - https://modal.com/docs/reference/cli/run
//   - https://modal.com/docs/reference/cli/app   (app stop)
package modal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mindungil/gil/runtime/cloud"
)

const (
	EnvTokenID     = "MODAL_TOKEN_ID"
	EnvTokenSecret = "MODAL_TOKEN_SECRET"
	// EnvBin overrides the path to the modal CLI. Tests inject a fake
	// shell script via this env var so they can capture argv without
	// requiring real Modal credentials.
	EnvBin = "MODAL_BIN"
)

// Provider implements cloud.Provider for Modal.
type Provider struct {
	// ModalBin overrides the modal binary path. Empty → resolveBin() consults
	// $MODAL_BIN, then falls back to "modal" on $PATH.
	ModalBin string
}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "modal" }

// resolveBin picks the modal binary in priority order:
//
//	p.ModalBin > $MODAL_BIN > "modal".
func (p *Provider) resolveBin() string {
	if p.ModalBin != "" {
		return p.ModalBin
	}
	if env := os.Getenv(EnvBin); env != "" {
		return env
	}
	return "modal"
}

// Available reports whether the driver can plausibly Provision: both
// credential env vars are set AND a modal binary is on PATH (or resolved
// via MODAL_BIN). Cheap — no network calls.
func (p *Provider) Available() bool {
	if os.Getenv(EnvTokenID) == "" || os.Getenv(EnvTokenSecret) == "" {
		return false
	}
	bin := p.resolveBin()
	_, err := exec.LookPath(bin)
	return err == nil
}

// Provision writes an ephemeral Python manifest for the session and
// returns a Sandbox whose Wrapper runs commands via `modal run`. We do
// NOT pre-warm the sandbox — Modal cold-starts on the first invocation.
// Pre-warming would require a separate `modal run …::warmup` and adds a
// failure mode without much win for short-lived sessions.
func (p *Provider) Provision(ctx context.Context, opts cloud.ProvisionOptions) (*cloud.Sandbox, error) {
	if !p.Available() {
		return nil, fmt.Errorf("modal: %w (need %s, %s, modal CLI)", cloud.ErrNotConfigured, EnvTokenID, EnvTokenSecret)
	}
	bin := p.resolveBin()
	appName := AppName(opts.SessionID)

	spec := ManifestSpec{
		AppName:      appName,
		Image:        opts.Image,
		WorkspaceDir: opts.WorkspaceDir,
		// Image hint is recorded in the manifest header. Concrete pip
		// packages aren't derivable from a single image string yet, so we
		// pass none; the agent installs what it needs at runtime.
	}
	manifest, err := WriteManifest(opts.SessionID, spec)
	if err != nil {
		return nil, err
	}

	wrapper := &Wrapper{
		ModalBin:     bin,
		ManifestPath: manifest,
		AppName:      appName,
	}
	teardown := func(tdCtx context.Context) error {
		// Best-effort: try `modal app stop`, then remove the manifest.
		// Both errors surface but don't block each other.
		stopErr := exec.CommandContext(tdCtx, bin, "app", "stop", appName).Run()
		rmErr := os.Remove(manifest)
		switch {
		case stopErr != nil && rmErr != nil:
			return fmt.Errorf("modal teardown: stop=%v; rm=%v", stopErr, rmErr)
		case stopErr != nil:
			return fmt.Errorf("modal app stop: %w", stopErr)
		case rmErr != nil && !os.IsNotExist(rmErr):
			return fmt.Errorf("modal manifest rm: %w", rmErr)
		}
		return nil
	}

	return &cloud.Sandbox{
		Wrapper:  wrapper,
		Teardown: teardown,
		Info: map[string]string{
			"provider": "modal",
			"app":      appName,
			"manifest": manifest,
			"image":    opts.Image,
		},
	}, nil
}

// Wrapper builds the `modal run` argv that executes a single command
// inside the Modal sandbox via the exec_in_sandbox entrypoint.
//
// argv shape:
//
//	[modal, run, <manifest>::exec_in_sandbox, --cmd, <cmd>, --args, <json-list>]
//
// The args are JSON-encoded so any positional flag/argument with spaces
// or shell metacharacters survives Modal's flag parser intact. The
// manifest's exec_in_sandbox decodes them via json.loads.
type Wrapper struct {
	ModalBin     string // path to modal CLI; "" → "modal"
	ManifestPath string // path to the generated Python manifest
	AppName      string // gil-<sessionID>; informational, not used in argv
}

// Wrap implements cloud.CommandWrapper / core/tool.CommandWrapper.
func (w *Wrapper) Wrap(cmd string, args ...string) []string {
	bin := w.ModalBin
	if bin == "" {
		bin = "modal"
	}
	target := w.ManifestPath + "::exec_in_sandbox"
	return []string{
		bin, "run", target,
		"--cmd", cmd,
		"--args", encodeArgs(args),
	}
}

// encodeArgs JSON-encodes a string slice, guaranteeing:
//   - nil/empty slice → "[]" (not "null"), so the Python side's json.loads
//     yields a list and `[*argv]` works without a special case.
//   - shell metachars like &, <, > are NOT HTML-escaped (default
//     json.Marshal would emit "&" which is harmless for json.loads
//     but ugly in logs and confuses argv comparison in tests).
func encodeArgs(args []string) string {
	if len(args) == 0 {
		return "[]"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(args) // []string never fails to marshal
	return strings.TrimRight(buf.String(), "\n")
}
