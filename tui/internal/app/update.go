package app

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/key"
)

// Update implements tea.Model. Handles key input, terminal resize, and
// async messages from loadSessionsCmd / future tail subscriptions.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sessionsLoadedMsg:
		m.sessions = msg.sessions
		if m.selectedIdx >= len(m.sessions) {
			m.selectedIdx = 0
		}
		m.err = ""
		return m, nil

	case errMsg:
		m.err = msg.message
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Refresh):
			return m, loadSessionsCmd(m.client)
		case key.Matches(msg, m.keys.Up):
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.selectedIdx < len(m.sessions)-1 {
				m.selectedIdx++
			}
			return m, nil
		}
	}
	return m, nil
}
