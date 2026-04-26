package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/sdk"
)

// tailHandle owns an active gRPC Tail stream for a single session.
// Calling cancel terminates the stream and its background context.
type tailHandle struct {
	cancel context.CancelFunc
	stream gilv1.RunService_TailClient
	sessID string
}

// startTail initiates a Tail subscription for sessionID. Returns a Cmd that
// performs the gRPC handshake and returns tailStartedMsg on success or
// tailErrMsg on failure.
func startTail(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.TailRun(ctx, sessionID)
		if err != nil {
			cancel()
			return tailErrMsg{sessID: sessionID, err: err.Error()}
		}
		h := &tailHandle{cancel: cancel, stream: stream, sessID: sessionID}
		return tailStartedMsg{handle: h}
	}
}

// nextEventCmd reads ONE event from the stream and returns a Cmd that delivers
// it as an eventMsg. Chain calls to drain the stream continuously.
func nextEventCmd(h *tailHandle) tea.Cmd {
	return func() tea.Msg {
		ev, err := h.stream.Recv()
		if err == io.EOF {
			return tailEndedMsg{sessID: h.sessID}
		}
		if err != nil {
			return tailErrMsg{sessID: h.sessID, err: err.Error()}
		}
		return eventMsg{sessID: h.sessID, ev: ev, handle: h}
	}
}

// formatEvent renders one event as a single line for the events pane.
// Format: "<HH:MM:SS> <SOURCE>/<KIND> <type> <data_json>".
func formatEvent(ev *gilv1.Event) string {
	var ts string
	if t := ev.GetTimestamp(); t != nil {
		ts = t.AsTime().Format("15:04:05")
	} else {
		ts = "--:--:--"
	}
	src := strings.TrimPrefix(ev.GetSource().String(), "SOURCE_")
	kind := strings.TrimPrefix(ev.GetKind().String(), "KIND_")
	data := string(ev.GetDataJson())
	if data == "" {
		data = "{}"
	}
	return fmt.Sprintf("%s %s/%s %s %s", ts, src, kind, ev.GetType(), data)
}

// Message types for the tail subscription lifecycle.

type tailStartedMsg struct{ handle *tailHandle }

type eventMsg struct {
	sessID string
	ev     *gilv1.Event
	handle *tailHandle
}

type tailEndedMsg struct{ sessID string }

type tailErrMsg struct {
	sessID string
	err    string
}

// silence unused import
var _ = time.Second
