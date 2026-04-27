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
			// Check for clarify_requested events and surface the
			// clarify modal. We always overwrite an existing
			// pendingClarify because the latest ask is the one the
			// runner is blocked on; older asks already timed out or
			// were cancelled when the run ended.
			if cl := parseClarifyRequested(msg.sessID, msg.ev.GetType(), msg.ev.GetDataJson()); cl != nil {
				m.pendingClarify = cl
				m.clarifyState = clarifyModalState{}
			}
			return m, nextEventCmd(msg.handle)
		}
		return m, nil

	case askAnswerMsg:
		if msg.err != "" {
			m.err = msg.err
		}
		return m, nil

	case clarifyAnswerMsg:
		// Symmetric to askAnswerMsg — we already cleared pendingClarify
		// when the user submitted; this just surfaces RPC errors.
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
		// When a clarify modal is open it owns ALL key input until the
		// user submits, types, or dismisses. Order matters: clarify
		// is checked BEFORE permission so a clarify that fires inside
		// a permission-asking iteration still gets the keystroke
		// (the model can't currently nest the two but defensively
		// handling both keeps a future "permission then clarify"
		// reorder safe).
		if m.pendingClarify != nil {
			if mm, cmd, handled := m.handleClarifyKey(msg); handled {
				return mm, cmd
			}
			// Swallow unhandled keys so the user can't refresh /
			// quit while a clarify is pending — same rule as the
			// permission modal.
			return m, nil
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

// handleClarifyKey implements the clarify modal's two-mode keymap.
//
// Pick mode (the default after the modal opens):
//   1..N (where N = len(suggestions)) — answer with that suggestion
//   t                                  — switch to type mode
//   esc / q                            — dismiss without answering
//                                        (the run will eventually time
//                                        out and the agent's tool_result
//                                        path will fire)
//
// Type mode (after pressing t):
//   <printable>                        — append to typing buffer
//   backspace                          — pop one rune
//   enter                              — send buffer as the answer
//   esc                                — back to pick mode (buffer cleared)
//
// Returns handled=true when the key was consumed; the caller swallows
// other keys so the user can't refresh / navigate while paused.
func (m *Model) handleClarifyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	ask := m.pendingClarify
	if ask == nil {
		return m, nil, false
	}
	k := msg.String()

	if m.clarifyState.mode == clarifyModeType {
		switch k {
		case "esc":
			m.clarifyState = clarifyModalState{mode: clarifyModePick}
			return m, nil, true
		case "enter":
			ans := m.clarifyState.typeBuf
			m.pendingClarify = nil
			m.clarifyState = clarifyModalState{}
			return m, answerClarifyCmd(m.client, ask.SessionID, ask.AskID, ans), true
		case "backspace", "ctrl+h":
			if len(m.clarifyState.typeBuf) > 0 {
				// Trim one rune (not byte) so multibyte chars don't
				// leave torn UTF-8 in the buffer.
				rs := []rune(m.clarifyState.typeBuf)
				m.clarifyState.typeBuf = string(rs[:len(rs)-1])
			}
			return m, nil, true
		default:
			// Append printable runes to the buffer. Bubbletea's
			// msg.Runes is non-empty for printable input; we
			// concatenate so multi-rune compositions (CJK IME)
			// flow through unchanged.
			if len(msg.Runes) > 0 {
				m.clarifyState.typeBuf += string(msg.Runes)
				return m, nil, true
			}
			return m, nil, true // swallow nav keys silently
		}
	}

	// Pick mode.
	switch k {
	case "t", "T":
		m.clarifyState = clarifyModalState{mode: clarifyModeType}
		return m, nil, true
	case "esc", "q", "Q":
		// Dismiss: clear the modal locally; the run-side timeout
		// (60min default) is the source of truth for cancellation.
		// We deliberately do NOT send an empty answer because that
		// is a valid response shape ("user said nothing useful"); a
		// dismissal should let the timeout fire instead.
		m.pendingClarify = nil
		m.clarifyState = clarifyModalState{}
		return m, nil, true
	}
	if idx := clarifyKeyToSuggestionIndex(k, ask.Suggestions); idx >= 0 {
		ans := ask.Suggestions[idx]
		m.pendingClarify = nil
		m.clarifyState = clarifyModalState{}
		return m, answerClarifyCmd(m.client, ask.SessionID, ask.AskID, ans), true
	}
	// Unknown key — swallow so navigation can't leak through.
	return m, nil, true
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
