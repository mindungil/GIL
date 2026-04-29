package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/cli/internal/chat/render"
	"github.com/mindungil/gil/cli/internal/chat/repl"
	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/sdk"
)

// chatCmd returns the explicit `gil chat` entrypoint. It is also the
// implementation behind bare `gil` invocation when stdout is a TTY (see
// root.go's RunE shim) — calling it directly is for users who want the
// chat surface even when their stdout is piped (e.g. tee'd into a log).
//
// chat is the V1 chat surface for gil. It collapses the previous
// verb-routing UI into a single REPL: the user types prompts in free
// text, slash commands manage session lifecycle (/sessions /switch /new
// /spec /status /diff /merge /run /quit /help), and a tracker maps
// daemon events to a one-line status strip rendered between turns.
//
// Pre-daemon onboarding (no-init / no-creds short-circuit) lives in
// chat_onboarding.go and is gated by detectPreDaemonState BEFORE the
// daemon is dialed.
//
// The actual REPL lives in cli/internal/chat/repl. This file is the
// thin cobra entry point that constructs a SessionClient (via
// repl.NewGRPCClient) and a Renderer (StdoutChatRenderer) and hands
// off to repl.Run.
func chatCmd() *cobra.Command {
	var socket, providerName, model string
	c := &cobra.Command{
		Use:     "chat",
		Aliases: []string{"talk"},
		Short:   "Drop into the gil conversational surface (no verbs needed)",
		Long: `Start the gil chat surface. Tell the agent what you want to do in
plain language; gil routes your message to the right downstream flow
(interview for new work, resume for prior sessions, status for a
glance at what's running).

The same surface is launched when you run bare gil in an interactive
terminal — gil chat is the explicit form for piped or scripted use.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChat(cmd, socket, providerName, model)
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "", "LLM provider for intent classification + interview (anthropic|openai|openrouter|vllm|mock)")
	c.Flags().StringVar(&model, "model", "", "LLM model id for the interview engine (empty → provider default)")
	return c
}

// runChat is the chat command entry point. After the onboarding gate,
// it dials the daemon, constructs a gRPC SessionClient and a stdout
// renderer, then runs repl.Run until the user types /quit or hits EOF.
//
// providerName and model parameters are accepted from the cobra command
// but currently unused — repl.Run drives prompts through the daemon's
// configured provider. They will return when V1.1 wires runtime prompt
// echo to a real LLM (see /docs/plans/phase-26-implementation.md T10).
func runChat(cmd *cobra.Command, socket, providerName, model string) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	// Phase 25 S3: short-circuit fresh-install / no-cred users to a
	// focused onboarding card BEFORE we boot the daemon. The chat
	// surface used to drop everyone — including users who'd never run
	// `gil init` — into the same banner + "Limited mode" warning, which
	// hid the actual next step (run init / run auth login) behind a
	// dim secondary line.
	switch detectPreDaemonState(cmd) {
	case stateNoInit:
		return runOnboardingNoInit(cmd, in, out, p, g)
	case stateNoCreds:
		return runOnboardingNoCreds(cmd, in, out, p, g)
	}

	if err := ensureDaemon(socket, defaultBase()); err != nil {
		return err
	}
	cli, err := sdk.Dial(socket)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	workingDir, _ := cmd.Flags().GetString("working-dir")
	if workingDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			workingDir = cwd
		}
	}
	grpcClient := repl.NewGRPCClient(cli, workingDir)
	defer grpcClient.Close()

	noColor := os.Getenv("NO_COLOR") != ""
	renderer := render.NewStdoutChatRenderer(out, in, asciiMode, noColor)
	defer renderer.Close()

	return repl.Run(ctx, repl.Config{
		In:       in,
		Renderer: renderer,
		Client:   grpcClient,
	})
}

// agentLine wraps text in the spec's quote-bar margin (§7 chat aesthetic).
// Used for every line the agent emits so a transcript reads naturally.
func agentLine(p uistyle.Palette, g uistyle.Glyphs, text string) string {
	return p.Dim(g.QuoteBar) + " " + text
}

// renderChatStatus is the chat-flavoured rendering of the session list.
// Differs from runSummary in that we drop the noisy header and budget
// columns — the chat surface is conversational, so a tighter list reads
// better between turns.
func renderChatStatus(out io.Writer, g uistyle.Glyphs, p uistyle.Palette, sessions []*sdk.Session) {
	if len(sessions) == 0 {
		fmt.Fprintln(out, agentLine(p, g, "No sessions yet."))
		return
	}
	fmt.Fprintln(out, agentLine(p, g, fmt.Sprintf("%d session(s):", len(sessions))))
	for i, s := range sessions {
		if i >= 10 {
			fmt.Fprintln(out, agentLine(p, g, p.Dim(fmt.Sprintf("  + %d more (run `gil status` for the full list)", len(sessions)-10))))
			break
		}
		marker, role := sessionStatusGlyph(g, s.Status)
		coloured := colourMarker(p, marker, role)
		goal := truncRune(s.GoalHint, 56)
		fmt.Fprintf(out, "%s   %s  %-22s %s\n",
			agentLine(p, g, ""), coloured, p.Dim(displayName(s)), goal)
	}
}

// renderChatHelp prints a one-screen capability primer. We keep it
// conversational rather than reproducing the cobra --help output —
// users who want the full surface still get it via `gil --help`.
//
// Phase 25 A2 — grouped by where the user is in their session
// (starting / working / recovery) instead of a flat command dump. The
// flat list optimised for "everything we can do"; the grouped form
// optimises for "what should I do RIGHT NOW", which matches how the
// chat surface is actually consulted (mid-task, looking for the next
// move) rather than browsed.
func renderChatHelp(out io.Writer, g uistyle.Glyphs, p uistyle.Palette) {
	type group struct {
		title string
		items []string
	}
	groups := []group{
		{
			"Just starting",
			[]string{
				"Tell me a task in plain English — I'll ask follow-ups, then run autonomously.",
				"Say \"explain\" for a short primer on what gil does.",
			},
		},
		{
			"Currently working",
			[]string{
				"Say \"status\" to see what's running.",
				"Say \"continue\" to resume a previous session.",
				"Outside chat:  gil watch <id>   gil events <id> --tail",
			},
		},
		{
			"Recovery",
			[]string{
				"/quit (or Ctrl-D)     leave the chat",
				"gil doctor            check setup",
				"gil auth login        (re)register a provider",
				"gil session rm <id>   delete a stuck session",
			},
		},
	}
	fmt.Fprintln(out, agentLine(p, g, "Here's what I can do, grouped by where you are."))
	for _, gr := range groups {
		fmt.Fprintln(out, agentLine(p, g, ""))
		fmt.Fprintln(out, agentLine(p, g, p.Surface(gr.title)))
		for _, it := range gr.items {
			fmt.Fprintln(out, agentLine(p, g, "  "+p.Dim("•")+" "+it))
		}
	}
}

// renderChatExplain prints a short "what is gil?" primer. Used when the
// classifier identifies a meta-question.
func renderChatExplain(out io.Writer, g uistyle.Glyphs, p uistyle.Palette) {
	lines := []string{
		"gil is an autonomous coding harness. The flow:",
		"",
		"  1. Interview — I ask you about the task until I have enough to lock a spec.",
		"  2. Freeze — the spec becomes immutable; the agent loop reads from it.",
		"  3. Run — the agent edits, runs verifiers, and self-corrects until done or stuck.",
		"",
		"You only talk to me at step 1. Steps 2-3 happen on their own.",
	}
	for _, ln := range lines {
		fmt.Fprintln(out, agentLine(p, g, ln))
	}
}

// credentialFor reads the named credstore entry; returns nil on miss.
func credentialFor(cmd *cobra.Command, name credstore.ProviderName) *credstore.Credential {
	store := newStoreFor(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cred, err := store.Get(ctx, name)
	if err != nil {
		return nil
	}
	return cred
}

// intentModelFor returns the model name to use for intent classification
// given a provider. When the user supplied an explicit --model we honour
// it; otherwise we pick the smallest model the provider exposes so a
// classification call costs well under a cent.
//
// PHASE 25 CHANGE: previously hardcoded "qwen3.6-27b" as a vllm default
// — that was the dev's local model, not a general one. The new
// resolution order is:
//
//  1. --model flag (userModel) — explicit user pick wins.
//  2. credstore.Credential.Model — set by the wizard at registration.
//  3. GIL_VLLM_MODEL env var (vllm only) — legacy override.
//  4. Hardcoded sensible default for paid providers; "" for vllm so
//     the chat surface refuses to start the LLM-driven path and tells
//     the user to run `gil auth login vllm` to pick a model.
//
// The two-arg form preserved here goes through resolveIntentModel with
// no cmd — it doesn't read credstore. The cmd-aware form is what
// pickIntentProvider actually calls in production.
func intentModelFor(providerName, userModel string) string {
	return resolveIntentModel(nil, providerName, userModel)
}

// resolveIntentModel is the cmd-aware variant of intentModelFor. The
// wizard-set credstore.Model takes precedence over hardcoded defaults
// so a vllm user who registered "qwen3-32b" via `gil auth login vllm`
// actually gets that, not a fallback that ignores their setup.
func resolveIntentModel(cmd *cobra.Command, providerName, userModel string) string {
	if userModel != "" {
		return userModel
	}
	if cmd != nil {
		if cred := credentialFor(cmd, credstore.ProviderName(providerName)); cred != nil && cred.Model != "" {
			return cred.Model
		}
	}
	switch providerName {
	case "anthropic":
		return "claude-haiku-4-5"
	case "openai":
		return "gpt-4o-mini"
	case "openrouter":
		return "anthropic/claude-haiku-4-5"
	case "vllm":
		// vllm has no canonical default. Honour the legacy env var
		// override; otherwise return "" — the caller (chat surface)
		// will refuse the LLM-driven path and point the user at the
		// wizard. The previous hardcoded "qwen3.6-27b" was the dev's
		// local model and broke "general user" assumptions.
		if v := os.Getenv("GIL_VLLM_MODEL"); v != "" {
			return v
		}
		return ""
	default:
		return ""
	}
}

// filterActiveSessions hides abandoned sessions from the chat preamble.
// Phase 24 § E rule: a session created more than a day ago that's still
// in the CREATED status with zero events is almost certainly a dummy
// from a prior smoke test; surfacing it just clutters the chat banner.
//
// We intentionally only filter at the chat surface — `gil status` and
// the no-arg summary keep their full lists. The chat is meant to be
// glanceable; the verb-mode surfaces are exhaustive.
func filterActiveSessions(in []*sdk.Session) []*sdk.Session {
	out := make([]*sdk.Session, 0, len(in))
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, s := range in {
		if s == nil {
			continue
		}
		// "CREATED" is the proto's pre-interview state; sessions in
		// later states (interviewing, frozen, running, done) always
		// pass. The cutoff comparison only kicks in when CreatedAt
		// is set — old daemons without the timestamp populated stay
		// visible.
		if strings.EqualFold(s.Status, "CREATED") || strings.EqualFold(s.Status, "SESSION_STATUS_CREATED") {
			if !s.CreatedAt.IsZero() && s.CreatedAt.Before(cutoff) {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// matchSessionByPrefix finds sessions whose ID starts with the given
// prefix (case-insensitive). Returns all matches so the caller can
// disambiguate when the prefix is too short.
func matchSessionByPrefix(sessions []*sdk.Session, prefix string) []*sdk.Session {
	var out []*sdk.Session
	for _, s := range sessions {
		if s == nil {
			continue
		}
		if strings.HasPrefix(strings.ToLower(s.ID), prefix) {
			out = append(out, s)
		}
	}
	return out
}

// isQuitWord returns true for the chat surface's exit lexicon.
// Includes "/quit" (matches run.go's interactive REPL) and bare
// "quit"/"exit"/"bye" because users don't usually type a leading slash.
func isQuitWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "/quit", "/q", "/exit", "quit", "exit", "bye":
		return true
	}
	return false
}

// shortHex returns the first 12 chars of a hex string for display. Used
// by the freeze confirmation line where the full SHA-256 would be
// overwhelming.
func shortHex(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// stdoutIsTTY reports whether stdout is connected to a terminal. Used
// by root.go to decide between dropping into chat (TTY) and keeping the
// existing summary (piped stdout, e.g. `gil > log.txt`). Centralising
// the check keeps the policy in one place — chat is for humans, the
// summary remains script-friendly.
func stdoutIsTTY() bool {
	f, ok := any(os.Stdout).(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
