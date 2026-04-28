package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
)

// newChatCmdForTest returns a synthetic cobra.Command shaped like a real
// chat invocation — owns its own context and the hidden --auth-file flag
// so detectPreDaemonState's credstore probe lands in the test's isolated
// auth.json rather than the user's real one.
func newChatCmdForTest(t *testing.T, authFile string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{}
	addAuthFileFlag(c)
	if authFile != "" {
		require.NoError(t, c.Flags().Set("auth-file", authFile))
	}
	c.SetContext(context.Background())
	return c
}

// withGilHomeForOnboard points GIL_HOME at a fresh tmpdir for the duration of the
// test, restoring whatever was there afterwards. Both branches of
// detectPreDaemonState route through paths.FromEnv() so this is the
// load-bearing isolation for state detection.
func withGilHomeForOnboard(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIL_HOME", dir)
	// Wipe known env-var fallbacks so hasAnyCred only sees the
	// credstore. Each test that wants env-creds re-sets them.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	return dir
}

// touchInit writes an empty config.toml under the GIL_HOME layout to
// simulate a completed `gil init`. We only care that hasInit() flips
// to true; content doesn't matter for state detection.
func touchInit(t *testing.T, gilHome string) {
	t.Helper()
	configDir := filepath.Join(gilHome, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("# stub\n"), 0o600))
}

func TestDetectPreDaemonState_NoInit(t *testing.T) {
	withGilHomeForOnboard(t)
	cmd := newChatCmdForTest(t, "")
	require.Equal(t, stateNoInit, detectPreDaemonState(cmd))
}

func TestDetectPreDaemonState_NoCreds(t *testing.T) {
	dir := withGilHomeForOnboard(t)
	touchInit(t, dir)
	authFile := filepath.Join(dir, "config", "auth.json")
	cmd := newChatCmdForTest(t, authFile)
	require.Equal(t, stateNoCreds, detectPreDaemonState(cmd))
}

func TestDetectPreDaemonState_NoCreds_EnvFallback(t *testing.T) {
	// Env var alone is enough to skip the no-creds card, even with an
	// empty auth.json.
	dir := withGilHomeForOnboard(t)
	touchInit(t, dir)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-env")
	authFile := filepath.Join(dir, "config", "auth.json")
	cmd := newChatCmdForTest(t, authFile)
	require.Equal(t, stateReady, detectPreDaemonState(cmd))
}

func TestDetectPreDaemonState_Ready(t *testing.T) {
	dir := withGilHomeForOnboard(t)
	touchInit(t, dir)
	authFile := filepath.Join(dir, "config", "auth.json")
	// Seed a credential through the real store so hasAnyCred returns true.
	require.NoError(t,
		runAuthLoginForTest(t, authFile, "anthropic", "sk-ant-test1234567890abcd"))
	cmd := newChatCmdForTest(t, authFile)
	require.Equal(t, stateReady, detectPreDaemonState(cmd))
}

// runAuthLoginForTest is a tiny shim around runAuthCmd so we don't have
// to repeat the boilerplate when seeding credentials in onboarding-
// state tests. We only need success / failure here, not the output.
func runAuthLoginForTest(t *testing.T, authFile, provider, apiKey string) error {
	t.Helper()
	_, _, err := runAuthCmd(t, authFile, "login", provider, "--api-key", apiKey)
	return err
}

func TestRenderOnboardCard_Snapshot(t *testing.T) {
	var buf bytes.Buffer
	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true) // ASCII so the snapshot is portable
	renderOnboardCard(&buf, p, g, "Welcome",
		[]string{"This looks like a fresh install."},
		[]onboardStep{{cmd: "gil init", desc: "create config dirs"}},
	)
	out := buf.String()
	require.Contains(t, out, "G I L")
	require.Contains(t, out, "Welcome")
	require.Contains(t, out, "gil init")
	require.Contains(t, out, "create config dirs")
}

func TestConfirmInline_DefaultYes(t *testing.T) {
	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true)
	in := strings.NewReader("\n") // empty line == default yes
	var out bytes.Buffer
	require.True(t, confirmInline(in, &out, p, g, "ok?"))
}

func TestConfirmInline_NoVariants(t *testing.T) {
	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true)
	for _, ans := range []string{"n", "N", "no", "No", "NOPE"} {
		in := strings.NewReader(ans + "\n")
		var out bytes.Buffer
		require.False(t, confirmInline(in, &out, p, g, "ok?"), "input %q", ans)
	}
}

func TestConfirmInline_YesVariants(t *testing.T) {
	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true)
	for _, ans := range []string{"y", "Y", "yes", "YES", "sure"} {
		in := strings.NewReader(ans + "\n")
		var out bytes.Buffer
		require.True(t, confirmInline(in, &out, p, g, "ok?"), "input %q", ans)
	}
}

func TestRunOnboardingNoInit_DeclineExits(t *testing.T) {
	withGilHomeForOnboard(t)
	cmd := newChatCmdForTest(t, "")
	cmd.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)

	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true)
	err := runOnboardingNoInit(cmd, cmd.InOrStdin(), &out, p, g)
	require.NoError(t, err)
	rendered := out.String()
	require.Contains(t, rendered, "Welcome")
	require.Contains(t, rendered, "gil init")
	require.Contains(t, rendered, "OK. Run `gil init` whenever you're ready.")
}

func TestRunOnboardingNoCreds_DeclineExits(t *testing.T) {
	dir := withGilHomeForOnboard(t)
	touchInit(t, dir)
	authFile := filepath.Join(dir, "config", "auth.json")
	cmd := newChatCmdForTest(t, authFile)
	cmd.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)

	p := uistyle.NewPalette(false)
	g := uistyle.NewGlyphs(true)
	err := runOnboardingNoCreds(cmd, cmd.InOrStdin(), &out, p, g)
	require.NoError(t, err)
	rendered := out.String()
	require.Contains(t, rendered, "Almost there")
	require.Contains(t, rendered, "gil auth login")
}
