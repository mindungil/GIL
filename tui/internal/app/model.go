package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jedutools/gil/sdk"
)

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
