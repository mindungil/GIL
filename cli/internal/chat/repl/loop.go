package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mindungil/gil/cli/internal/chat/render"
	"github.com/mindungil/gil/cli/internal/errmap"
)

// isTerminalExit reports whether a bare line should exit the REPL.
// Per docs/design/chat-architecture.md §3.1, terminal exit is the
// ONE client-side recognition that survives the slash-removal pass —
// the chat surface never matches strings to verbs otherwise. Slash-
// prefixed forms (`/quit`, `/exit`) also exit so users with the
// muscle memory don't get punished.
func isTerminalExit(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "quit", "exit", "bye", "/quit", "/exit":
		return true
	}
	return false
}

// SessionSummary carries the display-relevant fields for a single session
// in the /sessions listing. Name is a short human label (typically the
// truncated GoalHint); GoalHint is the full hint for fallback rendering;
// CreatedAt drives the relative-age column.
type SessionSummary struct {
	ID, Name, Phase string
	GoalHint        string
	CreatedAt       time.Time
}

// SessionClient abstracts the gRPC session boundary so loop tests
// don't need a live daemon. The cmd/chat.go integration provides a
// real implementation backed by sdk.Client.
//
// M2 narrowed this interface to just what the chat surface needs.
// Verb dispatch (Spec/Status/Diff/Merge/StartRun/Compact/SwitchSession/
// NewSession) was removed — the agent calls those as tools server-side.
// ListSessions stays for the pre-first-turn entry disclosure.
type SessionClient interface {
	SendPrompt(ctx context.Context, prompt string) error
	NextAssistantChunk(ctx context.Context) (chunk string, more bool, err error)
	NextEvent(ctx context.Context) (in TrackerInput, ok bool, err error)

	ActiveSessionID() string
	ListSessions(ctx context.Context) ([]SessionSummary, error)

	Close() error
}

type Config struct {
	In       io.Reader
	Renderer render.Renderer
	Client   SessionClient
	// Router was a §2.6(b) verb classifier on the client; M2 removed
	// it (see core/intent/router.go header). Field stays here (always
	// nil) to keep the cobra wiring in cli/internal/cmd/chat.go from
	// breaking until M3 deletes both sides. Effectively dead.
	Router *struct{}
}

