package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/intent"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/version"
	"github.com/mindungil/gil/sdk"
)

// chatCmd returns the explicit `gil chat` entrypoint. It is also the
// implementation behind bare `gil` invocation when stdout is a TTY (see
// root.go's RunE shim) — calling it directly is for users who want the
// chat surface even when their stdout is piped (e.g. tee'd into a log).
//
// The chat REPL's contract:
//   - Stage 0 (entry): print a banner, then read the first user message.
//   - Stage 1 (intent): classify the message via core/intent. Empty input
//     re-prompts; STATUS / HELP / EXPLAIN render directly; NEW_TASK and
//     RESUME hand off to the existing interview / resume flows.
//   - Stage 2 (handoff): for NEW_TASK we create a session pre-filled with
//     the extracted goal + workspace, then invoke the same interview
//     loop the standalone `gil interview <id>` uses. For RESUME we hand
//     the session ID (or a fuzzy-picked one) to that flow.
//
// The chat surface deliberately reuses existing subcommand implementations
// rather than re-implementing them — this keeps "gil interview <id>" and
// "chat" paths bug-for-bug equivalent.
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

// runChat is the testable entrypoint. cmd is used for in/out plumbing so
// cobra's Execute(args...) test pattern works without poking at the
// process-wide os.Stdin/Stdout. socket / providerName / model come from
// the flags or the no-arg shim defaults.
//
// The function returns nil on a clean exit (user typed /quit or sent EOF
// at the top-level prompt). All other errors propagate so the caller can
// surface them via cliutil.Exit; this matches every other subcommand's
// shape.
func runChat(cmd *cobra.Command, socket, providerName, model string) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	// Daemon up — we need it for ListSessions to seed the intent
	// classifier's "hasSessions" hint and for any handoff that follows.
	if err := ensureDaemon(socket, defaultBase()); err != nil {
		return err
	}
	cli, err := sdk.Dial(socket)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer cli.Close()

	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	allSessions, err := cli.ListSessions(listCtx, 200)
	cancel()
	if err != nil {
		return wrapRPCError(err)
	}
	active := filterActiveSessions(allSessions)

	renderChatBanner(out, g, p, len(active))

	// Single-shot intent classification loop. We deliberately keep the
	// chat at "first message" granularity: the user's first non-empty
	// message routes to a downstream flow that owns the rest of the
	// conversation (interview, resume, etc.). A future iteration can
	// loop here for multi-task chats — Phase 24 only owns the entry.
	reader := bufio.NewReader(in)
	for {
		fmt.Fprint(out, p.Info("›")+" ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return fmt.Errorf("read input: %w", rerr)
		}
		msg := strings.TrimSpace(line)

		// Top-level quit shortcuts. "/quit" matches the existing
		// run-interactive lexicon; bare "exit"/"quit" is forgiving for
		// users coming from other shells.
		if isQuitWord(msg) {
			fmt.Fprintln(out, p.Dim("bye."))
			return nil
		}
		if msg == "" && rerr == io.EOF {
			fmt.Fprintln(out)
			return nil
		}

		// Resolve the optional intent provider lazily. nil is fine —
		// the regex layer alone covers the chat surface's primary
		// shapes; the LLM only matters for ambiguous wordings.
		intentProv, intentModel := pickIntentProvider(cmd, providerName, model)

		it, ierr := intent.Classify(ctx, intentProv, intentModel, msg, len(active) > 0)
		if ierr != nil {
			fmt.Fprintln(out, p.Caution("(classification failed; treating as new task)"))
			it = intent.Intent{Kind: intent.KindNewTask, GoalText: msg}
		}

		switch it.Kind {
		case intent.KindUnknown:
			if msg == "" {
				fmt.Fprintln(out, p.Dim("Tell me what you want to work on."))
				continue
			}
			// Genuinely ambiguous — confirm before commiting to an
			// interview path. We treat the original message as the
			// goal and ask the user to confirm.
			fmt.Fprintln(out, agentLine(p, g, "Not sure I followed. Want me to start a new task with that?"))
			fmt.Fprintln(out, p.Dim("  [y] start new task   [n] back to prompt"))
			ans, _ := readLineRaw(reader)
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
				continue
			}
			it.Kind = intent.KindNewTask
			it.GoalText = msg
			fallthrough

		case intent.KindNewTask:
			return handleChatNewTask(ctx, cmd, cli, it, providerName, model)

		case intent.KindResume:
			return handleChatResume(ctx, cmd, cli, active, it, providerName, model)

		case intent.KindStatus:
			renderChatStatus(out, g, p, allSessions)
			fmt.Fprintln(out, p.Dim("type a task description to start a new session, or /quit"))
			continue

		case intent.KindHelp:
			renderChatHelp(out, g, p)
			continue

		case intent.KindExplain:
			renderChatExplain(out, g, p)
			continue
		}
	}
}

