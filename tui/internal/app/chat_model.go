package app

import (
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/workspace"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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

	// promptStream is the SessionService.Prompt stream cursor.
	// Active while the chat agent is mid-turn; nil otherwise.
	promptStream gilv1.SessionService_PromptClient
	promptCancel context.CancelFunc

	// Run-tail state owned by chat_run.go. Non-nil while a
	// RunService.Tail subscription is active; lets /quit cancel the
	// stream cleanly and lets a second /run replace the prior tail.
	runTail chatRunTailState

	// Live run telemetry — populated by chatRunEventMsg handlers,
	// consumed by renderStatusStrip so the strip body always
	// reflects what the agent is actually doing.
	runIter      int64
	runCost      float64
	runTokens    int64 // accumulated total — sum of EventMetrics.Tokens
	runLatencyMs int64 // most-recent provider call wall time (snapshot)
	stuckPattern string // last detected pattern; cleared on recovered

	// firstTurnDone flips true the moment the user submits the first
	// prompt; that's the cue for chat_view.go to stop rendering the
	// session list above the conversation viewport.
	firstTurnDone bool

	err string

	// providerLabel / modelLabel describe the LLM that backs this chat.
	// Resolved from the layered workspace config at construction time
	// so the header doesn't lie ("claude-opus-4-7" was hardcoded
	// previously, regardless of what the user actually configured).
	// Empty values fall through to a generic "model" placeholder.
	providerLabel string
	modelLabel    string

	// M6.1 agent tree — tool-call timeline that the center pane will
	// render once M6.3 lands. Populated by the chat_stream message
	// handlers; reset on session change. Lazy-initialised so the
	// existing constructors (which don't touch this field) keep
	// working — newAgentTreeOnce() handles the nil case.
	agentTree *AgentTree
}

// newAgentTreeOnce returns the model's tree, allocating on first use.
// Lets callers stay terse: m.tree().OnToolCall(...).
func (m *chatModel) tree() *AgentTree {
	if m.agentTree == nil {
		m.agentTree = NewAgentTree()
	}
	return m.agentTree
}

