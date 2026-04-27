package app

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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
		m.refreshMemoryFromSelection()
		m.rebuildProgressFromEvents()
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
			m.rawEvents = append(m.rawEvents, msg.ev)
			if len(m.rawEvents) > eventBufferSize {
				m.rawEvents = m.rawEvents[len(m.rawEvents)-eventBufferSize:]
			}
			m.rebuildProgressFromEvents()
			// Refresh memory excerpt opportunistically — progress.md is
			// touched at iteration boundaries / milestones; rereading on
			// every event is cheap (small file, OS cache).
			m.refreshMemoryFromSelection()
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

	case slashResultMsg:
		if m.slash != nil {
			m.slash.output = msg.output
		}
		return m, nil

	case slashQuitMsg:
		// /quit dismisses the local TUI but does NOT cancel the run on
		// the server — that's part of the "observation surface, not
		// intervention surface" rule.
		return m, tea.Quit

	case spinnerTickMsg:
		m.spinFrame++
		return m, spinnerTickCmd()

	case checkpointRestoreMsg:
		if msg.err != "" {
			m.checkpoints.Error = msg.err
			m.checkpoints.Notice = ""
		} else {
			m.checkpoints.Error = ""
			m.checkpoints.Notice = "restored to step " + itoa(msg.step)
		}
		return m, nil

	case tea.KeyMsg:
		// Slash command surface owns "/" and ":" plus whatever input the
		// prompt is currently capturing. Run before normal navigation so
		// typing `/help` can't accidentally trigger refresh / movement.
		if m.slash != nil {
			if handled, mm, cmd := m.slashKeyHandler(msg); handled {
				return mm, cmd
			}
		}
		// When a permission modal is open, intercept ALL keys and handle
		// the six allow/deny x once/session/always tiers (plus esc/q
		// for "deny once" as the safe default-on-dismissal). Any
		// non-permission key is swallowed so the user can't accidentally
		// trigger refresh / movement while the agent is blocked waiting
		// for an answer.
		if m.pendingAsk != nil {
			ask := m.pendingAsk
			decision := permissionKeyToDecision(msg.String())
			if decision != gilv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED {
				m.pendingAsk = nil
				return m, answerCmd(m.client, ask.SessionID, ask.RequestID, decision)
			}
			// Swallow other keys while modal is open.
			return m, nil
		}
		// When checkpoint modal is open, route navigation/restore/close.
		if m.checkpoints.Open {
			return m.handleCheckpointKey(msg)
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
		case key.Matches(msg, m.keys.ToggleFilter):
			if m.activityFilter == FilterMilestones {
				m.activityFilter = FilterAll
			} else {
				m.activityFilter = FilterMilestones
			}
			return m, nil
		case key.Matches(msg, m.keys.Checkpoints):
			m.openCheckpointsModal()
			return m, nil
		}
	}
	return m, nil
}

// openCheckpointsModal builds the entries from the current event buffer
// and switches the modal on. No RPC needed — checkpoints are observed
// in-band via tailed events.
func (m *Model) openCheckpointsModal() {
	lite := make([]*tailEventLite, 0, len(m.rawEvents))
	for _, ev := range m.rawEvents {
		ts := "--:--:--"
		if t := ev.GetTimestamp(); t != nil {
			ts = t.AsTime().Format("15:04:05")
		}
		var iter int32
		var note string
		typ := ev.GetType()
		if typ == "iteration_start" {
			iter = parseIterFromEvent(ev.GetDataJson())
		}
		if typ == "checkpoint_committed" {
			note = parseCheckpointNote(ev.GetDataJson())
		}
		lite = append(lite, &tailEventLite{Type: typ, When: ts, Iter: iter, Note: note})
	}
	entries := extractCheckpointEntries(lite)
	m.checkpoints.Open = true
	m.checkpoints.Entries = entries
	if m.checkpoints.Selected >= len(entries) {
		m.checkpoints.Selected = 0
	}
	m.checkpoints.Error = ""
	m.checkpoints.Notice = ""
}

// handleCheckpointKey owns key handling while the checkpoint modal is
// open. ↑/↓ navigate, enter restores the selected step, esc closes.
func (m *Model) handleCheckpointKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.checkpoints.Open = false
		return m, nil
	case "up", "k":
		if m.checkpoints.Selected > 0 {
			m.checkpoints.Selected--
		}
		return m, nil
	case "down", "j":
		if m.checkpoints.Selected < len(m.checkpoints.Entries)-1 {
			m.checkpoints.Selected++
		}
		return m, nil
	case "enter":
		if len(m.checkpoints.Entries) == 0 || len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
			return m, nil
		}
		s := m.sessions[m.selectedIdx]
		// RunService.Restore rejects RUNNING sessions with FailedPrecondition.
		// Reflect that inline in the modal rather than blocking; the
		// RPC error is the source of truth.
		step := m.checkpoints.Entries[m.checkpoints.Selected].Step
		return m, restoreCheckpointCmd(m.client, s.ID, step)
	}
	return m, nil
}

// parseCheckpointNote pulls the human-readable summary off a
// checkpoint_committed payload — preferring "note", falling back to a
// short SHA.
func parseCheckpointNote(raw []byte) string {
	type ck struct {
		Note string `json:"note"`
		SHA  string `json:"sha"`
	}
	var d ck
	_ = jsonUnmarshalQuiet(raw, &d)
	if d.Note != "" {
		return d.Note
	}
	if d.SHA != "" {
		return shortSHA(d.SHA)
	}
	return ""
}