// renderChatBanner prints the chat surface header. The design mirrors the
// no-arg summary banner so users who land here from `gil` recognise the
// visual vocabulary.
//
// abandonedHidden, if non-zero, is rendered as a dim "(N abandoned hidden)"
// note — Phase 24 § E asked us to keep clutter out of the chat preamble
// without surprising users who expected the full count.
func renderChatBanner(out io.Writer, g uistyle.Glyphs, p uistyle.Palette, activeCount int) {
	fmt.Fprintln(out)
	headLeft := p.Primary("G I L") + "  " + p.Dim(version.Short())
	headRight := currentUser() + "  " + p.Info(g.Running) + "  " + currentHost()
	fmt.Fprintf(out, "%s%s%s\n", headLeft, padBetween(headLeft, headRight, 78), headRight)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "  "+p.Surface("Hi. I'm your autonomous coding agent."))
	fmt.Fprintln(out, "  "+p.Dim("Tell me what you want to work on. I'll ask follow-ups until I understand,"))
	fmt.Fprintln(out, "  "+p.Dim("then run on my own — no more interruptions."))

	if activeCount > 0 {
		noun := "session"
		if activeCount != 1 {
			noun = "sessions"
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  "+p.Dim(fmt.Sprintf("%d active %s. Say \"continue\" or \"status\" to pick up.", activeCount, noun)))
	}
	fmt.Fprintln(out)
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
		fmt.Fprintf(out, "%s   %s  %s  %s\n",
			agentLine(p, g, ""), coloured, p.Dim(shortID(s.ID)), goal)
	}
}

