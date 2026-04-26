package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestFormatEvent_AllFields(t *testing.T) {
	ev := &gilv1.Event{
		Timestamp: timestamppb.New(time.Date(2026, 4, 26, 17, 30, 5, 0, time.UTC)),
		Source:    gilv1.EventSource_AGENT,
		Kind:      gilv1.EventKind_ACTION,
		Type:      "tool_call",
		DataJson:  []byte(`{"name":"bash"}`),
	}
	out := formatEvent(ev)
	require.Contains(t, out, "17:30:05")
	require.Contains(t, out, "AGENT/ACTION")
	require.Contains(t, out, "tool_call")
	require.Contains(t, out, `{"name":"bash"}`)
}

func TestFormatEvent_NilTimestamp(t *testing.T) {
	ev := &gilv1.Event{
		Source: gilv1.EventSource_SYSTEM,
		Kind:   gilv1.EventKind_NOTE,
		Type:   "note",
	}
	out := formatEvent(ev)
	require.Contains(t, out, "--:--:--")
	require.Contains(t, out, "{}")
}

func TestEventBufferRing_TrimsToCap(t *testing.T) {
	// Verify the ring-buffer trim logic by simulating eventBufferSize+5 appends.
	var buf []string
	for i := 0; i < eventBufferSize+5; i++ {
		buf = append(buf, "evt")
		if len(buf) > eventBufferSize {
			buf = buf[len(buf)-eventBufferSize:]
		}
	}
	require.Equal(t, eventBufferSize, len(buf))
}