// Run executes the chat REPL until the user types /quit, EOF, or an
// unrecoverable client error.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Renderer == nil {
		return fmt.Errorf("repl.Run: Renderer required")
	}
	tr := NewTracker()
	cfg.Renderer.Banner(tr.State())

	// §2.6 self-disclose: surface recent sessions inline at entry so the
	// user doesn't need to know /sessions to find prior work. Soft-fails
	// on errors — the status strip still prints the idle hint.
	if cfg.Client != nil && cfg.Client.ActiveSessionID() == "" {
		emitWelcomeDisclosure(ctx, cfg)
	}

	scanner := bufio.NewScanner(cfg.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	// lastStripPhase remembers the Phase last sent to the renderer so
	// we only repaint the status strip when something the strip
	// actually displays has changed. The previous unconditional repaint
	// every iteration sandwiched assistant text between two identical
	// strips after a turn ended (#40 — the artifact "What's your project
	// goal? appears AFTER the next strip line"). Initialised to a
	// sentinel so the very first iteration always paints once.
	var lastStripPhase render.Phase = "<unset>"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Drain any pending events into the tracker before deciding
		// whether to repaint. This may emit system notes that are
		// orthogonal to the strip — those still print regardless.
		drainEvents(ctx, cfg, tr)

		state := tr.State()
		if state.Phase != lastStripPhase {
			cfg.Renderer.StatusStrip(state)
			lastStripPhase = state.Phase
		}
		cfg.Renderer.PromptCue()

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("input error: %v", err))
				return err
			}
			return nil // EOF
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Terminal exit is the ONE non-agent client-side recognition
		// per docs/design/chat-architecture.md §3.1 — the chat surface
		// is a 100% natural-language path otherwise. A bare "quit",
		// "exit", or "bye" exits cleanly. Anything else, including
		// slash-prefixed input, forwards to the daemon's agent.
		if isTerminalExit(line) {
			return nil
		}
		if err := cfg.Client.SendPrompt(ctx, line); err != nil {
			cfg.Renderer.SystemNote(render.NoteSystem,
				"send failed: "+errmap.FormatForChat(err))
			continue
		}
		{
			// Stream assistant chunks until the client signals done.
			// A 30s watchdog converts a silent hang (gild waiting on a
			// dead provider, no events ever) into a visible note so the
			// user knows to ctrl+c rather than staring at an idle prompt.
			gotAnyChunk := false
			streamErrored := false
			turnStart := time.Now()
			warned := false
			for {
				chunk, more, err := cfg.Client.NextAssistantChunk(ctx)
				if err != nil {
					cfg.Renderer.SystemNote(render.NoteSystem,
						"stream error: "+humanizeStreamErr(err))
					streamErrored = true
					// #30: drainInterviewStream captures the error into
					// streamErr; surface the wrapped form (with Hint when
					// available) instead of dropping it on the floor.
					break
				}
				if chunk != "" {
					cfg.Renderer.AssistantText(chunk)
					gotAnyChunk = true
				}
				if !more {
					break
				}
				if !warned && !gotAnyChunk && time.Since(turnStart) > 30*time.Second {
					cfg.Renderer.SystemNote(render.NoteSystem,
						"no response after 30s — daemon may be hung on a provider call. check `tail /tmp/gild.log`, ctrl+c to abort")
					warned = true
				}
			}
			if !gotAnyChunk && !streamErrored {
				// Clean stream close with no events — daemon path
				// completed silently. Surface so the user knows their
				// turn produced nothing rather than staring at idle.
				cfg.Renderer.SystemNote(render.NoteSystem,
					"empty stream — interview produced no output (provider misconfigured or auth missing?)")
			}
			cfg.Renderer.AssistantText("\n")
		}
	}
}

func drainEvents(ctx context.Context, cfg Config, tr *Tracker) {
	for {
		in, ok, err := cfg.Client.NextEvent(ctx)
		if err != nil {
			cfg.Renderer.SystemNote(render.NoteSystem,
				"event stream error: "+errmap.FormatForChat(err))
			return
		}
		if !ok {
			return
		}
		prev := tr.State()
		tr.Apply(in)
		emitDeltaNotes(cfg.Renderer, prev, tr.State(), in)
	}
}

