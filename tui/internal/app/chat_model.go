package app

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/core/intent"
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

	// router classifies natural-language prompts into verb dispatches
	// per design.md §2.6(b). When nil the model forwards every prompt
	// directly (matches --no-intent-router on the cli surface).
	router *intent.Router
}

func (m *chatModel) Init() tea.Cmd { return nil }

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.ti.Width = msg.Width - 8 // panel inner width minus › prefix gutter
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyUp:
			m.input.historyUp()
			return m, nil
		case tea.KeyDown:
			m.input.historyDown()
			return m, nil
		case tea.KeyEnter:
			text := m.input.submit()
			if text == "" {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				if m.stream.cancel != nil {
					m.stream.cancel()
				}
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, "›  "+text)
			m.firstTurnDone = true

			// §2.6(b) intent router. Slash-prefixed inputs skip the
			// router (deterministic escape hatch). Verbs surface as a
			// transcript note; full verb-dispatch in the TUI is
			// followup #253b — for now non-quit verbs land a "use gil
			// chat for this verb" hint so the routing is at least
			// visible.
			if m.router != nil && !strings.HasPrefix(text, "/") {
				ctx := intent.SessionContext{
					Phase:           string(m.phase),
					ActiveSessionID: m.activeID,
					RecentSessions:  toIntentRefs(m.sessions),
				}
				cl := m.router.Classify(context.Background(), text, ctx)
				switch cl.Kind {
				case intent.KindVerb:
					if cl.Verb == intent.VerbQuit {
						if m.stream.cancel != nil {
							m.stream.cancel()
						}
						return m, tea.Quit
					}
					m.transcript = append(m.transcript,
						"   → "+cl.Rationale+"  (verb dispatch in TUI lands in followup; use `gil chat` for now)")
					return m, nil
				case intent.KindAmbiguous:
					m.transcript = append(m.transcript, "   ?  "+cl.Clarification)
					return m, nil
				case intent.KindTooVague:
					m.transcript = append(m.transcript, "   ?  "+cl.Clarification)
					return m, nil
				case intent.KindForward:
					// fall through to the daemon dispatch below
				}
			}

			if m.client == nil {
				return m, nil // test mode — no stream dispatch
			}
			if m.activeID == "" {
				m.err = "no active session — daemon must allocate one before chat"
				return m, nil
			}
			// Cancel any in-flight stream from a previous turn so the
			// new submit takes priority.
			if m.stream.cancel != nil {
				m.stream.cancel()
				m.stream = chatStreamState{}
			}
			return m, startInterviewCmd(m.client, m.activeID, text)
		default:
			// Forward to the textinput so typing works.
			var cmd tea.Cmd
			m.input.ti, cmd = m.input.ti.Update(msg)
			return m, cmd
		}

	case chatStreamStartedMsg:
		m.stream.stream = msg.stream
		m.stream.cancel = msg.cancel
		return m, nextChatEventCmd(msg.stream)

	case chatAssistantChunkMsg:
		// Coalesce consecutive assistant chunks onto the same line so
		// the user sees a flowing reply rather than one `‹` header
		// per chunk.
		if len(m.transcript) > 0 && strings.HasPrefix(m.transcript[len(m.transcript)-1], "‹") {
			m.transcript[len(m.transcript)-1] += msg.text
		} else {
			m.transcript = append(m.transcript, "‹  "+msg.text)
		}
		// Keep draining.
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatPhaseMsg:
		m.phase = msg.phase
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatStreamEventSkipMsg:
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatStreamDoneMsg:
		// Turn complete. Reset stream cursor; keep cancel so quit
		// path can call it harmlessly.
		m.stream.stream = nil
		return m, nil

	case chatStreamErrMsg:
		m.err = msg.err
		m.stream.stream = nil
		return m, nil
	}
	return m, nil
}

func (m *chatModel) View() string { return m.chatView() }

// toIntentRefs flattens the chatModel's session list into the slim
// SessionRef shape the intent router uses for slug matching.
func toIntentRefs(in []*sdk.Session) []intent.SessionRef {
	out := make([]intent.SessionRef, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		out = append(out, intent.SessionRef{ID: s.ID, Slug: s.GoalHint})
	}
	return out
}

// newChatModel constructs a chatModel ready for tea.NewProgram.
// socket is dialed lazily by the stream layer (Task 8).
func newChatModel(socket string) *chatModel {
	return &chatModel{
		socket: socket,
		phase:  ChatPhaseIdle,
		input:  newChatInput(),
		router: intent.NewRouter(),
	}
}

// NewChatModelForRun is the public constructor used by tui/run.Chat.
// Returns a tea.Model so the unexported chatModel type doesn't leak
// across the package boundary.
func NewChatModelForRun(socket string, client *sdk.Client) tea.Model {
	m := newChatModel(socket)
	m.client = client
	return m
}
