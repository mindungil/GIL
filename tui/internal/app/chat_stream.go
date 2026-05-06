package app

import (
	"context"
	"errors"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// chatAssistantChunkMsg carries one streamed text chunk from the
// daemon. View appends to the open assistant line.
type chatAssistantChunkMsg struct{ text string }

// chatPhaseMsg signals a phase transition derived from a daemon
// event. Drives status strip + affordance subtitle + border color.
// Currently only fired by the run tail pump (chat_run.go); the M2
// chat path doesn't emit phase transitions because the agent loop
// doesn't have stages.
type chatPhaseMsg struct{ phase ChatPhase }

// chatStreamDoneMsg signals graceful EOF on a stream. The model
// resets cursor state so the next Enter starts a fresh turn.
type chatStreamDoneMsg struct{}

// chatStreamErrMsg surfaces stream errors to the status strip.
type chatStreamErrMsg struct{ err string }

// chatStreamEventSkipMsg is the "drop and re-pump" sentinel for
// stream events the chat surface doesn't yet render. Keeps the drain
// loop going without the chat code knowing every Part body kind.
type chatStreamEventSkipMsg struct{}

// startChatPromptCmd is the chat path. It calls
// sdk.Client.Prompt (SessionService.Prompt) and adapts the resulting
// Part stream into the chat surface's existing message types
// (chatAssistantChunkMsg / chatStreamErrMsg / chatStreamDoneMsg) plus
// new ones for tool calls/results and prompt metrics.
//
// The Part stream type doesn't share an alias with InterviewEvent
// streams (different message bodies), so this command goes through a
// dedicated chatPromptStreamStartedMsg + chatPromptEventCmd pump that
// runs alongside the interview pump until M3 deletes the latter.
func startChatPromptCmd(client *sdk.Client, sessionID, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.Prompt(ctx, sdk.PromptOptions{
			SessionID: sessionID,
			Text:      text,
		})
		if err != nil {
			cancel()
			return chatStreamErrMsg{err: err.Error()}
		}
		return chatPromptStreamStartedMsg{stream: stream, cancel: cancel}
	}
}

// --- chat-prompt pump ------------------------------------------------
//
// SessionService.Prompt uses a different streaming type and different
// message body shape than InterviewService.Start. The msgs and pump
// below mirror the interview pump's structure but speak Parts.

// chatPromptStreamStartedMsg is the post-Prompt-RPC equivalent of
// chatStreamStartedMsg. The model stores the stream + cancel and
// schedules the pump.
type chatPromptStreamStartedMsg struct {
	stream gilv1.SessionService_PromptClient
	cancel context.CancelFunc
}

// chatPromptToolCallMsg / chatPromptToolResultMsg surface tool
// invocations to the chat transcript. Mirrors the cli REPL's
// "tool.call" / "tool.result" TrackerInput kinds so the user sees
// what the agent is doing on each iteration.
type chatPromptToolCallMsg struct {
	id, name, inputJSON string
}
type chatPromptToolResultMsg struct {
	callID, content string
	isError         bool
}

// chatPromptSessionAllocatedMsg fires once when the daemon allocated
// a session in response to an empty session_id. The model pins
// activeID and continues draining.
type chatPromptSessionAllocatedMsg struct {
	sessionID string
}

// chatPromptMetricsMsg carries per-turn tokens / latency for the
// status pill.
type chatPromptMetricsMsg struct {
	tokensIn, tokensOut, latencyMs int64
}

// nextChatPromptEventCmd reads one Part from the prompt stream and
// dispatches to the right msg. Mirrors nextChatEventCmd shape so the
// chat model's Update can re-pump after each event.
func nextChatPromptEventCmd(stream gilv1.SessionService_PromptClient) tea.Cmd {
	return func() tea.Msg {
		ev, err := stream.Recv()
		if err != nil {
			// Both EOF and stream errors collapse to the existing
			// chatStreamDoneMsg / chatStreamErrMsg handlers — turn
			// is over either way.
			if errors.Is(err, io.EOF) {
				return chatStreamDoneMsg{}
			}
			return chatStreamErrMsg{err: err.Error()}
		}
		if t := ev.GetText(); t != nil {
			return chatAssistantChunkMsg{text: t.GetContent()}
		}
		if c := ev.GetToolCall(); c != nil {
			return chatPromptToolCallMsg{
				id: c.GetId(), name: c.GetName(), inputJSON: c.GetInputJson(),
			}
		}
		if r := ev.GetToolResult(); r != nil {
			return chatPromptToolResultMsg{
				callID: r.GetCallId(), content: r.GetContent(), isError: r.GetIsError(),
			}
		}
		if a := ev.GetSessionAllocated(); a != nil {
			return chatPromptSessionAllocatedMsg{sessionID: a.GetSessionId()}
		}
		if mt := ev.GetMetrics(); mt != nil {
			return chatPromptMetricsMsg{
				tokensIn:  mt.GetTokensIn(),
				tokensOut: mt.GetTokensOut(),
				latencyMs: mt.GetLatencyMs(),
			}
		}
		if d := ev.GetDone(); d != nil {
			if d.GetStopReason() == "error" {
				return chatStreamErrMsg{err: d.GetErrorMessage()}
			}
			return chatStreamDoneMsg{}
		}
		// Unknown body — re-pump.
		return chatStreamEventSkipMsg{}
	}
}

// stagePhase maps an interview StageTransition.To to the local
// ChatPhase. The wire stages emitted by the daemon are "sensing" /
// "conversation" / "confirm" — V1 collapses sensing+conversation
// to interview and confirm to awaiting-confirm.
func stagePhase(stage string) ChatPhase {
	switch stage {
	case "confirm":
		return ChatPhaseAwaitingConfirm
	case "sensing", "conversation":
		return ChatPhaseInterview
	default:
		return ChatPhaseIdle
	}
}
