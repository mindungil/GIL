package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// SessionClient abstracts the gRPC session boundary so loop tests
// don't need a live daemon. The cmd/chat.go integration provides a
// real implementation backed by sdk.Client.
type SessionClient interface {
	SendPrompt(ctx context.Context, prompt string) error
	NextAssistantChunk(ctx context.Context) (chunk string, more bool, err error)
	NextEvent(ctx context.Context) (in TrackerInput, ok bool, err error)
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
			if SlashRequiresSession(cmd) && tr.State().SessionID == "" {
				cfg.Renderer.SystemNote(render.NoteSystem,
					"no active session — start one with a prompt or /sessions")
				continue
			}
			if err := dispatchSlash(ctx, cfg, tr, cmd, args); err != nil {
				cfg.Renderer.SystemNote(render.NoteSystem,
					fmt.Sprintf("/%s failed: %v", cmd, err))
			}

		case InputPrompt:
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

// dispatchSlash is a stub at this stage; Task 9 fills in real
// behaviors. We return nil so unknown-but-valid slashes don't crash.
func dispatchSlash(_ context.Context, cfg Config, _ *Tracker, cmd, _ string) error {
	switch cmd {
	case "help":
		cfg.Renderer.SystemNote(render.NoteSystem,
			"slash commands: /sessions /switch /new /spec /status /diff /merge /run /quit /help")
	}
	return nil
}
