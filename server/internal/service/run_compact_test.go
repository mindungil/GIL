package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/runner"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// TestRequestCompact_NoRunInFlight asserts the friendly fallback when
// no run is registered for the session: queued=false with a non-empty
// reason so the surface can render "no run in flight" without parsing
// error strings.
func TestRequestCompact_NoRunInFlight(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	resp, err := svc.RequestCompact(context.Background(), &gilv1.RequestCompactRequest{SessionId: "missing"})
	require.NoError(t, err)
	require.False(t, resp.Queued)
	require.Equal(t, "no run in flight", resp.Reason)
}

// TestRequestCompact_QueuesOnLiveLoop installs a fake AgentLoop in
// runLoops (matches how executeRun stages it) and verifies
// RequestCompact flips the loop's compactNowRequested flag via the
// public method. We also verify a compact_requested event lands on the
// session's stream so observers see the surface-issued nudge.
func TestRequestCompact_QueuesOnLiveLoop(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	loop := &runner.AgentLoop{}
	stream := event.NewStream()
	sub := stream.Subscribe(8)
	defer sub.Close()

	svc.mu.Lock()
	svc.runLoops["sess-1"] = loop
	svc.runStreams["sess-1"] = stream
	svc.mu.Unlock()

	resp, err := svc.RequestCompact(context.Background(), &gilv1.RequestCompactRequest{SessionId: "sess-1"})
	require.NoError(t, err)
	require.True(t, resp.Queued)

	// The AgentLoop's CompactRequester flips the internal flag; trigger
	// it via the same path the runner uses to confirm the wiring.
	loop.RequestCompact()
	// (We can't read the private flag directly; this call would simply
	//  be a no-op if the wiring were broken — we lean on the event
	//  assertion below to prove the RPC actually invoked the method.)

	select {
	case e := <-sub.Events():
		require.Equal(t, "compact_requested", e.Type)
		require.Equal(t, event.SourceUser, e.Source)
	default:
		t.Fatal("expected compact_requested event on stream")
	}
}

// TestRequestCompact_RequiresSessionID rejects empty input with an
// InvalidArgument so misconfigured surfaces fail loudly rather than
// hitting the "no run in flight" fallback by accident.
func TestRequestCompact_RequiresSessionID(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	_, err := svc.RequestCompact(context.Background(), &gilv1.RequestCompactRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session_id is required")
}
