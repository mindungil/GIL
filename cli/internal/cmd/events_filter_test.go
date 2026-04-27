package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func TestEventFilter_DefaultIsAll(t *testing.T) {
	f, err := newEventFilter(nil)
	require.NoError(t, err)
	require.True(t, f.all)
	require.True(t, f.matches(&gilv1.Event{Type: "anything_at_all"}))
}

func TestEventFilter_Milestones(t *testing.T) {
	f, err := newEventFilter([]string{"milestones"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "iteration_start"}))
	require.True(t, f.matches(&gilv1.Event{Type: "verify_result"}))
	require.True(t, f.matches(&gilv1.Event{Type: "stuck_detected"}))
	require.True(t, f.matches(&gilv1.Event{Type: "run_done"}))
	require.False(t, f.matches(&gilv1.Event{Type: "tool_call"}))
	require.False(t, f.matches(&gilv1.Event{Type: "provider_request"}))
}

func TestEventFilter_Errors(t *testing.T) {
	f, err := newEventFilter([]string{"errors"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "checkpoint_init_error"}))
	require.True(t, f.matches(&gilv1.Event{Type: "stuck_reset_failed"}))
	require.True(t, f.matches(&gilv1.Event{Type: "stuck_detected"}))
	require.False(t, f.matches(&gilv1.Event{Type: "iteration_start"}))
}

func TestEventFilter_Tools(t *testing.T) {
	f, err := newEventFilter([]string{"tools"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "tool_call"}))
	require.True(t, f.matches(&gilv1.Event{Type: "tool_result"}))
	require.True(t, f.matches(&gilv1.Event{Type: "tool_step"}))
	require.False(t, f.matches(&gilv1.Event{Type: "verify_result"}))
}

func TestEventFilter_Agent(t *testing.T) {
	f, err := newEventFilter([]string{"agent"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "provider_request"}))
	require.True(t, f.matches(&gilv1.Event{Type: "provider_response"}))
	require.True(t, f.matches(&gilv1.Event{Type: "compact_start"}))
	require.True(t, f.matches(&gilv1.Event{Type: "compact_done"}))
	require.False(t, f.matches(&gilv1.Event{Type: "tool_call"}))
}

func TestEventFilter_UnionViaCommas(t *testing.T) {
	f, err := newEventFilter([]string{"milestones,errors"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "iteration_start"}))
	require.True(t, f.matches(&gilv1.Event{Type: "checkpoint_init_error"}))
	require.False(t, f.matches(&gilv1.Event{Type: "tool_call"}))
}

func TestEventFilter_UnionViaRepeatFlag(t *testing.T) {
	f, err := newEventFilter([]string{"tools", "errors"})
	require.NoError(t, err)
	require.True(t, f.matches(&gilv1.Event{Type: "tool_result"}))
	require.True(t, f.matches(&gilv1.Event{Type: "run_error"}))
	require.False(t, f.matches(&gilv1.Event{Type: "iteration_start"}))
}

func TestEventFilter_UnknownSetIsUserError(t *testing.T) {
	_, err := newEventFilter([]string{"hyperdrive"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown --filter")
}

// TestTailEventsVisualFiltered_RendersAndFilters is an integration-ish
// test: drive the full filter+render loop with a mock stream and assert
// the visual output dropped non-matching lines.
func TestTailEventsVisualFiltered_RendersAndFilters(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	tm := time.Date(2026, 4, 26, 18, 34, 21, 0, time.UTC)
	events := []*gilv1.Event{
		{Timestamp: timestamppb.New(tm), Type: "tool_call", DataJson: []byte(`{"name":"bash"}`)},
		{Timestamp: timestamppb.New(tm), Type: "iteration_start", DataJson: []byte(`{"iter":1}`)},
		{Timestamp: timestamppb.New(tm), Type: "provider_request"},
	}
	mock := &mockTailClient{events: events}
	f, err := newEventFilter([]string{"milestones"})
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, tailEventsVisualFiltered(context.Background(), mock, &buf, f, false))
	out := buf.String()
	require.Contains(t, out, "iteration_start")
	require.NotContains(t, out, "tool_call", "filter milestones must drop tool_call")
	require.NotContains(t, out, "provider_request", "filter milestones must drop provider_request")
	require.True(t, strings.Contains(out, "18:34:21"), "expected HHMMSS timestamp")
}

func TestTailEventsJSONFiltered_OmitsNonMatching(t *testing.T) {
	tm := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	events := []*gilv1.Event{
		{Timestamp: timestamppb.New(tm), Type: "tool_call"},
		{Timestamp: timestamppb.New(tm), Type: "iteration_start"},
	}
	mock := &mockTailClient{events: events}
	f, err := newEventFilter([]string{"milestones"})
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, tailEventsJSONFiltered(context.Background(), mock, &buf, f))
	require.Contains(t, buf.String(), `"type":"iteration_start"`)
	require.NotContains(t, buf.String(), `"type":"tool_call"`)
}
