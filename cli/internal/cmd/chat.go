package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/cli/internal/chat/render"
	"github.com/mindungil/gil/cli/internal/chat/repl"
	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/sdk"
)

// noIntentRouter, when true, bypasses the §2.6(b) verb router and
// forwards every prompt straight to the daemon. Set by the
// --no-intent-router flag on bare gil. Defaults false (router on).
var noIntentRouter bool

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
	var socket, providerName, model, workingDir string
	c := &cobra.Command{
		Use:     "chat",
		Aliases: []string{"talk"},
		Short:   "Drop into the gil conversational surface (no verbs needed)",
		Long: `Start the gil chat surface. Type what you want to do in plain
language; the agent owns all verb dispatch via tools (show_diff,
freeze_spec, start_run, etc.). There are no slash commands and no
client-side routing — every prompt streams straight to the daemon.

Bare ` + "`gil`" + ` in a TTY launches the same surface. ` + "`gil chat`" + ` is the
explicit form for piped/scripted use and for the rare case the user
wants the chat surface regardless of TTY state.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChat(cmd, socket, providerName, model)
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "", "LLM provider (anthropic|openai|openrouter|vllm|mock); empty → workspace config")
	c.Flags().StringVar(&model, "model", "", "LLM model id; empty → provider default or workspace config")
	// G4 — #32 followup: the chat handler reads --working-dir via
	// cmd.Flags().GetString but the flag wasn't registered here, so
	// the value silently fell through to os.Getwd(). Register it so
	// `gil chat --working-dir /other/path` actually pins the session
	// to that directory.
	c.Flags().StringVar(&workingDir, "working-dir", "", "project working directory; empty → current working directory")
	return c
}

// runChat is the chat command entry point. After the onboarding gate,
// it dials the daemon, constructs a gRPC SessionClient and a stdout
// renderer, then runs repl.Run until the user types /quit or hits EOF.
//
// providerName and model are forwarded into the GRPCClient via
// SetProvider so they reach the daemon's SessionService.Prompt RPC
// (InterviewService removed in M3). Empty values defer to the layered
// workspace-config defaults applied by session_prompt.go.
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
	grpcClient.SetProvider(providerName, model)
	defer grpcClient.Close()

	noColor := os.Getenv("NO_COLOR") != ""
	renderer := render.NewStdoutChatRenderer(out, in, asciiMode, noColor)
	defer renderer.Close()

	// The intent router is gone (see core/intent/router.go header).
	// noIntentRouter flag is still parsed for backwards-compatibility
	// with shell history but has no effect now — every prompt forwards
	// to the daemon's agent loop.
	_ = noIntentRouter
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
