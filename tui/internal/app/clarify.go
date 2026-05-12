package app

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mindungil/gil/sdk"
)

// pendingClarifyMsg is dispatched when an event of type
// "clarify_requested" arrives on the tail stream. The TUI Update flips
// into a modal asking the user to pick a numbered suggestion or type a
// free-form answer. Mirrors the permission_ask flow but the answer is
// a string, not a bool.
type pendingClarifyMsg struct {
	SessionID   string
	AskID       string
	Question    string
	Context     string
	Suggestions []string
	Urgency     string
}

// clarifyAnswerMsg is the result of the user's answer; the SDK call
// already happened in the answerClarifyCmd goroutine.
type clarifyAnswerMsg struct {
	delivered bool
	err       string
}

// clarifyModalMode tracks whether the modal is in "pick a suggestion"
// mode (the default — number keys 1..N + 't' for typing) or in "type
// a custom answer" mode (the user pressed 't', a small inline buffer
// accumulates keystrokes until enter or esc).
type clarifyModalMode int

const (
	clarifyModePick clarifyModalMode = iota
	clarifyModeType
)

// clarifyModalState owns the modal's transient input. We keep it on
// the Model rather than the pendingClarifyMsg so the message stays
// purely the "what the agent asked" snapshot — the typing buffer is
// a TUI-only concern.
type clarifyModalState struct {
	mode    clarifyModalMode
	typeBuf string
}

// answerClarifyCmd fires the AnswerClarification RPC in a goroutine
// and delivers the result as a clarifyAnswerMsg. 5-second timeout —
// enough for any UDS roundtrip; if the daemon is hung we want to know
// fast rather than block the modal forever.
func answerClarifyCmd(client *sdk.Client, sessionID, askID, answer string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		delivered, err := client.AnswerClarification(ctx, sessionID, askID, answer)
		if err != nil {
			return clarifyAnswerMsg{err: err.Error()}
		}
		return clarifyAnswerMsg{delivered: delivered}
	}
}

// parseClarifyRequested inspects an event's type and data JSON for the
// clarify_requested payload. Returns nil when the event isn't a
// clarify_requested or when the payload is malformed / missing ask_id.
// Mirrors parsePermissionAsk so update.go can drop a single line in
// the event-handling switch.
func parseClarifyRequested(sessionID, eventType string, data []byte) *pendingClarifyMsg {
	if eventType != "clarify_requested" {
		return nil
	}
	var d struct {
		AskID       string   `json:"ask_id"`
		Question    string   `json:"question"`
		Context     string   `json:"context"`
		Suggestions []string `json:"suggestions"`
		Urgency     string   `json:"urgency"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil
	}
	if d.AskID == "" {
		return nil
	}
	return &pendingClarifyMsg{
		SessionID:   sessionID,
		AskID:       d.AskID,
		Question:    d.Question,
		Context:     d.Context,
		Suggestions: d.Suggestions,
		Urgency:     d.Urgency,
	}
}

// renderClarifyModal renders the boxed clarify modal. The shape
// matches the permission modal aesthetic (rounded light frame, accent
// on the headline, no bg fill) so a user who learned one surface can
// read the other instantly. Width is the desired content width;
// callers pass min(m.width-4, 70) — same as permission modal.
//
// In pick mode the suggestions are numbered and the footer hints at
// [t] type a custom answer. In type mode the typed buffer is shown
// inline with a cursor and enter/esc replace the suggestions list.
func renderClarifyModal(ask *pendingClarifyMsg, st *clarifyModalState, width int) string {
	urgency := ask.Urgency
	if urgency == "" {
		urgency = "normal"
	}
	header := styleCritical("Clarify")
	if urgency == "high" {
		header += " " + styleAlert("(urgent)")
	} else if urgency == "low" {
		header += " " + styleDim("(low)")
	}

	intro := styleSurface("The agent needs your input.")
	question := styleEmphasis("Q: ") + styleSurface(wrapText(ask.Question, max(width-6, 20)))

	var ctxLine string
	if ask.Context != "" {
		ctxLine = styleDim("context: " + wrapText(ask.Context, max(width-12, 20)))
	}

	body := []string{header, "", intro, "", question}
	if ctxLine != "" {
		body = append(body, "", ctxLine)
	}

	if st != nil && st.mode == clarifyModeType {
		// Typing mode: show the buffer with a trailing block cursor.
		buf := st.typeBuf + "_"
		body = append(body, "",
			styleEmphasis("answer> ")+styleSurface(buf),
			"",
			styleDim("[enter] send    [esc] back to suggestions"),
		)
	} else {
		// Pick mode: numbered suggestions (or nothing if no suggestions
		// supplied) + the "type a custom answer" footer.
		body = append(body, "")
		for i, s := range ask.Suggestions {
			body = append(body, styleSurface("["+string(rune('1'+i))+"] "+s))
		}
		if len(ask.Suggestions) == 0 {
			body = append(body, styleDim("(no suggestions — type a custom answer)"))
		}
		body = append(body, "",
			styleDim("[t] type a custom answer    [esc] cancel (timeout)"),
		)
	}

	frame := paneFrame("").Padding(1, 2)
	return frame.Render(strings.Join(body, "\n"))
}

// wrapText breaks a string at word boundaries to fit width columns.
// Single line input passes through when it already fits. Used so a
// long question doesn't blow past the modal frame and break the box-
// drawing characters.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var lines []string
	var current string
	for _, w := range strings.Fields(s) {
		if current == "" {
			current = w
			continue
		}
		if lipgloss.Width(current+" "+w) > width {
			lines = append(lines, current)
			current = w
			continue
		}
		current += " " + w
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}

// clarifyKeyToSuggestionIndex maps a numeric keystroke ("1".."9") to
// the corresponding 0-indexed suggestion slot, or -1 when the key is
// not a number. Returns -1 also when the key is in range but the
// suggestion slot is empty (the caller swallows the keystroke rather
// than dispatching nothing).
func clarifyKeyToSuggestionIndex(k string, suggestions []string) int {
	if len(k) != 1 {
		return -1
	}
	c := k[0]
	if c < '1' || c > '9' {
		return -1
	}
	idx := int(c - '1')
	if idx >= len(suggestions) {
		return -1
	}
	return idx
}
