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
		return m, m.startTailingSelected()

	case tailStartedMsg:
		m.activeTail = msg.handle
		return m, nextEventCmd(msg.handle)

	case eventMsg:
		// Only buffer events for the currently active subscription.
		if m.activeTail != nil && msg.sessID == m.activeTail.sessID {
			m.events = append(m.events, formatEvent(msg.ev))
			if len(m.events) > eventBufferSize {
				m.events = m.events[len(m.events)-eventBufferSize:]
			}
			// Check for permission_ask events and surface the modal.
			if ask := parsePermissionAsk(msg.sessID, msg.ev.GetType(), msg.ev.GetDataJson()); ask != nil {
				m.pendingAsk = ask
			}
			return m, nextEventCmd(msg.handle)
		}
		return m, nil

	case askAnswerMsg:
		if msg.err != "" {
			m.err = msg.err
		}
		return m, nil

	case tailEndedMsg:
		if m.activeTail != nil && msg.sessID == m.activeTail.sessID {
			m.activeTail = nil
		}
		return m, nil

	case tailErrMsg:
		if m.activeTail != nil && msg.sessID == m.activeTail.sessID {
			m.activeTail = nil
		}
		// Silently ignore — NotFound when no run is active is expected.
		return m, nil

	case errMsg:
		m.err = msg.message
		return m, nil

	case tea.KeyMsg:
		// When a permission modal is open, intercept ALL keys and handle y/n/esc.
		if m.pendingAsk != nil {
			ask := m.pendingAsk
			switch msg.String() {
			case "y", "Y":
				m.pendingAsk = nil
				return m, answerCmd(m.client, ask.SessionID, ask.RequestID, true)
			case "n", "N", "esc":
				m.pendingAsk = nil
				return m, answerCmd(m.client, ask.SessionID, ask.RequestID, false)
			}
			// Swallow other keys while modal is open.
			return m, nil
		}
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Refresh):
			return m, tea.Batch(loadSessionsCmd(m.client), m.startTailingSelected())
		case key.Matches(msg, m.keys.Up):
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
			return m, m.startTailingSelected()
		case key.Matches(msg, m.keys.Down):
			if m.selectedIdx < len(m.sessions)-1 {
				m.selectedIdx++
			}
			return m, m.startTailingSelected()
		}
	}
	return m, nil
}
