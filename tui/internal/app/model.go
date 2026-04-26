package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jedutools/gil/sdk"
)

const eventBufferSize = 200

// Model is the Bubbletea root model for the gil TUI.
type Model struct {
	socket   string
	client   *sdk.Client
	keys     KeyMap

	sessions    []*sdk.Session
	selectedIdx int
	width       int
	height      int
	err         string

	activeTail *tailHandle // current Tail subscription; nil when none
	events     []string    // ring buffer of formatted event lines for the active session
}

// startTailingSelected cancels any existing tail subscription and starts a new
// one for the currently selected session if its status is RUNNING.
// Returns nil when no tail should be started.
func (m *Model) startTailingSelected() tea.Cmd {
	if m.activeTail != nil {
		m.activeTail.cancel()
		m.activeTail = nil
	}
	m.events = nil
	if len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
		return nil
	}
	s := m.sessions[m.selectedIdx]
	if s.Status != "RUNNING" {
		return nil
	}
	return startTail(m.client, s.ID)
}

// New constructs a Model and dials the gild socket.
func New(socket string) (*Model, error) {
	cli, err := sdk.Dial(socket)
	if err != nil {
		return nil, err
	}
	return &Model{
		socket: socket,
		client: cli,
		keys:   DefaultKeys(),
	}, nil
}

// Init returns the initial Cmd: load the session list.
func (m *Model) Init() tea.Cmd {
	return loadSessionsCmd(m.client)
}

// loadSessionsCmd asynchronously fetches the session list.
func loadSessionsCmd(client *sdk.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sessions, err := client.ListSessions(ctx, 100)
		if err != nil {
			return errMsg{err.Error()}
		}
		return sessionsLoadedMsg{sessions}
	}
}

type sessionsLoadedMsg struct{ sessions []*sdk.Session }
type errMsg struct{ message string }
