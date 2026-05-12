package app

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// makeEvent is a small constructor for protobuf Events used by the
// activity-pane fixtures.
func makeEvent(typ string, dataJSON string, ts time.Time) *gilv1.Event {
	return &gilv1.Event{
		Timestamp: timestamppb.New(ts),
		Source:    gilv1.EventSource_AGENT,
		Kind:      gilv1.EventKind_ACTION,
		Type:      typ,
		DataJson:  []byte(dataJSON),
	}
}

func TestActivityFromEvents_FilterMilestones(t *testing.T) {
	now := time.Date(2026, 4, 26, 18, 34, 0, 0, time.UTC)
	events := []*gilv1.Event{
		makeEvent("iteration_start", `{"iter":22}`, now),
		makeEvent("provider_request", `{}`, now.Add(time.Second)),
		makeEvent("tool_call", `{"name":"bash","input":{"command":"git diff"}}`, now.Add(2*time.Second)),
		makeEvent("tool_result", `{"name":"bash"}`, now.Add(3*time.Second)),
		makeEvent("verify_result", `{"checks":[{"pass":true},{"pass":true}],"summary":"ok"}`, now.Add(4*time.Second)),
		makeEvent("checkpoint_committed", `{"sha":"abc1234","note":"baseline"}`, now.Add(5*time.Second)),
	}
	rows := activityFromEvents(events, FilterMilestones, 10)
	// Milestones excluded: provider_request, tool_call, tool_result.
	verbs := []string{}
	for _, r := range rows {
		verbs = append(verbs, r.Verb)
	}
	require.NotContains(t, strings.Join(verbs, " "), "bash")
	require.NotContains(t, strings.Join(verbs, " "), "llm")
	// Milestones present:
	joined := strings.Join(verbs, " ")
	require.Contains(t, joined, "iter")
	require.Contains(t, joined, "verify")
	require.Contains(t, joined, "checkpoint")
}

func TestActivityFromEvents_FilterAll(t *testing.T) {
	now := time.Date(2026, 4, 26, 18, 34, 0, 0, time.UTC)
	events := []*gilv1.Event{
		makeEvent("iteration_start", `{"iter":22}`, now),
		makeEvent("tool_call", `{"name":"bash","input":{"command":"ls"}}`, now.Add(time.Second)),
	}
	rows := activityFromEvents(events, FilterAll, 10)
	require.Len(t, rows, 2)
	require.Equal(t, "bash", rows[1].Verb)
}

func TestActivityFromEvents_LatestSpinningWhileToolPending(t *testing.T) {
	now := time.Date(2026, 4, 26, 18, 34, 0, 0, time.UTC)
	events := []*gilv1.Event{
		makeEvent("iteration_start", `{"iter":1}`, now),
		makeEvent("tool_call", `{"name":"bash","input":{"command":"sleep 1"}}`, now.Add(time.Second)),
	}
	rows := activityFromEvents(events, FilterAll, 10)
	require.True(t, rows[len(rows)-1].IsLatest)
	require.True(t, rows[len(rows)-1].Spinning)
}

func TestActivityFromEvents_LatestNotSpinningAfterToolResult(t *testing.T) {
	now := time.Date(2026, 4, 26, 18, 34, 0, 0, time.UTC)
	events := []*gilv1.Event{
		makeEvent("iteration_start", `{"iter":1}`, now),
		makeEvent("tool_call", `{"name":"bash","input":{"command":"x"}}`, now.Add(time.Second)),
		makeEvent("tool_result", `{"name":"bash"}`, now.Add(2*time.Second)),
	}
	rows := activityFromEvents(events, FilterAll, 10)
	require.False(t, rows[len(rows)-1].Spinning)
}

func TestRenderActivityPane_RendersAllRows(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	rows := []ActivityRow{
		{Timestamp: "18:34", Iter: 22, Verb: "bash", Summary: "git diff HEAD"},
		{Timestamp: "18:35", Iter: 22, Verb: "verify ✓", Summary: "tests pass"},
		{Timestamp: "18:36", Iter: 23, Verb: "edit", Summary: "src/app.tsx"},
	}
	out := renderActivityPane(80, 10, rows, 0)
	require.Contains(t, out, "18:34")
	require.Contains(t, out, "iter 22")
	require.Contains(t, out, "bash")
	require.Contains(t, out, "git diff HEAD")
	require.Contains(t, out, "▏") // QuoteBar
}

func TestRenderActivityPane_LatestSpinning(t *testing.T) {
	unicodeOnly(t)
	nocolor(t)
	rows := []ActivityRow{
		{Timestamp: "18:36", Iter: 23, Verb: "edit", Summary: "src/app.tsx", IsLatest: true, Spinning: true},
	}
	out := renderActivityPane(80, 10, rows, 0)
	require.Contains(t, out, "thinking")
	// Spinner glyph from the Braille set:
	hasFrame := false
	for _, f := range Glyphs().Spinner {
		if strings.Contains(out, f) {
			hasFrame = true
			break
		}
	}
	require.True(t, hasFrame, "expected spinner glyph in %q", out)
}

func TestRenderActivityPane_Empty(t *testing.T) {
	nocolor(t)
	out := renderActivityPane(80, 10, nil, 0)
	require.Contains(t, out, "no activity")
}

func TestParseVerifyChecks_BothShapes(t *testing.T) {
	a := parseVerifyChecks([]byte(`{"checks":[{"pass":true},{"pass":false}]}`))
	require.Equal(t, []string{"pass", "fail"}, a)
	b := parseVerifyChecks([]byte(`{"results":["pass","skip","fail"]}`))
	require.Equal(t, []string{"pass", "skip", "fail"}, b)
}
