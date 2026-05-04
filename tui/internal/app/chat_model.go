package app

import (
	"context"
	"fmt"
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

	// Run-tail state owned by chat_run.go. Non-nil while a
	// RunService.Tail subscription is active; lets /quit cancel the
	// stream cleanly and lets a second /run replace the prior tail.
	runTail chatRunTailState

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
			m.cancelAllStreams()
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
				m.cancelAllStreams()
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, "›  "+text)
			m.firstTurnDone = true

			// §2.6(b) intent router. Slash-prefixed inputs skip the
			// router (deterministic escape hatch). Verbs route through
			// dispatchVerb; ambiguous/too-vague get a clarification
			// turned into a transcript note.
			if m.router != nil && !strings.HasPrefix(text, "/") {
				ctx := intent.SessionContext{
					Phase:           string(m.phase),
					ActiveSessionID: m.activeID,
					RecentSessions:  toIntentRefs(m.sessions),
				}
				cl := m.router.Classify(context.Background(), text, ctx)
				switch cl.Kind {
				case intent.KindVerb:
					return m, m.dispatchVerb(cl)
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

	case chatStageReasonMsg:
		// Same effect as chatPhaseMsg (flips the strip) but also
		// surfaces the human-readable reason in the transcript so
		// the user sees what the engine inferred.
		m.phase = msg.phase
		var note string
		switch msg.phase {
		case ChatPhaseInterview:
			note = "interview started"
		case ChatPhaseAwaitingConfirm:
			note = "ready to freeze — say `run` to start the agent"
		}
		if note != "" {
			line := "   ‹  " + note
			if msg.reason != "" {
				line += " — " + msg.reason
			}
			m.transcript = append(m.transcript, line)
		}
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatSaturationMsg:
		m.transcript = append(m.transcript,
			fmt.Sprintf("   ‹  slot filled (%d/%d, sat %d%%)",
				msg.filled, msg.total, int(msg.saturation*100+0.5)))
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatAdversaryMsg:
		if msg.count > 0 {
			m.transcript = append(m.transcript,
				fmt.Sprintf("   ‹  adversary: %d finding(s)", msg.count))
		}
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

	case chatVerbResultMsg:
		// Multi-line results (sessions list, spec, diff) get split so
		// each line aligns with the transcript's leading 3-space gutter.
		glyph := "   ‹"
		if msg.kind == "err" {
			glyph = "   !"
		}
		for _, line := range strings.Split(msg.text, "\n") {
			m.transcript = append(m.transcript, glyph+"  "+line)
		}
		return m, nil

	case chatNewSessionMsg:
		if msg.err != "" {
			m.transcript = append(m.transcript, "   !  new: "+msg.err)
			return m, nil
		}
		m.activeID = msg.session.ID
		m.sessions = append([]*sdk.Session{msg.session}, m.sessions...)
		m.transcript = append(m.transcript,
			"   ‹  created "+shortChatID(msg.session.ID)+" — describe the task to begin")
		return m, nil

	case chatRunStartedMsg:
		// StartRun returned ok; flip phase and open the Tail
		// subscription so the run becomes visible in the transcript.
		// A second /run replaces the prior tail (cancel first).
		if m.runTail.handle != nil {
			m.runTail.handle.cancel()
			m.runTail = chatRunTailState{}
		}
		m.phase = ChatPhaseRun
		m.transcript = append(m.transcript, "   ‹  run started — tailing")
		if m.client == nil {
			return m, nil
		}
		return m, startChatRunTailCmd(m.client, msg.sessionID)

	case chatRunStartFailedMsg:
		m.transcript = append(m.transcript, "   !  run: "+msg.err)
		return m, nil

	case chatRunTailStartedMsg:
		m.runTail.handle = msg.handle
		return m, nextChatRunEventCmd(msg.handle)

	case chatRunEventMsg:
		phase, lines, keep := formatChatRunEvent(msg.ev)
		if phase != "" {
			m.phase = phase
		}
		// Coalesce consecutive agent_turn chunks the same way the
		// interview path does — if the new line starts with "‹" and
		// the prior transcript line also starts with "‹", append in
		// place. Other glyphs (   ‹, !, ?) stay as separate entries.
		for _, line := range lines {
			if strings.HasPrefix(line, "‹  ") && len(m.transcript) > 0 &&
				strings.HasPrefix(m.transcript[len(m.transcript)-1], "‹  ") {
				m.transcript[len(m.transcript)-1] += strings.TrimPrefix(line, "‹  ")
				continue
			}
			m.transcript = append(m.transcript, line)
		}
		if !keep {
			// Terminal event (run.done) — close the tail and let
			// chatRunTailDoneMsg arrive naturally on the next Recv.
			return m, nextChatRunEventCmd(msg.handle)
		}
		if m.runTail.handle != nil {
			return m, nextChatRunEventCmd(m.runTail.handle)
		}
		return m, nil

	case chatRunTailDoneMsg:
		m.runTail = chatRunTailState{}
		return m, nil

	case chatRunTailErrMsg:
		m.transcript = append(m.transcript, "   !  run tail error: "+msg.err)
		m.runTail = chatRunTailState{}
		return m, nil
	}
	return m, nil
}

// cancelAllStreams cancels every in-flight gRPC stream owned by the
// chat model. Called from /quit, /exit, and Ctrl+C so we don't leave
// goroutines waiting on Recv after the bubbletea program exits.
func (m *chatModel) cancelAllStreams() {
	if m.stream.cancel != nil {
		m.stream.cancel()
	}
	if m.runTail.handle != nil {
		m.runTail.handle.cancel()
	}
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
