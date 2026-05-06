package app

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// chatStreamState carries the cursor for an in-flight interview
// stream. The bubbletea pattern (see tui/internal/app/tail.go for
// the watch surface's analogue) is:
//
//   1. submit → startInterviewCmd → chatStreamStartedMsg{stream}
//   2. Update stores the stream handle, returns nextChatEventCmd
//   3. nextChatEventCmd → stream.Recv() → chatStreamEventMsg{event}
//   4. Update processes the event AND returns nextChatEventCmd again
//   5. on EOF / error, emits chatStreamDoneMsg / chatStreamErrMsg
//
// Storing the stream on the model lets the user's next Enter cancel
// the previous turn cleanly via cancel().
type chatStreamState struct {
	stream gilv1.InterviewService_StartClient
	cancel context.CancelFunc
}

// chatAssistantChunkMsg carries one streamed text chunk from the
// daemon. View appends to the open assistant line.
type chatAssistantChunkMsg struct{ text string }

// chatPhaseMsg signals a phase transition derived from a daemon
// event. Drives status strip + affordance subtitle + border color.
type chatPhaseMsg struct{ phase ChatPhase }

// chatStreamStartedMsg hands the freshly-opened stream handle and
// its cancel function to the Update loop so the model can pump it
// with nextChatEventCmd.
type chatStreamStartedMsg struct {
	stream gilv1.InterviewService_StartClient
	cancel context.CancelFunc
}

// chatStreamDoneMsg signals graceful EOF on the stream — usually
// after a stage transition to confirm. The model resets the
// stream cursor so the next Enter starts a fresh leg.
type chatStreamDoneMsg struct{}

// chatStreamErrMsg surfaces stream errors to the status strip.
type chatStreamErrMsg struct{ err string }

// chatStreamEventSkipMsg is the "drop and re-pump" sentinel for
// events the chat surface doesn't yet render. Keeps the drain loop
// going without the chat code having to know every event type the
// daemon might emit.
type chatStreamEventSkipMsg struct{}

// chatSaturationMsg surfaces interview progress (slots filled out of
// total). Mirrors the cli REPL's interview.slot_filled SystemNote so
// the user watches the spec fill in real time.
type chatSaturationMsg struct {
	filled, total int
	saturation    float64
}

// chatAdversaryMsg surfaces the adversary critique count. Mirrors the
// cli REPL's interview.adversary SystemNote.
type chatAdversaryMsg struct{ count int }

// chatStageReasonMsg carries the stage transition's Reason field
// (e.g. "domain=cli-tooling confidence=0.85" on sensing→conversation,
// or the audit's "ready" reason on conversation→confirm). Keeps the
// existing chatPhaseMsg shape minimal — only Update paths that want
// the human-readable reason consume it.
type chatStageReasonMsg struct {
	phase  ChatPhase
	reason string
}

// startInterviewCmd opens the interview stream and returns the
// handle via chatStreamStartedMsg. The stream is then drained by
// repeated nextChatEventCmd calls scheduled from Update.
func startInterviewCmd(client *sdk.Client, sessionID, firstInput string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.StartInterview(ctx, sessionID, firstInput, "", "", sdk.InterviewModels{})
		if err != nil {
			cancel()
			return chatStreamErrMsg{err: err.Error()}
		}
		return chatStreamStartedMsg{stream: stream, cancel: cancel}
	}
}

// replyInterviewCmd is the post-first-turn analogue of
// startInterviewCmd. After the daemon has begun an interview for a
// session, subsequent user replies must use ReplyInterview so the
// engine continues the conversation instead of restarting sensing
// (which it does on every Start call). Without this distinction the
// TUI was driving sensing on every keystroke — domain classification
// re-ran for "예 ?" / "응 ?" and produced new "interview started" notes
// instead of forwarding the reply to the open conversation.
//
// Returns the same chatStreamStartedMsg shape because the Reply
// stream type is identical to Start (both are aliased to
// grpc.ServerStreamingClient[InterviewEvent]).
func replyInterviewCmd(client *sdk.Client, sessionID, content string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.ReplyInterview(ctx, sessionID, content)
		if err != nil {
			cancel()
			return chatStreamErrMsg{err: err.Error()}
		}
		return chatStreamStartedMsg{stream: stream, cancel: cancel}
	}
}

// nextChatEventCmd reads one event from the active stream and
// converts it to the appropriate Msg. Re-issued by Update after
// each event so the stream drains continuously.
//
// InterviewEvent is a proto oneof; we discriminate via the typed
// getters (GetAgentTurn / GetStage / GetError) rather than the
// type/data_json shape used by the watch-surface event stream.
func nextChatEventCmd(stream gilv1.InterviewService_StartClient) tea.Cmd {
	return func() tea.Msg {
		ev, err := stream.Recv()
		if err != nil {
			// Stream closed cleanly OR errored. The chatModel's
			// Update treats both as turn-end; if the daemon emits
			// a richer signal we'll switch on err here.
			return chatStreamDoneMsg{}
		}
		if t := ev.GetAgentTurn(); t != nil {
			return chatAssistantChunkMsg{text: t.GetContent()}
		}
		if st := ev.GetStage(); st != nil {
			return chatStageReasonMsg{
				phase:  stagePhase(st.GetTo()),
				reason: st.GetReason(),
			}
		}
		if su := ev.GetSaturationUpdate(); su != nil {
			return chatSaturationMsg{
				filled:     int(su.GetSlotsFilled()),
				total:      int(su.GetSlotsTotal()),
				saturation: su.GetSaturation(),
			}
		}
		if af := ev.GetAdversaryFindings(); af != nil {
			return chatAdversaryMsg{count: int(af.GetCount())}
		}
		if e := ev.GetError(); e != nil {
			return chatStreamErrMsg{err: e.GetMessage()}
		}
		// Unknown / not-yet-rendered events become "skip" — Update
		// converts the sentinel back into a pump call. V1 just drops them.
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
