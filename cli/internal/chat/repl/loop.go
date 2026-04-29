package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// SessionSummary carries the display-relevant fields for a single session
// in the /sessions listing.
type SessionSummary struct {
	ID, Name, Phase string
}

// SessionClient abstracts the gRPC session boundary so loop tests
// don't need a live daemon. The cmd/chat.go integration provides a
// real implementation backed by sdk.Client.
type SessionClient interface {
	SendPrompt(ctx context.Context, prompt string) error
	NextAssistantChunk(ctx context.Context) (chunk string, more bool, err error)
	NextEvent(ctx context.Context) (in TrackerInput, ok bool, err error)

	ActiveSessionID() string
	Spec(ctx context.Context) (*render.SpecView, error)
	Status(ctx context.Context) (string, error)
	Diff(ctx context.Context) ([]render.DiffHunk, error)
	Merge(ctx context.Context) error
	StartRun(ctx context.Context) error
	ListSessions(ctx context.Context) ([]SessionSummary, error)
	SwitchSession(ctx context.Context, idOrName string) error
	NewSession(ctx context.Context) error

	Close() error
}

type Config struct {
	In       io.Reader
	Renderer render.Renderer
	Client   SessionClient
}

// Run executes the chat REPL until the user types /quit, EOF, or an
// unrecoverable client error.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Renderer == nil {
		return fmt.Errorf("repl.Run: Renderer required")
	}
	tr := NewTracker()
	cfg.Renderer.Banner(tr.State())

	scanner := bufio.NewScanner(cfg.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Drain any pending events into the tracker before drawing strip.
		drainEvents(ctx, cfg, tr)

		cfg.Renderer.StatusStrip(tr.State())
		cfg.Renderer.PromptCue()

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("input error: %v", err))
				return err
			}
			return nil // EOF
		}
		line := scanner.Text()

		kind, cmd, args := ParseInput(line)
		switch kind {
		case InputBlank:
			continue

		case InputSlash:
			if !IsKnownSlash(cmd) {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("unknown slash: /%s — try /help", cmd))
				continue
			}
			if cmd == "quit" {
				return nil
			}
			if SlashRequiresSession(cmd) && cfg.Client.ActiveSessionID() == "" {
				cfg.Renderer.SystemNote(render.NoteSystem,
					"no active session — start one with a prompt or /sessions")
				continue
			}
			if err := dispatchSlash(ctx, cfg, tr, cmd, args); err != nil {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("/%s failed: %v", cmd, err))
			}

		case InputPrompt:
			if tr.State().Phase == render.PhaseRun || tr.State().Phase == render.PhaseStuck {
				cfg.Renderer.SystemNote(render.NoteV11,
					"run-time prompts are V1.1; for now wait for done, or `gil stop <id>` from another shell")
				continue
			}
			if err := cfg.Client.SendPrompt(ctx, args); err != nil {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("send failed: %v", err))
				continue
			}
			// Stream assistant chunks until the client signals done.
			for {
				chunk, more, err := cfg.Client.NextAssistantChunk(ctx)
				if err != nil {
					cfg.Renderer.SystemNote(render.NoteSystem,
						fmt.Sprintf("stream error: %v", err))
					break
				}
				if chunk != "" {
					cfg.Renderer.AssistantText(chunk)
				}
				if !more {
					break
				}
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
				fmt.Sprintf("event stream error: %v", err))
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
	case "interview.ready_to_freeze":
		r.SystemNote(render.NoteSaturation, "ready to freeze — /run to start")
	case "run.stuck":
		r.SystemNote(render.NoteSystem,
			"stuck after recovery — V1.1 will offer /interrupt; for now `gil stop <id>` from another shell")
	case "run.done":
		r.SystemNote(render.NoteSystem, "done — /diff to review, /merge to apply")
	}
}

// dispatchSlash routes each known slash command to the appropriate
// SessionClient call or renderer method.
func dispatchSlash(ctx context.Context, cfg Config, tr *Tracker, cmd, args string) error {
	r := cfg.Renderer
	c := cfg.Client
	switch cmd {
	case "help":
		r.SystemNote(render.NoteSystem,
			"slash commands: /sessions /switch /new /spec /status /diff /merge /run /quit /help")
	case "sessions":
		list, err := c.ListSessions(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			r.SystemNote(render.NoteSystem, "no sessions — type a prompt to start one")
			return nil
		}
		for i, s := range list {
			short := s.ID
			if len(short) > 6 {
				short = short[:6]
			}
			r.SystemNote(render.NoteSystem,
				fmt.Sprintf("%d. %s  %s  [%s]", i+1, short, s.Name, s.Phase))
		}
	case "switch":
		if args == "" {
			r.SystemNote(render.NoteSystem, "/switch <id|name>")
			return nil
		}
		return c.SwitchSession(ctx, args)
	case "new":
		return c.NewSession(ctx)
	case "spec":
		v, err := c.Spec(ctx)
		if err != nil {
			return err
		}
		r.Spec(v)
	case "status":
		s, err := c.Status(ctx)
		if err != nil {
			return err
		}
		r.SystemNote(render.NoteSystem, s)
	case "diff":
		h, err := c.Diff(ctx)
		if err != nil {
			return err
		}
		r.Diff(h)
	case "merge":
		ok, err := r.Confirm("Apply diff to working tree?", false)
		if err != nil || !ok {
			return err
		}
		return c.Merge(ctx)
	case "run":
		if tr.State().Phase != render.PhaseAwaitingConfirm {
			r.SystemNote(render.NoteSystem,
				"spec is not ready to freeze yet — keep iterating with prompts")
			return nil
		}
		return c.StartRun(ctx)
	}
	return nil
}
