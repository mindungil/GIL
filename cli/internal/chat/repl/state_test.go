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
	Kind      string
	Iter      int
	MaxIter   int
	CostUSD   float64
	Status    string
	ChecksOK  int
	ChecksTot int
}

func (f fakeEvent) ToTrackerInput() TrackerInput {
	return TrackerInput{
		Kind:         f.Kind,
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

// iter211: the interview engine was deleted in M3, so the tracker no
// longer recognizes interview.* event kinds. The tests that used to
// pin slot_filled / adversary / ready_to_freeze / started / resumed
// transitions are gone with the producers — the goal-assembly path is
// the freeze_spec tool inside the chat agent's natural-language stream
// now, not a separate phase the strip needs to surface.

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
