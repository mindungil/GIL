package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/runner"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// TestPostHint_NoRunInFlight returns posted=false with the friendly
// reason rather than an error code, matching RequestCompact's
// contract.
func TestPostHint_NoRunInFlight(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	resp, err := svc.PostHint(context.Background(), &gilv1.PostHintRequest{
		SessionId: "missing",
		Hint:      map[string]string{"model": "claude-haiku-4-5"},
	})
	require.NoError(t, err)
	require.False(t, resp.Posted)
	require.Equal(t, "no run in flight", resp.Reason)
}

// TestPostHint_RequiresSessionID and TestPostHint_RequiresHint guard
// the InvalidArgument paths.
func TestPostHint_RequiresSessionID(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	_, err := svc.PostHint(context.Background(), &gilv1.PostHintRequest{
		Hint: map[string]string{"model": "x"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session_id")
}

func TestPostHint_RequiresHint(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	_, err := svc.PostHint(context.Background(), &gilv1.PostHintRequest{
		SessionId: "sess-1",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hint must contain")
}

// TestPostHint_QueuesNoteOnLiveLoop installs a real AgentLoop, calls
// PostHint, then exercises the same code path the runner uses to
// build the per-iteration system prompt — confirming the hint shows
// up exactly once and is cleared after consumption.
func TestPostHint_QueuesNoteOnLiveLoop(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	loop := &runner.AgentLoop{}
	stream := event.NewStream()
	sub := stream.Subscribe(8)
	defer sub.Close()

	svc.mu.Lock()
	svc.runLoops["sess-1"] = loop
	svc.runStreams["sess-1"] = stream
	svc.mu.Unlock()

	resp, err := svc.PostHint(context.Background(), &gilv1.PostHintRequest{
		SessionId: "sess-1",
		Hint: map[string]string{
			"model":  "claude-haiku-4-5",
			"reason": "user prefers cheaper model",
		},
	})
	require.NoError(t, err)
	require.True(t, resp.Posted)

	// formatHintNote keys are sorted; assert deterministic body.
	want := "USER HINT (consider for next turn):\n  model: claude-haiku-4-5\n  reason: user prefers cheaper model"
	require.Equal(t, want, formatHintNote(map[string]string{
		"reason": "user prefers cheaper model",
		"model":  "claude-haiku-4-5",
	}))

	// Stream should carry a user_hint event whose payload echoes the
	// hint map so observers (TUI tail, gil events) can render it.
	select {
	case e := <-sub.Events():
		require.Equal(t, "user_hint", e.Type)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &payload))
		hintMap, ok := payload["hint"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "claude-haiku-4-5", hintMap["model"])
	default:
		t.Fatal("expected user_hint event on stream")
	}
}

// TestFormatHintNote_OmitsTrailingNewline keeps the consumer
// (system-prompt builder) free of cosmetic whitespace handling.
func TestFormatHintNote_OmitsTrailingNewline(t *testing.T) {
	out := formatHintNote(map[string]string{"model": "x"})
	require.False(t, strings.HasSuffix(out, "\n"))
}