// emitDeltaNotes turns tracker state changes into one-line system
// notes so the user sees what shifted between strips.
func emitDeltaNotes(r render.Renderer, prev, cur render.SessionState, ev TrackerInput) {
	switch ev.Kind {
	case "session.allocated":
		// Daemon allocated a fresh session in response to a
		// SessionService.Prompt call with empty session_id. Render
		// once so the user can pin the id (paste it elsewhere /
		// reference it in chat). Subsequent prompts reuse the id
		// silently.
		r.SystemNote(render.NoteSystem,
			"session "+shortID(ev.SessionID)+" started")
	case "tool.call":
		// Agent invoked a server-side tool. Show ⚒ + name + brief
		// input so the user sees what the agent is doing.
		input := ev.ToolInput
		if len(input) > 80 {
			input = input[:80] + "…"
		}
		msg := "⚒ " + ev.ToolName
		if input != "" && input != "{}" {
			msg += "  " + input
		}
		r.SystemNote(render.NoteSystem, msg)
	case "tool.result":
		glyph := "⚒ ✓"
		body := ev.ToolContent
		if ev.ToolIsError {
			glyph = "⚒ ✗"
		}
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		body = strings.ReplaceAll(body, "\n", " · ")
		r.SystemNote(render.NoteSystem, glyph+"  "+body)
	case "prompt.metrics":
		// Tokens / latency are reflected in the strip via
		// Tracker.Apply — no need for a system note. Left as a
		// no-op case to document where the kind comes from.
	case "interview.slot_filled":
		if cur.SlotsFilled > prev.SlotsFilled {
			r.SystemNote(render.NoteSpec,
				fmt.Sprintf("slot filled (%d/%d, sat %d%%)",
					cur.SlotsFilled, cur.SlotsTotal, int(cur.Saturation*100+0.5)))
		}
	case "interview.adversary":
		if cur.AdvFindings != prev.AdvFindings {
			r.SystemNote(render.NoteAdversary,
				fmt.Sprintf("%d finding(s)", cur.AdvFindings))
		}
	case "interview.started":
		// Sensing → conversation. The Reason payload looks like
		// "domain=cli-tooling confidence=0.85" — show it so the user
		// knows what the engine inferred about their request.
		msg := "interview started"
		if ev.Reason != "" {
			msg += " — " + ev.Reason
		}
		r.SystemNote(render.NoteSystem, msg)
	case "interview.resumed":
		r.SystemNote(render.NoteSystem, "resumed in-progress interview")
	case "interview.ready_to_freeze":
		r.SystemNote(render.NoteSaturation, "ready to freeze — /run to start")
	case "run.stuck":
		// Surface WHICH pattern fired so the user can decide whether
		// to wait for the auto-recovery strategy or step in. Reads
		// from ev (the unmuted TrackerInput payload) since the
		// tracker keeps SessionState renderer-shaped and doesn't
		// retain the stuck details.
		msg := "stuck — agent loop detected"
		if ev.StuckPattern != "" {
			msg = "stuck — " + humanStuckPattern(ev.StuckPattern)
			if ev.StuckDetail != "" {
				msg += " (" + ev.StuckDetail + ")"
			}
		}
		msg += ". recovery in progress — no in-chat stop verb yet (V1.1)"
		r.SystemNote(render.NoteSystem, msg)
	case "run.recovered":
		msg := "recovered — agent unblocked"
		if ev.StuckDetail != "" {
			msg += ": " + ev.StuckDetail
		}
		r.SystemNote(render.NoteSystem, msg)
	case "run.done":
		r.SystemNote(render.NoteSystem, "done — /diff to review, /merge to apply")
	case "compact_start":
		// Tokens carries the estimated-token count when not forced.
		msg := "compacting conversation history"
		if ev.Reason != "" {
			msg = "compacting — " + ev.Reason
		} else if ev.Tokens > 0 {
			msg = fmt.Sprintf("compacting — ~%s estimated tokens", formatTokensCompact(ev.Tokens))
		}
		r.SystemNote(render.NoteSystem, msg)
	case "compact_done":
		// RetryAttempt = original message count, RetryMax = compacted
		// message count, Tokens = saved-tokens delta. Original 24 → 6
		// reads better than "saved 1.2k tokens" alone.
		msg := "compaction done"
		if ev.RetryAttempt > 0 && ev.RetryMax > 0 {
			msg = fmt.Sprintf("compaction done — %d → %d msgs", ev.RetryAttempt, ev.RetryMax)
		}
		if ev.Tokens > 0 {
			msg += fmt.Sprintf(" (saved ~%s tokens)", formatTokensCompact(ev.Tokens))
		}
		r.SystemNote(render.NoteSystem, msg)
	case "compact_error":
		msg := "compaction failed — continuing with current history"
		if ev.Reason != "" {
			msg = "compaction failed — " + ev.Reason
		}
		r.SystemNote(render.NoteSystem, msg)
	case "subagent_started":
		// Show the goal so a long sub-loop doesn't look like a hang.
		// The subagent tool clamps goal length at the server side; we
		// truncate again as a chat-strip safety net.
		msg := "subagent started"
		if ev.Reason != "" {
			msg += " — " + truncateRetryReason(ev.Reason)
		}
		r.SystemNote(render.NoteSystem, msg)
	case "subagent_done":
		// RetryAttempt is reused as iteration count for this case
		// (see grpc_client.go's mapping comment). Summary is the
		// already-truncated 512B from the server.
		msg := "subagent done"
		if ev.RetryAttempt > 0 {
			msg = fmt.Sprintf("subagent done (%d iters)", ev.RetryAttempt)
		}
		if ev.Reason != "" {
			msg += " — " + truncateRetryReason(ev.Reason)
		}
		r.SystemNote(render.NoteSystem, msg)
	case "budget_warning":
		// Reason is "tokens" or "cost"; CostUSD carries the running
		// total. Mark with NoteV11 (the existing "soft warning"
		// taxonomy) so the strip color emphasizes vs system notes.
		dim := "budget"
		if ev.Reason != "" {
			dim = ev.Reason
		}
		msg := fmt.Sprintf("approaching %s budget", dim)
		if ev.CostUSD > 0 && ev.Reason == "cost" {
			msg = fmt.Sprintf("approaching cost budget — $%.2f used", ev.CostUSD)
		}
		r.SystemNote(render.NoteV11, msg)
	case "budget_exceeded":
		dim := "budget"
		if ev.Reason != "" {
			dim = ev.Reason
		}
		msg := fmt.Sprintf("%s budget exceeded — run will halt at end of iteration", dim)
		r.SystemNote(render.NoteSystem, msg)
	case "provider.retry_attempt":
		// Provider returned a retryable error (5xx / rate-limit / network).
		// We're between attempt N and N+1, sleeping RetryWaitMs. Show the
		// progression so the user understands the gap is intentional rather
		// than a daemon hang. Phase is unaffected — the run hasn't moved.
		msg := "provider retry"
		if ev.RetryAttempt > 0 && ev.RetryMax > 0 {
			msg = fmt.Sprintf("retry %d/%d in %s",
				ev.RetryAttempt, ev.RetryMax, formatRetryWait(ev.RetryWaitMs))
		}
		if ev.Reason != "" {
			msg += " — " + truncateRetryReason(ev.Reason)
		}
		r.SystemNote(render.NoteSystem, msg)
	case "permission.ask":
		// Followup #2 — pre-M3 claim was permission_ask events fell on
		// the chat floor. Surface as a SystemNote with the request_id +
		// tool + key the user pipes to `gil permission answer`. When the
		// ask comes from a subagent (S9 routing), the label is prepended
		// so the user knows which child blocked.
		src := ""
		if ev.FromSubagentLabel != "" {
			src = "subagent " + ev.FromSubagentLabel + " "
		}
		msg := fmt.Sprintf("%spermission requested: %s %s · req_id=%s · `gil permission answer %s --allow` (or --deny) within 60s",
			src, ev.PermissionTool, truncatePermissionKey(ev.PermissionKey), ev.RequestID, ev.RequestID)
		r.SystemNote(render.NoteSystem, msg)
	}
}

