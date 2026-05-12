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

func TestMapRunEvent_CompactionLifecycle(t *testing.T) {
	// Compaction events were dropped on the chat floor — users
	// would see suspicious gaps when the runner crossed the threshold
	// or `/compact` was forced. Adapter now extracts forced/estimate/
	// before-after counts/saved tokens so emitDeltaNotes can render
	// the lifecycle visibly.
	start := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "compact_start",
		DataJson: []byte(`{"estimated_tokens":12500,"threshold":10000,"forced":false}`),
	})
	require.Equal(t, "compact_start", start.Kind)
	require.EqualValues(t, 12500, start.Tokens)

	forced := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "compact_start",
		DataJson: []byte(`{"estimated_tokens":1000,"threshold":10000,"forced":true}`),
	})
	require.Equal(t, "compact_start", forced.Kind)
	require.Contains(t, forced.Reason, "forced")

	done := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "compact_done",
		DataJson: []byte(`{"original":24,"compacted":6,"saved_tokens":4321,"skipped":false}`),
	})
	require.Equal(t, "compact_done", done.Kind)
	require.Equal(t, 24, done.RetryAttempt)
	require.Equal(t, 6, done.RetryMax)
	require.EqualValues(t, 4321, done.Tokens)

	cerr := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "compact_error",
		DataJson: []byte(`{"err":"compactor: provider returned empty response"}`),
	})
	require.Equal(t, "compact_error", cerr.Kind)
	require.Contains(t, cerr.Reason, "empty response")
}

func TestMapRunEvent_SubagentLifecycle(t *testing.T) {
	// Subagent events were dropped on the floor — long sub-loops
	// looked like the parent stopped working. Adapter forwards the
	// goal on start and (truncated) summary + iter count on done.
	start := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "subagent_started",
		DataJson: []byte(`{"goal":"investigate dead-letter queue growth","max_iterations":8,"max_tokens":30000,"model":"haiku","tools":["read_file","grep"]}`),
	})
	require.Equal(t, "subagent_started", start.Kind)
	require.Contains(t, start.Reason, "investigate")

	done := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "subagent_done",
		DataJson: []byte(`{"goal":"x","status":"complete","iterations":5,"tokens":4321,"summary":"DLQ was reaching cap; consumer crashed in handlerv2"}`),
	})
	require.Equal(t, "subagent_done", done.Kind)
	require.Equal(t, 5, done.RetryAttempt) // reused field as iteration count
	require.Contains(t, done.Reason, "DLQ")
}

func TestMapRunEvent_BudgetWarningAndExceeded(t *testing.T) {
	warn := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "budget_warning",
		DataJson: []byte(`{"reason":"cost","used":1.50,"limit":2.00,"threshold":0.75}`),
	})
	require.Equal(t, "budget_warning", warn.Kind)
	require.Equal(t, "cost", warn.Reason)
	require.InDelta(t, 1.50, warn.CostUSD, 1e-9)

	exceeded := mapRunEventToTracker("01HQ", &gilv1.Event{
		Type:     "budget_exceeded",
		DataJson: []byte(`{"reason":"tokens","used":50000,"limit":40000}`),
	})
	require.Equal(t, "budget_exceeded", exceeded.Kind)
	require.Equal(t, "tokens", exceeded.Reason)
}

func TestMapRunEvent_RetryAttempt_PopulatesPayload(t *testing.T) {
	// Regression: chat REPL ignored provider.retry_attempt events, so
	// flaky upstreams looked like 30s hangs. Adapter now extracts the
	// attempt counter, the wait window, and the upstream error so
	// emitDeltaNotes can render "retry 2/4 in 1.0s — 503 service unavailable".
	ev := &gilv1.Event{
		Type:     "provider.retry_attempt",
		DataJson: []byte(`{"attempt":2,"max_attempts":4,"wait_ms":1000,"err":"status 503 service unavailable"}`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "provider.retry_attempt", in.Kind)
	require.Equal(t, 2, in.RetryAttempt)
	require.Equal(t, 4, in.RetryMax)
	require.EqualValues(t, 1000, in.RetryWaitMs)
	require.Contains(t, in.Reason, "503")
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

func TestMapRunEvent_PermissionAsk_LandsAsTrackerInput(t *testing.T) {
	// Followup #2 wiring — pre-M3 dropped these events. After this
	// patch the REPL adapter maps permission_ask → "permission.ask"
	// and pulls request_id / tool / key / from_subagent_label.
	ev := &gilv1.Event{
		Type: "permission_ask",
		DataJson: []byte(`{
			"request_id": "01KREQ123",
			"tool": "bash",
			"key": "rm -rf /tmp/x",
			"from_subagent_label": "explore-auth"
		}`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "permission.ask", in.Kind)
	require.Equal(t, "01KREQ123", in.RequestID)
	require.Equal(t, "bash", in.PermissionTool)
	require.Equal(t, "rm -rf /tmp/x", in.PermissionKey)
	require.Equal(t, "explore-auth", in.FromSubagentLabel)
}

func TestMapRunEvent_PermissionAsk_RootSessionNoLabel(t *testing.T) {
	// Root session asks don't carry from_subagent_label. The field
	// stays empty; loop.go renders without the "subagent X " prefix.
	ev := &gilv1.Event{
		Type:     "permission_ask",
		DataJson: []byte(`{"request_id":"01KR","tool":"bash","key":"ls"}`),
	}
	in := mapRunEventToTracker("01HQ", ev)
	require.Equal(t, "permission.ask", in.Kind)
	require.Equal(t, "", in.FromSubagentLabel)
}
