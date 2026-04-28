package repl

import "github.com/mindungil/gil/cli/internal/chat/render"

// TrackerInput is the renderer-agnostic shape of a single event the
// tracker consumes. The REPL adapts sdk.Event → TrackerInput at the
// gRPC boundary (Task 8) so this module stays free of proto deps and
// is trivially unit-testable.
type TrackerInput struct {
	Kind         string
	SessionID    string
	DisplayName  string
	SlotsFilled  int
	SlotsTotal   int
	Saturation   float64
	AdvFindings  int
	Iter         int
	MaxIter      int
	CostUSD      float64
	Status       string
	ChecksPassed int
	ChecksTotal  int
	Autonomy     string
}

type Tracker struct {
	s render.SessionState
}

func NewTracker() *Tracker {
	return &Tracker{s: render.SessionState{Phase: render.PhaseIdle}}
}

func (t *Tracker) State() render.SessionState { return t.s }

// Apply mutates state in-place based on event kind. The kind strings
// are gil-internal event names; if Step 0 audit added new event types,
// extend this switch.
func (t *Tracker) Apply(in TrackerInput) {
	if in.SessionID != "" {
		t.s.SessionID = in.SessionID
	}
	if in.DisplayName != "" {
		t.s.DisplayName = in.DisplayName
	}
	if in.Autonomy != "" {
		t.s.Autonomy = in.Autonomy
	}

	switch in.Kind {
	case "interview.slot_filled":
		t.s.Phase = render.PhaseInterview
		t.s.SlotsFilled = in.SlotsFilled
		t.s.SlotsTotal = in.SlotsTotal
		t.s.Saturation = in.Saturation

	case "interview.adversary":
		// Phase stays whatever it was; only update count.
		t.s.AdvFindings = in.AdvFindings

	case "interview.ready_to_freeze":
		t.s.Phase = render.PhaseAwaitingConfirm

	case "run.started":
		t.s.Phase = render.PhaseRun
		t.s.Iter = 0
		if in.MaxIter > 0 {
			t.s.MaxIter = in.MaxIter
		}

	case "run.iter":
		t.s.Phase = render.PhaseRun
		t.s.Iter = in.Iter
		if in.CostUSD > 0 {
			t.s.CostUSD = in.CostUSD
		}

	case "run.stuck":
		t.s.Phase = render.PhaseStuck
		if in.Iter > 0 {
			t.s.Iter = in.Iter
		}
		if in.MaxIter > 0 {
			t.s.MaxIter = in.MaxIter
		}

	case "run.done":
		t.s.Phase = render.PhaseDone
		if in.Iter > 0 {
			t.s.Iter = in.Iter
		}
		if in.CostUSD > 0 {
			t.s.CostUSD = in.CostUSD
		}
		t.s.ChecksPassed = in.ChecksPassed
		t.s.ChecksTotal = in.ChecksTotal
	}
}
