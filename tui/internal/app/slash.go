package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jedutools/gil/core/paths"
	"github.com/jedutools/gil/core/slash"
	"github.com/jedutools/gil/sdk"
)

// slashState tracks the surface-side slash-command lifecycle. It is
// owned by Model and only mutated from the Update goroutine.
//
// Two booleans + one buffer model the three states:
//
//   - inputting=false, output=""        : idle (no panel)
//   - inputting=true                    : prompt open at bottom of screen
//   - inputting=false, output non-empty : last command's output shown
//
// Mode flips happen on key events; see slashKeyHandler below.
type slashState struct {
	registry *slash.Registry
	env      *slash.HandlerEnv

	inputting bool   // true while the user types a command
	buffer    string // current input contents (without leading "/")

	// output of the most-recent command. Cleared by Esc when no prompt
	// is open, or replaced by the next command.
	output string
}

// initSlashState builds the registry, registers the canonical 9 commands,
// and returns a slashState ready to be plugged onto Model.
func initSlashState(client *sdk.Client) *slashState {
	layout, _ := paths.FromEnv()
	env := &slash.HandlerEnv{
		Layout: layout,
		// Fetcher closes over the gRPC client so /status, /cost, /diff,
		// /agents can refresh session info on demand. We use a 2 s
		// deadline mirroring loadSessionsCmd above — slash commands are
		// interactive and a hung daemon should fall back to a friendly
		// error rather than freeze the surface.
		Fetcher: func(ctx context.Context, sessionID string) (*slash.SessionInfo, error) {
			if client == nil {
				return nil, errors.New("no gRPC client (mock-mode TUI)")
			}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			s, err := client.GetSession(cctx, sessionID)
			if err != nil {
				return nil, err
			}
			return &slash.SessionInfo{
				ID:               s.ID,
				Status:           s.Status,
				WorkingDir:       s.WorkingDir,
				GoalHint:         s.GoalHint,
				CurrentIteration: s.CurrentIteration,
				CurrentTokens:    s.CurrentTokens,
			}, nil
		},
	}
	reg := slash.NewRegistry()
	slash.RegisterDefaults(reg, env)
	return &slashState{registry: reg, env: env}
}

// attachSession refreshes env.SessionID + LocalState before each
// dispatch so /status etc. always operate on the currently-focused row.
func (s *slashState) attachSession(m *Model) {
	if len(m.sessions) > 0 && m.selectedIdx < len(m.sessions) {
		s.env.SessionID = m.sessions[m.selectedIdx].ID
	} else {
		s.env.SessionID = ""
	}
	s.env.Local.ClearEvents = func() { m.events = nil }
}

// slashKeyHandler routes key events while a slash prompt is open or a
// dismissable output panel is showing. Returns (handled, model, cmd):
// handled=false means the event should fall through to the normal Update
// switch.
func (m *Model) slashKeyHandler(msg tea.KeyMsg) (handled bool, _ tea.Model, _ tea.Cmd) {
	if m.slash == nil {
		return false, m, nil
	}
	st := m.slash

	// Input mode: capture printable runes, Backspace, Enter, Esc.
	if st.inputting {
		switch msg.Type {
		case tea.KeyEsc:
			st.inputting = false
			st.buffer = ""
			return true, m, nil
		case tea.KeyEnter:
			line := "/" + strings.TrimSpace(st.buffer)
			st.inputting = false
			st.buffer = ""
			return true, m, dispatchSlashCmd(m, line)
		case tea.KeyBackspace:
			if len(st.buffer) > 0 {
				st.buffer = st.buffer[:len(st.buffer)-1]
			}
			return true, m, nil
		case tea.KeyRunes, tea.KeySpace:
			st.buffer += string(msg.Runes)
			return true, m, nil
		}
		// Swallow everything else while input is open so navigation keys
		// don't accidentally move the cursor underneath.
		return true, m, nil
	}

	// Idle: "/" or ":" opens the prompt; Esc dismisses any leftover
	// output panel.
	switch {
	case key.Matches(msg, m.keys.Slash):
		st.inputting = true
		st.buffer = ""
		st.output = ""
		return true, m, nil
	case key.Matches(msg, m.keys.DismissSlash) && st.output != "":
		st.output = ""
		return true, m, nil
	}
	return false, m, nil
}

// dispatchSlashCmd parses the line and runs the resolved handler in a
// background goroutine, delivering the result via slashResultMsg so the
// Update loop never blocks waiting for a gRPC round-trip.
func dispatchSlashCmd(m *Model, line string) tea.Cmd {
	cmd, ok := slash.ParseLine(line)
	if !ok {
		return func() tea.Msg {
			return slashResultMsg{output: "(empty command)"}
		}
	}
	spec, ok := m.slash.registry.Lookup(cmd.Name)
	if !ok {
		return func() tea.Msg {
			return slashResultMsg{output: "unknown command: /" + cmd.Name + "  (try /help)"}
		}
	}
	// Refresh env attachments so the handler operates on the row the
	// user is currently looking at, not whatever was selected when the
	// TUI booted.
	m.slash.attachSession(m)
	env := m.slash.env
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// NoSession commands skip the env.SessionID guard.
		if !spec.NoSession && env.SessionID == "" {
			return slashResultMsg{output: "no session attached — open a session first"}
		}
		out, err := spec.Handler(ctx, cmd)
		if err != nil {
			if errors.Is(err, slash.ErrQuit) {
				return slashQuitMsg{output: out}
			}
			return slashResultMsg{output: "error: " + err.Error()}
		}
		return slashResultMsg{output: out}
	}
}

// Messages emitted by dispatchSlashCmd.
type slashResultMsg struct{ output string }
type slashQuitMsg struct{ output string }
