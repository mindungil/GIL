package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

const eventBufferSize = 200

// Model is the Bubbletea root model for the gil TUI.
type Model struct {
	socket string
	client *sdk.Client
	keys   KeyMap

	sessions    []*sdk.Session
	selectedIdx int
	width       int
	height      int
	err         string

	activeTail *tailHandle      // current Tail subscription; nil when none
	events     []string         // ring buffer of formatted event lines (legacy: kept for /clear)
	rawEvents  []*gilv1.Event   // ring buffer of raw events; drives activity + progress + memory
	progress   ProgressData     // derived view-model rebuilt on every event

	// Activity pane filter (milestones ↔ all). 't' toggles.
	activityFilter ActivityFilter

	// Memory excerpt — rebuilt opportunistically (on event arrival,
	// session change, refresh).
	memory MemoryExcerpt

	// Checkpoint modal.
	checkpoints CheckpointModalState

	// Spinner frame counter — incremented on tickMsg every 80ms while a
	// session is RUNNING.
	spinFrame int

	pendingAsk *pendingAskMsg // when non-nil, permission modal is shown

	// slash holds the slash-command registry + transient input/output
	// state. Constructed by New(); nil-safe in unit tests that build a
	// Model literal without dialing gild.
	slash *slashState
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
	m.rawEvents = nil
	m.progress = ProgressData{}
	m.checkpoints.Entries = nil
	m.checkpoints.Selected = 0
	m.refreshMemoryFromSelection()
	if len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
		return nil
	}
	s := m.sessions[m.selectedIdx]
	if s.Status != "RUNNING" {
		return nil
	}
	return startTail(m.client, s.ID)
}

// refreshMemoryFromSelection reads progress.md for the currently
// selected session and updates m.memory. No-op when no session is
// selected.
func (m *Model) refreshMemoryFromSelection() {
	if len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
		m.memory = MemoryExcerpt{NotFound: true}
		return
	}
	s := m.sessions[m.selectedIdx]
	m.memory = loadMemoryExcerpt(s.ID)
}

// rebuildProgressFromEvents recomputes the ProgressData view-model from
// the raw event buffer + the latest session snapshot. Called after
// every event arrival.
//
// Verify matrix is collapsed to one slice tracking the most recent
// verify_result payload's per-check outcomes; tokens are taken from
// the session row; cost is a coarse estimate (currently 0 — placeholder
// pending Phase 14 cost-meter wiring).
func (m *Model) rebuildProgressFromEvents() {
	if len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
		return
	}
	s := m.sessions[m.selectedIdx]
	pd := ProgressData{
		Goal:     s.GoalHint,
		Iter:     s.CurrentIteration,
		MaxIter:  100, // default budget surface; updated below from spec when available
		TokensIn: s.CurrentTokens,
		Autonomy: "ASK_DESTRUCTIVE",
	}
	// Walk events to derive verify outcomes, stuck state, latest iter.
	var stuckPattern, stuckRecovery string
	stuckExhausted := false
	for _, ev := range m.rawEvents {
		switch ev.GetType() {
		case "iteration_start":
			pd.Iter = parseIterFromEvent(ev.GetDataJson())
		case "verify_result":
			pd.VerifyResults = parseVerifyChecks(ev.GetDataJson())
		case "stuck_detected":
			stuckPattern = parseStuckPattern(ev.GetDataJson())
			stuckRecovery = ""
		case "stuck_recovered":
			stuckRecovery = parseStuckStrategy(ev.GetDataJson())
		case "stuck_unrecovered":
			stuckExhausted = true
		}
	}
	pd.StuckPattern = stuckPattern
	pd.StuckRecovery = stuckRecovery
	pd.StuckExhausted = stuckExhausted
	m.progress = pd
}

// New constructs a Model and dials the gild socket.
func New(socket string) (*Model, error) {
	cli, err := sdk.Dial(socket)
	if err != nil {
		return nil, err
	}
	m := &Model{
		socket:         socket,
		client:         cli,
		keys:           DefaultKeys(),
		activityFilter: FilterMilestones,
	}
	m.slash = initSlashState(cli)
	return m, nil
}

// Init returns the initial Cmd: load the session list + start the
// spinner ticker.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(loadSessionsCmd(m.client), spinnerTickCmd())
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

// spinnerTickCmd schedules the next 80ms spinner advance.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

type sessionsLoadedMsg struct{ sessions []*sdk.Session }
type errMsg struct{ message string }
type spinnerTickMsg struct{}