// renderChatHelp prints a one-screen capability primer. We keep it
// conversational rather than reproducing the cobra --help output —
// users who want the full surface still get it via `gil --help`.
func renderChatHelp(out io.Writer, g uistyle.Glyphs, p uistyle.Palette) {
	lines := []string{
		"Here's what I can do:",
		"",
		"  • Tell me a task in plain English — I'll ask follow-ups, then run autonomously.",
		"  • Say \"continue\" to resume a previous session.",
		"  • Say \"status\" to see what's running.",
		"  • Type /quit (or Ctrl-D) to leave the chat.",
		"",
		"Power users: every command behind the chat is also available standalone:",
		"  gil interview <id>   gil run <id>   gil status   gil events <id> --tail",
	}
	for _, ln := range lines {
		fmt.Fprintln(out, agentLine(p, g, ln))
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

// handleChatNewTask creates a session pre-filled with the goal/workspace
// the classifier extracted, then drops into the same interactive
// interview loop the standalone `gil interview <id>` uses.
//
// On the spec-freeze handoff (interview reaching the confirm stage), we
// ask the user to ratify the autonomous run; if they accept we kick off
// a detached run + print the watch hint, matching the existing run.go
// --detach behaviour.
func handleChatNewTask(ctx context.Context, cmd *cobra.Command, cli *sdk.Client, it intent.Intent, providerName, model string) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()

	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	// Stage 1 — show the intent we picked up so the user can correct
	// us before we burn provider tokens on an interview.
	if it.Workspace != "" {
		fmt.Fprintln(out, agentLine(p, g, fmt.Sprintf("Got it. New task in %s.", p.Primary(it.Workspace))))
	} else {
		fmt.Fprintln(out, agentLine(p, g, "Got it. Starting a new task."))
	}
	fmt.Fprintln(out, agentLine(p, g, p.Dim("Goal: "+it.GoalText)))

	// Stage 2 — create the session. We pass goal hint + working dir so
	// the spec slot-fill seeds with what we already know, eliminating
	// at least one redundant question.
	sess, err := cli.CreateSession(ctx, sdk.CreateOptions{
		WorkingDir: it.Workspace,
		GoalHint:   it.GoalText,
	})
	if err != nil {
		return wrapRPCError(err)
	}
	fmt.Fprintln(out, agentLine(p, g, p.Dim(fmt.Sprintf("session %s created", shortID(sess.ID)))))
	fmt.Fprintln(out)

	// Stage 3 — run the interview. We mirror the inline loop in
	// interview.go but drive it from this function so the chat surface
	// stays a single REPL from the user's POV (no "gil interview <id>"
	// prompt to copy-paste).
	prov := providerName
	if prov == "" {
		prov = "anthropic"
	}
	startStream, err := cli.StartInterview(ctx, sess.ID, it.GoalText, prov, model, sdk.InterviewModels{})
	if err != nil {
		return wrapRPCError(err)
	}

	reachedSaturation, err := drainChatEvents(out, p, g, startStream)
	if err != nil {
		return wrapRPCError(err)
	}

	reader := bufio.NewReader(in)
	for !reachedSaturation {
		fmt.Fprint(out, "\n"+p.Info("›")+" ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return fmt.Errorf("read input: %w", rerr)
		}
		ans := strings.TrimSpace(line)
		if isQuitWord(ans) {
			fmt.Fprintln(out, p.Dim("bye. (interview paused — `gil interview "+shortID(sess.ID)+"` to resume)"))
			return nil
		}
		if ans == "" && rerr == io.EOF {
			fmt.Fprintln(out)
			return nil
		}
		if ans == "" {
			continue
		}
		replyStream, err := cli.ReplyInterview(ctx, sess.ID, ans)
		if err != nil {
			return wrapRPCError(err)
		}
		reached, err := drainChatEvents(out, p, g, replyStream)
		if err != nil {
			return wrapRPCError(err)
		}
		reachedSaturation = reached
	}

	// Stage 4 — saturation reached. Ask whether to freeze + run.
	fmt.Fprintln(out)
	fmt.Fprintln(out, agentLine(p, g, "Spec is ready. Freeze and run autonomously?"))
	fmt.Fprintln(out, p.Dim("  [Y] freeze + run    [n] stop here    [s] just freeze (run later)"))
	fmt.Fprint(out, p.Info("›")+" ")
	choice, _ := readLineRaw(reader)
	choice = strings.ToLower(strings.TrimSpace(choice))
	switch {
	case choice == "n" || choice == "no":
		fmt.Fprintln(out, agentLine(p, g, p.Dim("Stopping. Resume with: gil interview "+shortID(sess.ID))))
		return nil
	case choice == "s" || choice == "freeze":
		_, hex, err := cli.ConfirmInterview(ctx, sess.ID)
		if err != nil {
			return wrapRPCError(err)
		}
		fmt.Fprintln(out, agentLine(p, g, fmt.Sprintf("Frozen (sha=%s).", shortHex(hex))))
		fmt.Fprintln(out, agentLine(p, g, p.Dim("Run later with: gil run "+shortID(sess.ID))))
		return nil
	default:
		// Y / empty / anything else → freeze + run detached.
		_, _, err := cli.ConfirmInterview(ctx, sess.ID)
		if err != nil {
			return wrapRPCError(err)
		}
		resp, err := cli.StartRun(ctx, sess.ID, prov, model, true)
		if err != nil {
			return wrapRPCError(err)
		}
		fmt.Fprintln(out, agentLine(p, g, "Spec frozen. Run started in the background."))
		if resp.GetStatus() != "" {
			fmt.Fprintln(out, agentLine(p, g, p.Dim("status: "+resp.GetStatus())))
		}
		fmt.Fprintln(out, agentLine(p, g, p.Dim("watch: gil watch "+shortID(sess.ID))))
		fmt.Fprintln(out, agentLine(p, g, p.Dim("tail:  gil events "+shortID(sess.ID)+" --tail")))
		return nil
	}
}

// handleChatResume picks an existing session and hands off to the
// resume flow. The picker is keyboard-driven, no third-party prompt
// library, just numbered choices on stdin.
//
// When the classifier already extracted a session ID prefix and exactly
// one active session matches, we skip the picker entirely.
func handleChatResume(ctx context.Context, cmd *cobra.Command, cli *sdk.Client, active []*sdk.Session, it intent.Intent, providerName, model string) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()

	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	if len(active) == 0 {
		fmt.Fprintln(out, agentLine(p, g, "No sessions to resume. Tell me a new task instead."))
		return nil
	}

	// Try the fast path: classifier extracted a session-id-like token
	// and exactly one active session ID has that prefix.
	if it.SessionID != "" {
		matches := matchSessionByPrefix(active, strings.ToLower(it.SessionID))
		if len(matches) == 1 {
			return runResumeForSession(ctx, cmd, cli, matches[0], providerName, model)
		}
	}

	// Picker: top 3 most-recent active sessions. We rely on the server's
	// list ordering; the SDK doesn't expose a sort knob today.
	candidates := active
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	fmt.Fprintln(out, agentLine(p, g, "Which session?"))
	for i, s := range candidates {
		marker, role := sessionStatusGlyph(g, s.Status)
		coloured := colourMarker(p, marker, role)
		goal := truncRune(s.GoalHint, 56)
		fmt.Fprintf(out, "  %s [%d] %s  %s  %s\n", agentLine(p, g, ""), i+1, coloured, p.Dim(shortID(s.ID)), goal)
	}
	fmt.Fprint(out, p.Info("›")+" ")
	reader := bufio.NewReader(in)
	choice, _ := readLineRaw(reader)
	choice = strings.TrimSpace(choice)
	if choice == "" || isQuitWord(choice) {
		return nil
	}
	idx := -1
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(candidates) {
		fmt.Fprintln(out, agentLine(p, g, p.Caution("Not a valid choice; quitting.")))
		return nil
	}
	return runResumeForSession(ctx, cmd, cli, candidates[idx-1], providerName, model)
}

