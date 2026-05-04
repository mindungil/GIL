package repl

import (
	"testing"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func TestMapRunEvent_StuckDetected_NamedPattern(t *testing.T) {
	// Regression: prior to this fix the adapter listened for
	// "run.stuck" but the runner emits "stuck_detected" — every
	// stuck loop appeared as a generic hang. The fix renames the
	// adapter case AND surfaces the rich payload (pattern + detail)
	// so the user sees WHICH of the 6 detector patterns triggered,
	// not just "stuck".
	ev := &gilv1.Event{
		Type:     "stuck_detected",
		DataJson: []byte(`{"pattern":"PatternMonologue","detail":"3 consecutive provider_response with zero tool_calls","count":3}`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "run.stuck", in.Kind)
	require.Equal(t, "PatternMonologue", in.StuckPattern)
	require.Contains(t, in.StuckDetail, "3 consecutive")
}

func TestMapRunEvent_StuckRecovered_FlipsToRecoveredKind(t *testing.T) {
	ev := &gilv1.Event{
		Type:     "stuck_recovered",
		DataJson: []byte(`{"strategy":"alt_tool_order","new_model":"","explanation":"swapped tool_use ordering"}`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "run.recovered", in.Kind)
	require.Equal(t, "swapped tool_use ordering", in.StuckDetail)
}

func TestMapRunEvent_LegacyRunStuckNameNoLongerFires(t *testing.T) {
	// The old "run.stuck" string isn't emitted by anything in the
	// codebase; matching on it would mask the renamed bug.
	ev := &gilv1.Event{Type: "run.stuck"}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "", in.Kind, "legacy event name must NOT be matched")
}

func TestMapRunEvent_StuckDetected_BadJSON_StillRoutesPhase(t *testing.T) {
	// Malformed payload must not block the phase change — pattern /
	// detail simply remain empty so the SystemNote falls back to a
	// generic "stuck — agent loop detected" message.
	ev := &gilv1.Event{
		Type:     "stuck_detected",
		DataJson: []byte(`not json`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "run.stuck", in.Kind)
	require.Equal(t, "", in.StuckPattern)
}
