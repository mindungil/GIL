package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// ChatPhase mirrors cli/internal/chat/render.Phase but lives in the
// tui module so we don't pull cli into a tui dependency. Values are
// the same string constants the daemon emits in events, so cross-
// module comparisons stay trivial.
type ChatPhase string

const (
	ChatPhaseIdle            ChatPhase = "idle"
	ChatPhaseInterview       ChatPhase = "interview"
	ChatPhaseAwaitingConfirm ChatPhase = "awaiting-confirm"
	ChatPhaseRun             ChatPhase = "run"
	ChatPhaseStuck           ChatPhase = "stuck"
	ChatPhaseDone            ChatPhase = "done"
)

// chatModel is the bubbletea root model for the prompt-centric chat
// surface. See docs/plans/phase-26.6-prompt-centric-tui.md.
type chatModel struct {
	socket string
	client *sdk.Client

	width  int
	height int

	phase    ChatPhase
	sessions []*sdk.Session // pre-first-turn list, scrolls off after first turn
	activeID string         // current session; empty before first turn

	// Conversation transcript, oldest first. Each entry is a fully
	// pre-rendered line ready for the viewport.
	transcript []string

	// Input state owned by chat_input.go (textinput model + history).
	input chatInputState

	// Streaming state owned by chat_stream.go.
	stream chatStreamState

	// firstTurnDone flips true the moment the user submits the first
	// prompt; that's the cue for chat_view.go to stop rendering the
	// session list above the conversation viewport.
	firstTurnDone bool

	err string
}

func (m *chatModel) Init() tea.Cmd { return nil }

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}
	return m, nil
}

func (m *chatModel) View() string { return m.chatView() }

// TEMPORARY stub — replaced in Task 6 (chat_session.go).
func (m *chatModel) renderPreFirstTurn(convH int) string {
	return strings.Repeat("\n", convH-1)
}

// chatStreamState is filled in by Task 8.
// Empty placeholder here keeps the package buildable until that task lands.
type chatStreamState struct{}

// newChatModel constructs a chatModel ready for tea.NewProgram.
// socket is dialed lazily by the stream layer (Task 8).
func newChatModel(socket string) *chatModel {
	return &chatModel{
		socket: socket,
		phase:  ChatPhaseIdle,
		input:  newChatInput(),
	}
}