// runResumeForSession is the resume tail shared by the fast-path and
// the picker. It re-emits the last agent turn (so the user sees the
// prompt they paused on) and then either drops into a reply loop or
// hands to a run, depending on session status.
func runResumeForSession(ctx context.Context, cmd *cobra.Command, cli *sdk.Client, sess *sdk.Session, providerName, model string) error {
	out := cmd.OutOrStdout()
	g := uistyle.NewGlyphs(asciiMode)
	p := uistyle.NewPalette(false)

	prov := providerName
	if prov == "" {
		prov = "anthropic"
	}

	fmt.Fprintln(out, agentLine(p, g, fmt.Sprintf("Resuming %s — %s", p.Primary(shortID(sess.ID)), truncRune(sess.GoalHint, 56))))

	// Lean on the existing resume RPC. It re-emits the last agent
	// turn; we then loop on user replies just like the new-task path.
	stream, err := cli.StartInterview(ctx, sess.ID, "", prov, model, sdk.InterviewModels{})
	if err != nil {
		return wrapRPCError(err)
	}
	reachedSaturation, err := drainChatEvents(out, p, g, stream)
	if err != nil {
		return wrapRPCError(err)
	}

	reader := bufio.NewReader(cmd.InOrStdin())
	for !reachedSaturation {
		fmt.Fprint(out, "\n"+p.Info("›")+" ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return fmt.Errorf("read input: %w", rerr)
		}
		ans := strings.TrimSpace(line)
		if isQuitWord(ans) {
			return nil
		}
		if ans == "" && rerr == io.EOF {
			fmt.Fprintln(out)
			return nil
		}
		if ans == "" {
			continue
		}
		replyStream, err := cli.ReplyInterview(ctx, sess.ID, ans)
		if err != nil {
			return wrapRPCError(err)
		}
		reached, err := drainChatEvents(out, p, g, replyStream)
		if err != nil {
			return wrapRPCError(err)
		}
		reachedSaturation = reached
	}
	fmt.Fprintln(out, agentLine(p, g, p.Dim("Spec ready. Run with: gil run "+shortID(sess.ID))))
	return nil
}

