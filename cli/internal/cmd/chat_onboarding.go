package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/paths"
)

// onboardingState classifies a fresh `gil` invocation into one of four
// progress states. The chat surface used to drop every user — fresh
// install or seasoned — into the same banner + "Limited mode" warning,
// which Phase 25 user testing flagged as the worst part of first-run UX.
//
// The rule: short-circuit BEFORE we boot the daemon when the user has
// not yet completed init or registered a provider. Steering to the
// concrete next command (gil init / gil auth login) — and offering to
// run it inline — is the difference between "I get it" and "what is
// this thing".
type onboardingState int

const (
	// stateNoInit — no config.toml on disk. The user has never run
	// `gil init`; the XDG dirs may or may not exist (tests, partial
	// setups). Treat as a clean slate and walk them through init.
	stateNoInit onboardingState = iota

	// stateNoCreds — init has been done (config.toml present) but no
	// provider is registered (credstore empty AND no env-var
	// fallback). Walk them straight into the auth wizard.
	stateNoCreds

	// stateFirstMission — provider registered, no sessions yet. The
	// existing chat banner already adapts ("No active sessions"); we
	// keep the normal flow and let it speak for itself.
	stateFirstMission

	// stateReady — provider + at least one session. Normal returning-
	// user flow; nothing onboarding-specific to render.
	stateReady
)

// detectPreDaemonState classifies the user's setup without spawning
// the daemon. Only stateNoInit and stateNoCreds matter here — those
// are the two we want to short-circuit on before paying the daemon
// boot cost. Everything else collapses to stateReady; the caller
// then proceeds to the regular daemon-backed flow which can refine
// further with session counts if it wants.
//
// hasInit + hasAnyCred are split out so the chat package can unit-
// test each branch without a full filesystem fixture.
func detectPreDaemonState(cmd *cobra.Command) onboardingState {
	layout, err := paths.FromEnv()
	if err != nil {
		// If we can't even resolve the layout, the user has bigger
		// problems than onboarding — fall through to ready and let
		// downstream surfaces produce the actionable error.
		return stateReady
	}
	if !hasInit(layout) {
		return stateNoInit
	}
	if !hasAnyCred(cmd) {
		return stateNoCreds
	}
	return stateReady
}

// hasInit reports whether `gil init` has been run at least once. We use
// the presence of config.toml as the marker — `gil init` always writes
// it (idempotently) and no other surface does. Checking the dir alone
// would over-trigger because XDG roots can pre-exist for unrelated
// tools.
func hasInit(layout paths.Layout) bool {
	_, err := os.Stat(layout.ConfigFile())
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	// Permission / IO weirdness — fail open so we don't block the
	// chat on a transient stat error.
	return true
}

