package repl

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeEvent mimics the relevant fields of sdk.Event we care about.
// We declare a minimal local enum so this test stays decoupled from
// the proto package while we wire the real types.
type fakeEvent struct {
	Kind        string
	SlotsFilled int
	SlotsTotal  int
	Saturation  float64
	AdvFindings int
	Iter        int
	MaxIter     int
	CostUSD     float64
	Status      string
	ChecksOK    int
	ChecksTot   int
}

func (f fakeEvent) ToTrackerInput() TrackerInput {
	return TrackerInput{
		Kind:         f.Kind,
		SlotsFilled:  f.SlotsFilled,
		SlotsTotal:   f.SlotsTotal,
		Saturation:   f.Saturation,
		AdvFindings:  f.AdvFindings,
		Iter:         f.Iter,
		MaxIter:      f.MaxIter,
		CostUSD:      f.CostUSD,
		Status:       f.Status,
		ChecksPassed: f.ChecksOK,
		ChecksTotal:  f.ChecksTot,
	}
}

func TestTracker_StartsIdle(t *testing.T) {
	tr := NewTracker()
	require.Equal(t, render.PhaseIdle, tr.State().Phase)
}

func TestTracker_InterviewSlotProgress(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "interview.slot_filled", SlotsFilled: 4, SlotsTotal: 11, Saturation: 0.36}.ToTrackerInput())
	s := tr.State()
	require.Equal(t, render.PhaseInterview, s.Phase)
	require.Equal(t, 4, s.SlotsFilled)
	require.Equal(t, 11, s.SlotsTotal)
	require.InDelta(t, 0.36, s.Saturation, 0.001)
}

func TestTracker_AdversaryFindingAccumulates(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "interview.adversary", AdvFindings: 1}.ToTrackerInput())
	require.Equal(t, 1, tr.State().AdvFindings)
	tr.Apply(fakeEvent{Kind: "interview.adversary", AdvFindings: 3}.ToTrackerInput())
	require.Equal(t, 3, tr.State().AdvFindings, "tracker overwrites with latest count")
}

func TestTracker_SaturationReadyTransitionsToAwaitingConfirm(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "interview.ready_to_freeze"}.ToTrackerInput())
	require.Equal(t, render.PhaseAwaitingConfirm, tr.State().Phase)
}

func TestTracker_RunStartsAndIters(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "run.started", MaxIter: 100}.ToTrackerInput())
	require.Equal(t, render.PhaseRun, tr.State().Phase)
	require.Equal(t, 100, tr.State().MaxIter)

	tr.Apply(fakeEvent{Kind: "run.iter", Iter: 23, CostUSD: 0.61}.ToTrackerInput())
	s := tr.State()
	require.Equal(t, 23, s.Iter)
	require.InDelta(t, 0.61, s.CostUSD, 0.001)
}

func TestTracker_StuckSignal(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "run.started", MaxIter: 100}.ToTrackerInput())
	tr.Apply(fakeEvent{Kind: "run.stuck", Iter: 45, MaxIter: 100}.ToTrackerInput())
	require.Equal(t, render.PhaseStuck, tr.State().Phase)
}

func TestTracker_InterviewStarted_FlipsToInterviewPhase(t *testing.T) {
	// sensing → conversation transition. Before #39's fix this was
	// silently dropped; now it must flip the phase so the strip
	// stops saying "idle" while the engine is actively interviewing.
	tr := NewTracker()
	tr.Apply(TrackerInput{Kind: "interview.started", Reason: "domain=cli-tooling confidence=0.85"})
	require.Equal(t, render.PhaseInterview, tr.State().Phase)
}

func TestTracker_InterviewResumed_FlipsToInterviewPhase(t *testing.T) {
	tr := NewTracker()
	tr.Apply(TrackerInput{Kind: "interview.resumed"})
	require.Equal(t, render.PhaseInterview, tr.State().Phase)
}

func TestTracker_RecoveredFlipsBackToRun(t *testing.T) {
	// Stuck-then-recovered must restore PhaseRun so the strip stops
	// showing the alarming stuck state. Without this, the chat
	// surface would keep saying "stuck" forever after the recovery
	// strategy succeeded — the user would think something's broken
	// when in fact the agent moved on.
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "run.started", MaxIter: 100}.ToTrackerInput())
	tr.Apply(fakeEvent{Kind: "run.stuck", Iter: 45, MaxIter: 100}.ToTrackerInput())
	require.Equal(t, render.PhaseStuck, tr.State().Phase)
	tr.Apply(TrackerInput{Kind: "run.recovered"})
	require.Equal(t, render.PhaseRun, tr.State().Phase)
}

func TestTracker_DoneWithChecks(t *testing.T) {
	tr := NewTracker()
	tr.Apply(fakeEvent{Kind: "run.done", Iter: 87, CostUSD: 2.34, ChecksOK: 4, ChecksTot: 4}.ToTrackerInput())
	s := tr.State()
	require.Equal(t, render.PhaseDone, s.Phase)
	require.Equal(t, 4, s.ChecksPassed)
	require.Equal(t, 4, s.ChecksTotal)
}