func (m *chatModel) Init() tea.Cmd { return nil }

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Panel inner width minus left margin (3) + border (1) +
		// cursor gutter (4 for " ›  ") + right border (1) + right
		// margin (3) ≈ 12 cells of chrome subtracted from terminal
		// width before handing to the textarea.
		m.input.ta.SetWidth(msg.Width - 12)
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
			if isTerminalExit(text) {
				m.cancelAllStreams()
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, "›  "+text)
			m.firstTurnDone = true

			// §2.6 + chat-architecture.md M2: 100% natural-language
			// surface. No slash dispatch, no client-side classification.
			// The daemon's agent loop receives every prompt and decides
			// what to do — including verb-style requests like "show me
			// the diff" or "merge it" via tool calls.
			if m.client == nil {
				return m, nil // test mode — no stream dispatch
			}
			// Cancel any in-flight prompt stream from a previous turn
			// so the new submit takes priority.
			if m.promptCancel != nil {
				m.promptCancel()
				m.promptStream = nil
				m.promptCancel = nil
			}
			return m, startChatPromptCmd(m.client, m.activeID, text)
		default:
			// Forward to the textinput so typing works.
			var cmd tea.Cmd
			m.input.ta, cmd = m.input.ta.Update(msg)
			// Resize on every keystroke so the panel grows/shrinks
			// to fit the current buffer's row count (capped at
			// chatInputMaxRows by rowCount()).
			m.input.ta.SetHeight(m.input.rowCount())
			return m, cmd
		}

	case chatPromptStreamStartedMsg:
		// M2: post-Prompt-RPC stream handle. Same pump pattern as the
		// interview stream but a different Recv() shape.
		m.promptStream = msg.stream
		m.promptCancel = msg.cancel
		// M6.2 — fresh turn root so subsequent tool_call/result msgs
		// land in the right node. OnTurnStart collapses prior turns
		// so the active turn dominates the (forthcoming) tree pane.
		m.tree().OnTurnStart(time.Time{})
		return m, nextChatPromptEventCmd(msg.stream)

	case chatPromptToolCallMsg:
		// Render the tool call inline so the user sees what the agent
		// is doing. Chat surface convention: ⚒ glyph + tool name +
		// brief input. Don't coalesce — each tool call is its own line.
		input := msg.inputJSON
		if len(input) > 80 {
			input = input[:80] + "…"
		}
		line := "   ⚒ " + msg.name
		if input != "" && input != "{}" {
			line += "  " + input
		}
		m.transcript = append(m.transcript, line)
		// M6.2 — mirror into the agent tree for the (forthcoming)
		// center pane. The transcript line stays as-is so M6.3 can
		// land without churning the chat scrollback.
		m.tree().OnToolCall(msg.id, msg.name, msg.inputJSON)
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatPromptToolResultMsg:
		glyph := "   ⚒ ✓ "
		body := msg.content
		if msg.isError {
			glyph = "   ⚒ ✗ "
		}
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		body = strings.ReplaceAll(body, "\n", " · ")
		m.transcript = append(m.transcript, glyph+body)
		// M6.2 — transition the matching tree node.
		m.tree().OnToolResult(msg.callID, msg.content, msg.isError)
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatPromptSessionAllocatedMsg:
		m.activeID = msg.sessionID
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatPromptMetricsMsg:
		m.runTokens = msg.tokensIn + msg.tokensOut
		m.runLatencyMs = msg.latencyMs
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatAssistantChunkMsg:
		// Coalesce consecutive assistant chunks onto the same line so
		// the user sees a flowing reply rather than one `‹` header
		// per chunk.
		if len(m.transcript) > 0 && strings.HasPrefix(m.transcript[len(m.transcript)-1], "‹") {
			m.transcript[len(m.transcript)-1] += msg.text
		} else {
			m.transcript = append(m.transcript, "‹  "+msg.text)
		}
		// Keep draining the prompt pump.
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatPhaseMsg:
		// Phase transitions are still emitted by the run-tail pump
		// (chat_run.go). Update the phase; no stream re-pump here
		// because that pump owns its own cycle.
		m.phase = msg.phase
		return m, nil

	case chatStreamEventSkipMsg:
		if m.promptStream != nil {
			return m, nextChatPromptEventCmd(m.promptStream)
		}
		return m, nil

	case chatStreamDoneMsg:
		m.promptStream = nil
		// M6.2 — close the active tree turn so the next user prompt
		// opens a fresh root rather than appending.
		m.tree().OnTurnDone()
		return m, nil

	case chatStreamErrMsg:
		m.err = msg.err
		m.promptStream = nil
		return m, nil

	// chatVerbResultMsg / chatNewSessionMsg removed in M3 — verb
	// dispatch and standalone session creation are gone. The daemon
	// auto-allocates a session on the first SessionService.Prompt
	// call and the agent's tools cover what verbs used to do.

	// chatRunStartedMsg / chatRunStartFailedMsg deleted in M3 — the
	// agent's start_run tool will emit a RunStarted Part directly
	// when that tool lands. For now run-tail attachment is driven by
	// the agent itself; chat_run.go provides the subscription pump
	// the agent can hook into.

	case chatRunTailStartedMsg:
		m.runTail.handle = msg.handle
		return m, nextChatRunEventCmd(msg.handle)

	case chatRunEventMsg:
		phase, lines, keep := formatChatRunEvent(msg.ev)
		if phase != "" {
			m.phase = phase
		}
		// Telemetry capture for the status strip — single source of
		// truth so renderStatusStrip doesn't re-parse event payloads.
		updateChatRunTelemetry(m, msg.ev)
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
	if m.promptCancel != nil {
		m.promptCancel()
	}
	if m.runTail.handle != nil {
		m.runTail.handle.cancel()
	}
}

func (m *chatModel) View() string { return m.chatView() }

// (toIntentRefs deleted in M3 — the intent router is gone.)

// isTerminalExit reports whether a bare line should exit the TUI.
// Per docs/design/chat-architecture.md §3.1 this is the ONE non-agent
// client-side recognition that survives the slash-removal pass.
func isTerminalExit(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "quit", "exit", "bye", "/quit", "/exit":
		return true
	}
	return false
}

func newChatModel(socket string) *chatModel {
	prov, model := resolveDisplayLabels()
	return &chatModel{
		socket:        socket,
		phase:         ChatPhaseIdle,
		input:         newChatInput(),
		providerLabel: prov,
		modelLabel:    model,
	}
}

// resolveDisplayLabels reads the layered workspace config (global +
// project) and returns (provider, model) suitable for the chat
// header. Best-effort — any error returns ("", "") and the header
// falls back to a generic placeholder. Project-local config wins
// over global; defaults supplied by workspace.Defaults are used when
// neither layer sets a value.
func resolveDisplayLabels() (string, string) {
	layout, err := paths.FromEnv()
	if err != nil {
		return "", ""
	}
	cwd, _ := os.Getwd()
	cfg, _ := workspace.Resolve(layout.ConfigFile(), workspace.LocalConfigFile(cwd))
	return cfg.Provider, cfg.Model
}

// NewChatModelForRun is the public constructor used by tui/run.Chat.
// Returns a tea.Model so the unexported chatModel type doesn't leak
// across the package boundary.
func NewChatModelForRun(socket string, client *sdk.Client) tea.Model {
	m := newChatModel(socket)
	m.client = client
	return m
}