// hasAnyCred reports whether the user has at least one usable
// credential, either in the credstore or via an environment variable.
// We check env vars because a user who exports ANTHROPIC_API_KEY in
// their shell rc has a fully working setup even with an empty
// auth.json.
func hasAnyCred(cmd *cobra.Command) bool {
	store := newStoreFor(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if names, err := store.List(ctx); err == nil && len(names) > 0 {
		return true
	}
	for _, env := range []string{
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENAI_BASE_URL", // vllm via env
	} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}

// runOnboardingNoInit renders the fresh-install welcome card and offers
// to run `gil init` inline. Returning true means the caller should drop
// out of runChat (the user either completed init in-line or chose to
// quit); returning false is currently impossible (we always exit) but
// the signature is reserved for later "skip onboarding, drop to limited
// chat" branching.
func runOnboardingNoInit(cmd *cobra.Command, in io.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs) error {
	renderOnboardCard(out, p, g, "Welcome",
		[]string{
			"This looks like a fresh install — let's set up gil first.",
		},
		[]onboardStep{
			{cmd: "gil init", desc: "create config dirs + walk you through provider login"},
		},
	)

	if !confirmInline(in, out, p, g, "Run `gil init` now?") {
		fmt.Fprintln(out, agentLine(p, g, p.Dim("OK. Run `gil init` whenever you're ready.")))
		fmt.Fprintln(out)
		return nil
	}
	fmt.Fprintln(out)
	return invokeInit(cmd)
}

// runOnboardingNoCreds renders the "almost there" card for users who
// have run init but haven't registered a provider yet, then offers the
// auth wizard inline.
func runOnboardingNoCreds(cmd *cobra.Command, in io.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs) error {
	renderOnboardCard(out, p, g, "Almost there",
		[]string{
			"You've initialised gil but haven't registered a provider yet.",
			"Pick one to start coding — Anthropic, OpenAI, OpenRouter, or self-hosted vLLM.",
		},
		[]onboardStep{
			{cmd: "gil auth login", desc: "interactive wizard — picks provider, saves key, tests connection"},
		},
	)

	if !confirmInline(in, out, p, g, "Run the wizard now?") {
		fmt.Fprintln(out, agentLine(p, g, p.Dim("OK. Run `gil auth login` whenever you're ready.")))
		fmt.Fprintln(out)
		return nil
	}
	fmt.Fprintln(out)
	return invokeAuthLogin(cmd)
}

// onboardStep is one row in an onboarding card — a command on the left
// and a short prose description on the right. Two columns hand-aligned
// (no tabwriter) because the card is small and the labels are
// fixed-width by design.
type onboardStep struct {
	cmd, desc string
}

// renderOnboardCard prints a focused onboarding panel — title plate +
// rule + body lines + indented command list. Visually consistent with
// the chat banner (renderChatBanner) so the user doesn't feel like
// they've been kicked into a different application.
func renderOnboardCard(out io.Writer, p uistyle.Palette, g uistyle.Glyphs, title string, body []string, steps []onboardStep) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s  ─  %s\n", p.Primary("G I L"), p.Surface(title))
	fmt.Fprintln(out, "  "+p.Dim(strings.Repeat("━", 76)))
	fmt.Fprintln(out)
	for _, ln := range body {
		fmt.Fprintln(out, "  "+p.Surface(ln))
	}
	fmt.Fprintln(out)
	for _, st := range steps {
		fmt.Fprintf(out, "    %s   %s\n", p.Info(st.cmd), p.Dim(st.desc))
	}
	fmt.Fprintln(out)
}

// confirmInline reads a single Y/n line from stdin. Default is YES — we
// want fresh-install users to fall into the next step without thinking;
// anyone who wants to bail can type `n`. Reads one line, trims, and
// treats anything starting with "n"/"N" (incl. "no") as a refusal.
//
// EOF and read errors return false (treat as "user wants to leave"),
// which matches the safe default for non-TTY callers.
func confirmInline(in io.Reader, out io.Writer, p uistyle.Palette, g uistyle.Glyphs, prompt string) bool {
	fmt.Fprintf(out, "  %s %s %s ", p.Dim(g.QuoteBar), p.Surface(prompt), p.Dim("[Y/n]"))
	r, ok := in.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(in)
	}
	line, err := r.ReadString('\n')
	// Emit a newline regardless of whether the user's terminal echoed
	// one — keeps the next message on its own row when piped or
	// captured (tests, GIL_TEST_FORCE_TTY harness, scripted demos).
	fmt.Fprintln(out)
	if err != nil && line == "" {
		return false
	}
	ans := strings.TrimSpace(strings.ToLower(line))
	if ans == "" {
		return true
	}
	return !strings.HasPrefix(ans, "n")
}

// invokeInit re-uses the existing `gil init` subcommand so the chat
// surface and the standalone command stay bug-for-bug equivalent. We
// plumb the chat command's stdin/out/err through so prompts land in
// the right place; SetArgs(nil) keeps init from inheriting the chat
// command's flags by accident.
func invokeInit(parent *cobra.Command) error {
	c := initCmd()
	c.SetIn(parent.InOrStdin())
	c.SetOut(parent.OutOrStdout())
	c.SetErr(parent.ErrOrStderr())
	c.SetArgs(nil)
	return c.ExecuteContext(parent.Context())
}

// invokeAuthLogin re-uses authLoginCmd in the same way as invokeInit.
// We pass no positional args so the wizard runs in full interactive
// mode (no provider preselected).
func invokeAuthLogin(parent *cobra.Command) error {
	c := authLoginCmd()
	c.SetIn(parent.InOrStdin())
	c.SetOut(parent.OutOrStdout())
	c.SetErr(parent.ErrOrStderr())
	c.SetArgs(nil)
	return c.ExecuteContext(parent.Context())
}
