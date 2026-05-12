package modal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/runtime/cloud"
)

// --- interface conformance --------------------------------------------------

func TestProvider_ImplementsCloudProvider(t *testing.T) {
	var _ cloud.Provider = (*Provider)(nil)
}

func TestWrapper_ImplementsCloudWrapper(t *testing.T) {
	var _ cloud.CommandWrapper = (*Wrapper)(nil)
}

// --- Available --------------------------------------------------------------

func TestProvider_Available_RequiresEnvVars(t *testing.T) {
	t.Setenv(EnvTokenID, "")
	t.Setenv(EnvTokenSecret, "")
	require.False(t, New().Available())
}

func TestProvider_Available_RequiresBinaryOnPath(t *testing.T) {
	t.Setenv(EnvTokenID, "fake")
	t.Setenv(EnvTokenSecret, "fake")
	t.Setenv(EnvBin, "definitely-no-such-binary-xyzzy")
	require.False(t, New().Available())
}

func TestProvider_Available_OK_WithFakeBin(t *testing.T) {
	bin := writeFakeModal(t, `echo OK`)
	t.Setenv(EnvTokenID, "fake")
	t.Setenv(EnvTokenSecret, "fake")
	t.Setenv(EnvBin, bin)
	require.True(t, New().Available())
}

// --- Provision --------------------------------------------------------------

func TestProvider_Provision_NotConfigured(t *testing.T) {
	t.Setenv(EnvTokenID, "")
	t.Setenv(EnvTokenSecret, "")
	_, err := New().Provision(context.Background(), cloud.ProvisionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, cloud.ErrNotConfigured))
}

func TestProvider_Provision_WritesManifest(t *testing.T) {
	bin := writeFakeModal(t, `echo OK`)
	t.Setenv(EnvTokenID, "fake")
	t.Setenv(EnvTokenSecret, "fake")
	t.Setenv(EnvBin, bin)

	work := t.TempDir()
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		SessionID:    "sess-abc",
		Image:        "python:3.12-slim",
		WorkspaceDir: work,
	})
	require.NoError(t, err)
	require.NotNil(t, sb)

	manifest := sb.Info["manifest"]
	require.NotEmpty(t, manifest)
	require.FileExists(t, manifest)
	t.Cleanup(func() { _ = os.Remove(manifest) })

	body, err := os.ReadFile(manifest)
	require.NoError(t, err)
	bs := string(body)

	// Manifest must define the right Modal app and the exec_in_sandbox fn.
	require.Contains(t, bs, `modal.App("gil-sess-abc")`)
	require.Contains(t, bs, "def exec_in_sandbox(cmd: str, args: str)")
	require.Contains(t, bs, `modal.Mount.from_local_dir(`)
	require.Contains(t, bs, fmt.Sprintf("%q", work))
	require.Contains(t, bs, `remote_path="/workspace"`)
	require.Contains(t, bs, "subprocess.run")
	require.Contains(t, bs, "@app.local_entrypoint()")

	// Info map exposes the things that matter to RunService logging.
	require.Equal(t, "modal", sb.Info["provider"])
	require.Equal(t, "gil-sess-abc", sb.Info["app"])
	require.Equal(t, "python:3.12-slim", sb.Info["image"])
}

// --- Wrapper.Wrap argv shape ------------------------------------------------

func TestWrapper_Wrap_ArgvShape(t *testing.T) {
	w := &Wrapper{
		ModalBin:     "modal",
		ManifestPath: "/tmp/gil-modal-sess-abc.py",
		AppName:      "gil-sess-abc",
	}
	got := w.Wrap("bash", "-c", "echo hello && ls")

	expected := []string{
		"modal", "run",
		"/tmp/gil-modal-sess-abc.py::exec_in_sandbox",
		"--cmd", "bash",
		"--args", `["-c","echo hello && ls"]`,
	}
	require.Equal(t, expected, got)
}

func TestWrapper_Wrap_DefaultsModalBin(t *testing.T) {
	w := &Wrapper{ManifestPath: "/tmp/x.py"}
	got := w.Wrap("ls")
	require.Equal(t, "modal", got[0])
}

func TestWrapper_Wrap_EmptyArgsIsValidJSON(t *testing.T) {
	w := &Wrapper{ModalBin: "modal", ManifestPath: "/tmp/x.py"}
	got := w.Wrap("whoami")
	// Last arg must be a JSON list literal, not the empty string.
	require.Equal(t, "[]", got[len(got)-1])
}

// --- Teardown ---------------------------------------------------------------

func TestProvider_Teardown_CallsAppStopAndRemovesManifest(t *testing.T) {
	// Fake modal binary that writes its argv (one per line) into $CAPTURE.
	tmp := t.TempDir()
	capture := filepath.Join(tmp, "argv.log")
	bin := writeFakeModal(t, fmt.Sprintf(`for a in "$@"; do printf '%%s\n' "$a"; done >> %q`, capture))

	t.Setenv(EnvTokenID, "fake")
	t.Setenv(EnvTokenSecret, "fake")
	t.Setenv(EnvBin, bin)

	work := t.TempDir()
	sb, err := New().Provision(context.Background(), cloud.ProvisionOptions{
		SessionID:    "td-1",
		Image:        "python:3.12-slim",
		WorkspaceDir: work,
	})
	require.NoError(t, err)
	manifest := sb.Info["manifest"]
	require.FileExists(t, manifest)

	require.NoError(t, sb.Teardown(context.Background()))

	// After teardown the manifest must be gone.
	_, statErr := os.Stat(manifest)
	require.True(t, os.IsNotExist(statErr), "manifest should be removed; stat err=%v", statErr)

	// Captured argv must include `app stop gil-td-1`.
	body, err := os.ReadFile(capture)
	require.NoError(t, err)
	args := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Equal(t, []string{"app", "stop", "gil-td-1"}, args)
}

// --- helpers ---------------------------------------------------------------

// writeFakeModal drops a tiny shell script into a temp dir and returns its
// absolute path. Tests inject this via $MODAL_BIN. The body runs as the
// script body of `bash`.
func writeFakeModal(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake modal binary uses POSIX sh; skip on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "modal")
	script := "#!/usr/bin/env bash\n" + body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// --- ManifestSpec rendering -------------------------------------------------

func TestRenderManifest_PinsAppNameAndWorkspace(t *testing.T) {
	body := RenderManifest(ManifestSpec{
		AppName:      "gil-x",
		Image:        "python:3.12-slim",
		WorkspaceDir: "/abs/work dir",
		PipPackages:  []string{"requests", "numpy"},
	})
	require.Contains(t, body, `modal.App("gil-x")`)
	require.Contains(t, body, `pip_install(["requests", "numpy"])`)
	require.Contains(t, body, `modal.Mount.from_local_dir("/abs/work dir"`)
}

func TestAppName_SanitizesSessionID(t *testing.T) {
	require.Equal(t, "gil-abc-123", AppName("ABC_123"))
}

// --- ensure JSON round-trip in Wrap is unambiguous --------------------------

func TestWrapper_Wrap_JSONArgsRoundTrip(t *testing.T) {
	w := &Wrapper{ModalBin: "modal", ManifestPath: "/tmp/x.py"}
	got := w.Wrap("python", "-c", "print('hi, world')", "--flag=value with spaces")
	var decoded []string
	require.NoError(t, json.Unmarshal([]byte(got[len(got)-1]), &decoded))
	require.Equal(t, []string{"-c", "print('hi, world')", "--flag=value with spaces"}, decoded)
}
