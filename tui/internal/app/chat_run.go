package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// chatRunTailState owns the cursor for an in-flight RunService.Tail
// subscription. Mirrors the chatStreamState pattern from chat_stream.go
// but for run events instead of interview events. Storing the handle
// on the chat model lets `/quit` cancel a running tail cleanly.
type chatRunTailState struct {
	handle *chatRunTailHandle
}

type chatRunTailHandle struct {
	cancel context.CancelFunc
	stream gilv1.RunService_TailClient
	sessID string
}

// chatRunTailStartedMsg hands the freshly-opened Tail handle to the
// chat Update loop so it can begin pumping run events.
type chatRunTailStartedMsg struct{ handle *chatRunTailHandle }

// chatRunEventMsg carries one drained run event. The chat Update
// renders it into transcript entries and may flip the phase.
type chatRunEventMsg struct {
	ev     *gilv1.Event
	handle *chatRunTailHandle
}

// chatRunTailDoneMsg signals the run stream closed cleanly (run
// finished or session done). Update resets the tail cursor.
type chatRunTailDoneMsg struct{}

// chatRunTailErrMsg surfaces a stream error to the transcript.
type chatRunTailErrMsg struct{ err string }

// startChatRunTailCmd opens the Tail subscription and returns the
// handle via chatRunTailStartedMsg. The stream is then drained by
// repeated nextChatRunEventCmd calls scheduled from chatModel.Update.
func startChatRunTailCmd(client *sdk.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.TailRun(ctx, sessionID)
		if err != nil {
			cancel()
			return chatRunTailErrMsg{err: err.Error()}
		}
		return chatRunTailStartedMsg{handle: &chatRunTailHandle{
			cancel: cancel, stream: stream, sessID: sessionID,
		}}
	}
}

// nextChatRunEventCmd reads one event from the active Tail stream and
// converts it to the appropriate chat msg. Re-issued by Update after
// each event so the stream drains continuously, matching the
// nextChatEventCmd pattern from chat_stream.go.
func nextChatRunEventCmd(h *chatRunTailHandle) tea.Cmd {
	return func() tea.Msg {
		ev, err := h.stream.Recv()
		if err == io.EOF {
			return chatRunTailDoneMsg{}
		}
		if err != nil {
			return chatRunTailErrMsg{err: err.Error()}
		}
		return chatRunEventMsg{ev: ev, handle: h}
	}
}

// formatChatRunEvent maps a run event to (phase, transcript line(s),
// shouldDrain). When phase is non-empty, the chat model flips its
// phase strip. When transcript lines are non-empty, each is appended
// to the conversation transcript. Returning shouldDrain=false on
// terminal events (run.done) lets the caller stop pumping; otherwise
// the loop keeps draining.
//
// Mirrors the cli REPL's mapRunEventToTracker (cli/internal/chat/repl/
// grpc_client.go) but emits transcript-shaped strings instead of
// TrackerInput. Single source of truth for the human-readable strings:
// keep this in sync with cli/internal/chat/repl/loop.go's emitDeltaNotes
// as both surfaces should describe the same agent state identically.
func formatChatRunEvent(ev *gilv1.Event) (phase ChatPhase, lines []string, keepDraining bool) {
	keepDraining = true
	if ev == nil {
		return "", nil, true
	}
	switch ev.GetType() {
	case "run.started":
		return ChatPhaseRun, []string{"   ‹  agent run started"}, true
	case "run.iter":
		var cost float64
		if m := ev.GetMetrics(); m != nil {
			cost = m.GetCostUsd()
		}
		var iter int64
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if v, ok := d["iter"].(float64); ok {
				iter = int64(v)
			}
		}
		return ChatPhaseRun, []string{fmt.Sprintf("   ‹  iter %d · $%.4f", iter, cost)}, true
	case "stuck_detected":
		var d map[string]any
		_ = json.Unmarshal(ev.GetDataJson(), &d)
		pattern, _ := d["pattern"].(string)
		detail, _ := d["detail"].(string)
		msg := "stuck — agent loop detected"
		if pattern != "" {
			msg = "stuck — " + humanChatStuckPattern(pattern)
			if detail != "" {
				msg += " (" + detail + ")"
			}
		}
		msg += ". recovery in progress; if it persists, `gil stop <id>` from another shell"
		return ChatPhaseStuck, []string{"   !  " + msg}, true
	case "stuck_recovered":
		var d map[string]any
		_ = json.Unmarshal(ev.GetDataJson(), &d)
		explanation, _ := d["explanation"].(string)
		msg := "recovered — agent unblocked"
		if explanation != "" {
			msg += ": " + explanation
		}
		return ChatPhaseRun, []string{"   ‹  " + msg}, true
	case "run.done":
		return ChatPhaseDone, []string{"   ‹  run complete — /diff to review, /merge to apply"}, false
	case "run_error":
		var d map[string]any
		_ = json.Unmarshal(ev.GetDataJson(), &d)
		errMsg, _ := d["error"].(string)
		if errMsg == "" {
			errMsg = string(ev.GetDataJson())
		}
		return "", []string{"   !  run error: " + errMsg}, true
	case "agent_turn":
		// Run-side AgentTurn comes through the Event stream (not the
		// InterviewService stream that chat_stream.go handles). The
		// interview-side coalescing logic in chatModel.Update keys
		// off the leading "‹" so we use the same prefix here for
		// continuity — multiple agent_turn chunks in a row will fold
		// onto one line.
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if c, ok := d["content"].(string); ok && c != "" {
				return "", []string{"‹  " + c}, true
			}
		}
		return "", nil, true
	}
	// Unknown / not-yet-rendered event types pass silently — keep
	// draining so the next event arrives.
	return "", nil, true
}

// humanChatStuckPattern mirrors humanStuckPattern in cli/internal/
// chat/repl/loop.go so the chat surface and the cli REPL describe
// the same detector pattern with the same phrase. Duplicated locally
// to avoid a cross-module import.
func humanChatStuckPattern(p string) string {
	switch p {
	case "PatternRepeatedActionObservation":
		return "same tool result loop"
	case "PatternRepeatedActionError":
		return "same tool error loop"
	case "PatternMonologue":
		return "talking without acting"
	case "PatternPingPong":
		return "alternating tool ping-pong"
	case "PatternContextWindowError":
		return "context window overflow"
	case "PatternNoProgress":
		return "no file progress"
	}
	return p
}

// silence unused warning when the package builds without callers of
// strings via formatChatRunEvent's switch (the linter sees only
// transitive use).
var _ = strings.TrimSpace