// drainChatEvents reads agent turns + stage transitions and renders them
// in the chat aesthetic. Returns (reachedSaturation, err) — saturation
// is the signal for handleChatNewTask to ask the freeze-and-run question.
//
// This mirrors interview.go's drainEvents but with the prefix style the
// chat surface uses ("▏ " quote bar instead of "Agent: ").
func drainChatEvents(out io.Writer, p uistyle.Palette, g uistyle.Glyphs, s eventStream) (bool, error) {
	reached := false
	for {
		evt, err := s.Recv()
		if err == io.EOF {
			return reached, nil
		}
		if err != nil {
			return reached, fmt.Errorf("recv event: %w", err)
		}
		if t := evt.GetAgentTurn(); t != nil {
			for _, line := range strings.Split(strings.TrimSpace(t.Content), "\n") {
				fmt.Fprintln(out, agentLine(p, g, line))
			}
			continue
		}
		if st := evt.GetStage(); st != nil {
			if st.To == "confirm" {
				reached = true
				fmt.Fprintln(out, agentLine(p, g, p.Success("Saturation reached.")))
				return reached, nil
			}
			fmt.Fprintln(out, p.Dim(fmt.Sprintf("  [stage %s → %s]", st.From, st.To)))
			continue
		}
		if e := evt.GetError(); e != nil {
			return reached, fmt.Errorf("interview error %s: %s", e.Code, e.Message)
		}
	}
}

// pickIntentProvider resolves the provider used for ambiguous-message
// classification. Selection rules (highest priority first):
//
//  1. Explicit --provider flag wins.
//  2. The first credstore entry that maps to a known provider.
//  3. nil (regex layer alone covers the chat surface's primary shapes).
//
// We pick the smallest available model on the resolved provider — haiku
// for anthropic, gpt-4o-mini for openai. If the user passed --model, we
// honour that for everything (interview included), so the smallest-model
// rule only applies to the auto-resolved path.
func pickIntentProvider(cmd *cobra.Command, providerName, model string) (provider.Provider, string) {
	if providerName != "" {
		if p := buildProvider(cmd, providerName); p != nil {
			return p, intentModelFor(providerName, model)
		}
	}
	// Auto: pick the first credstore entry that maps to a provider we
	// can build. We do not require any specific provider — users who
	// only have a vllm endpoint should still get LLM classification.
	store := newStoreFor(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 1*time.Second)
	defer cancel()
	if cmd.Context() == nil {
		ctx = context.Background()
	}
	names, err := store.List(ctx)
	if err != nil {
		return nil, ""
	}
	for _, n := range names {
		if p := buildProvider(cmd, string(n)); p != nil {
			return p, intentModelFor(string(n), model)
		}
	}
	return nil, ""
}

// buildProvider returns a Provider for name, sourcing the API key from
// the credstore (or env, for env-only configurations). Returns nil when
// no credential is available — callers should fall through to the
// regex-only path.
func buildProvider(cmd *cobra.Command, name string) provider.Provider {
	switch credstore.ProviderName(name) {
	case credstore.Anthropic:
		key := credentialOrEnv(cmd, credstore.Anthropic, "ANTHROPIC_API_KEY")
		if key == "" {
			return nil
		}
		return provider.NewAnthropic(key)
	default:
		// OpenAI / OpenRouter / vllm don't have a Provider impl in
		// core/provider yet (or live behind an SDK function the
		// classifier doesn't need to gate on). Returning nil keeps
		// the chat surface working — the regex layer is sufficient.
		return nil
	}
}

// credentialOrEnv reads a provider's API key from the credstore first,
// then falls back to the named env var. Used for the chat surface's
// best-effort intent classification — the runner has its own factory
// that honours BaseURL/OAuth too, but for a single small completion
// the API-key fast-path is enough.
func credentialOrEnv(cmd *cobra.Command, name credstore.ProviderName, envKey string) string {
	store := newStoreFor(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if cred, err := store.Get(ctx, name); err == nil && cred != nil && cred.APIKey != "" {
		return cred.APIKey
	}
	return os.Getenv(envKey)
}

// intentModelFor returns the model name to use for intent classification
// given a provider. When the user supplied an explicit --model we honour
// it; otherwise we pick the smallest model the provider exposes so a
// classification call costs well under a cent.
func intentModelFor(providerName, userModel string) string {
	if userModel != "" {
		return userModel
	}
	switch providerName {
	case "anthropic":
		return "claude-haiku-4-5"
	case "openai":
		return "gpt-4o-mini"
	case "openrouter":
		return "anthropic/claude-haiku-4-5"
	case "vllm":
		return "" // vllm has no canonical "small" — let the server decide
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

// readLineRaw reads one line from r without the surrounding error mess.
// Used by the in-flow prompts (resume picker, freeze-and-run choice)
// where EOF and error are equivalent: the user dropped the connection,
// we return whatever we have.
func readLineRaw(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
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
