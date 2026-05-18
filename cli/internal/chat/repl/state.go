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

	// Permission-ask payload (#2 followup wiring): populated when
	// Kind is "permission.ask". S9 subagent routing means the ask can
	// come from a child — FromSubagentLabel surfaces which one.
	RequestID         string
	PermissionTool    string
	PermissionKey     string
	FromSubagentLabel string

	// Reason is the optional context string attached to an event —
	// used by provider.retry_attempt to carry the retry's err string,
	// and by future event kinds that need a single free-form note.
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

	// Tool* fields populated when Kind == "tool.call" or "tool.result".
	// The chat surface renders these as inline transcript lines so the
	// user sees what tools the agent invoked. ToolID correlates the
	// call → result pair.
	ToolName    string
	ToolID      string
	ToolInput   string
	ToolContent string
	ToolIsError bool
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
	// interview.* event handlers removed in iter211 — the interview
	// engine that emitted slot_filled / adversary / started / resumed /
	// ready_to_freeze was deleted in M3. No producer remains; the chat
	// agent's freeze_spec tool is the single goal-assembly path.

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

	case "session.allocated":
		// Daemon auto-created a session in response to a Prompt with
		// empty session_id. Tracker doesn't change phase — the chat
		// surface already shows ChatPhaseIdle / interview as
		// appropriate; this kind exists so emitDeltaNotes can mention
		// the new id once.

	case "tool.call", "tool.result":
		// Tool invocations don't change phase. emitDeltaNotes prints
		// them as ⚒ lines in the transcript so the user sees what
		// the agent is doing.

	case "prompt.metrics":
		if in.Tokens > 0 {
			t.s.Tokens = in.Tokens
		}
		if in.LatencyMs > 0 {
			t.s.LatencyMs = in.LatencyMs
		}
		// P49: accumulate per-turn cost into the session running
		// total. Each prompt.metrics carries the cost for ONE turn,
		// not the lifetime — sum them so the status strip / banner
		// reflects total spend across the chat session.
		if in.CostUSD > 0 {
			t.s.CostUSD += in.CostUSD
		}
	}
}
