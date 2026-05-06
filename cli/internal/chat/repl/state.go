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

	// Stuck-detector payload, populated when Kind is "run.stuck" or
	// "run.recovered" — see grpc_client.go's mapping of the runner's
	// stuck_detected / stuck_recovered events. Pattern names mirror
	// core/stuck/detector.go (PatternRepeatedActionObservation, …).
	StuckPattern string
	StuckDetail  string

	// Reason is the StageTransition.Reason payload — e.g.
	// "domain=cli-tooling confidence=0.85" on interview.started, or
	// the audit's "ready" reason on interview.ready_to_freeze. Empty
	// for events that don't carry one. Reused for retry's err string
	// when Kind == "provider.retry_attempt".
	Reason string

	// Retry-attempt payload, populated when Kind is
	// "provider.retry_attempt". Sent by core/provider/retry.go's
	// OnRetry hook before sleeping `RetryWaitMs` to retry as
	// attempt RetryAttempt of RetryMax. Surfaces in the chat REPL
	// as a transient SystemNote so backoff is visible.
	RetryAttempt int
	RetryMax     int
	RetryWaitMs  int64

	// Tokens is the EventMetrics.Tokens value for this single event.
	// Tracker.Apply sums it into SessionState.Tokens so the strip and
	// /tokens slash see a running total, not the per-event delta.
	Tokens int64
	// LatencyMs is the EventMetrics.LatencyMs value — the wall time
	// the provider took for the call this event represents. Snapshot,
	// not accumulated; surfaces the most recent latency in the strip.
	LatencyMs int64
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
	// Tokens accumulate; LatencyMs is a snapshot. EventMetrics carries
	// per-event values, so we sum tokens here once instead of in every
	// case below — keeps the kind-switch focused on phase logic and
	// avoids missing the accumulator on a future event type.
	if in.Tokens > 0 {
		t.s.Tokens += in.Tokens
	}
	if in.LatencyMs > 0 {
		t.s.LatencyMs = in.LatencyMs
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

	case "interview.started", "interview.resumed":
		t.s.Phase = render.PhaseInterview

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

	case "run.recovered":
		// Strategy actually unblocked the loop — flip back to PhaseRun
		// so the strip stops showing the alarming "stuck" state. The
		// SystemNote in loop.go carries the explanation text.
		t.s.Phase = render.PhaseRun

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