// truncatePermissionKey trims a long command/path key down to a width
// that fits the strip without breaking layout.
func truncatePermissionKey(k string) string {
	if len(k) <= 60 {
		return k
	}
	return k[:60] + "…"
}

// formatTokensCompact returns "1.2k" for ≥1000, the bare integer
// otherwise. Used by emitDeltaNotes to keep compaction notes short.
func formatTokensCompact(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// formatRetryWait turns a wait duration in ms into a compact label
// ("0.5s", "12s", "2m"). Single-line strip cells don't have room for
// a full duration so we cap at minutes.
func formatRetryWait(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm", ms/60_000)
}

// truncateRetryReason clips a long upstream error message (often the
// raw provider response body — JSON, HTML maintenance page, etc.) to a
// chat-friendly length. Most real retry errors are short ("rate limit
// exceeded", "503"); the truncation is a safety net for HTML 503s and
// deeply nested JSON error envelopes.
func truncateRetryReason(s string) string {
	const max = 80
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// (buildSessionContext / verbToSlashArgs / dispatchSlash deleted in M2 —
// the chat surface no longer dispatches verbs. The agent calls tools
// in the daemon. cli/internal/cmd/* keeps the verb-mode subcommands for
// headless / script use; that path uses the SDK directly, not these
// helpers.)

// formatSessionRow renders one session as a numbered listing row used by
// both the entry self-disclosure and /sessions. Single source of truth
// for the row layout: short ID (10 chars covers full ms-precision ULID
// timestamp), relative age, phase tag, label.
func formatSessionRow(n int, s SessionSummary) string {
	short := s.ID
	if len(short) > 10 {
		short = short[:10]
	}
	label := s.Name
	if label == "" {
		label = "—"
	}
	return fmt.Sprintf("%d. %-10s  %-6s  [%s]  %s",
		n, short, formatAge(s.CreatedAt), s.Phase, label)
}

// emitWelcomeDisclosure prints a one-line lead-in plus up to topN recent
// sessions as system notes, called once at REPL entry when there's no
// active session. Soft-fails on ListSessions errors (daemon not ready,
// transient gRPC issue) so chat entry never blocks on a stale daemon.
func emitWelcomeDisclosure(ctx context.Context, cfg Config) {
	const topN = 5
	list, err := cfg.Client.ListSessions(ctx)
	if err != nil {
		return
	}
	if len(list) == 0 {
		cfg.Renderer.SystemNote(render.NoteSystem,
			"no past sessions — describe what you want to build")
		return
	}
	shown := list
	if len(shown) > topN {
		shown = shown[:topN]
	}
	var lead string
	if len(list) == 1 {
		lead = "1 past session — pick it below or describe a new task"
	} else if len(list) <= topN {
		lead = fmt.Sprintf("%d past sessions — pick one below or describe a new task", len(list))
	} else {
		lead = fmt.Sprintf("%d past sessions — most recent %d below, describe a new task or resume one",
			len(list), topN)
	}
	cfg.Renderer.SystemNote(render.NoteSystem, lead)
	for i, s := range shown {
		cfg.Renderer.SystemNote(render.NoteSystem, formatSessionRow(i+1, s))
	}
}

// formatAge renders a CreatedAt timestamp as a compact relative-age
// string (e.g. "4m", "2h", "3d"). Zero / future timestamps render as "—"
// so the listing column doesn't flash a 57-year offset for mocked rows.
func formatAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return "—"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// humanizeStreamErr extracts the user-relevant part of a gRPC error for
// chat-surface display. Strips the "rpc error: code = ... desc = " prefix
// the gRPC client wraps every status with, leaving just the server-supplied
// message. When the message is unrecognized, returns the original error
// string so we never lose information.
// humanStuckPattern maps a core/stuck/detector.go pattern name into a
// short user-facing phrase. Returns the input unchanged for unknown
// patterns so a future detector pattern still surfaces SOMETHING.
func humanStuckPattern(p string) string {
	switch p {
	case "PatternRepeatedActionObservation":
		return "same tool result loop"
	case "PatternRepeatedActionError":
		return "same tool error loop"
	case "PatternMonologue":
		return "talking without acting"
	case "PatternPingPong":
		return "alternating tool ping-pong"
	case "PatternContextWindowError":
		return "context window overflow"
	case "PatternNoProgress":
		return "no file progress"
	}
	return p
}

func humanizeStreamErr(err error) string {
	if err == nil {
		return ""
	}
	// First try the shared dispatch table so a known server message
	// (credentials missing, no active run, etc.) surfaces with the
	// same Hint cobra commands give. Falls through to the raw-strip
	// path for unrecognised errors so we never lose information.
	if wrapped := errmap.WrapRPCError(err); wrapped != err {
		return errmap.FormatForChat(wrapped)
	}
	s := err.Error()
	if i := strings.Index(s, "desc = "); i >= 0 {
		s = s[i+len("desc = "):]
	}
	return s
}
