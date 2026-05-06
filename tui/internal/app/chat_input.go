package app

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// chatInputState wraps a bubbles textarea with prompt-history
// navigation (↑/↓). The textarea owns the cursor + buffer (incl.
// multi-line composition); this wrapper adds the recall-history
// affordance described in design §3.1, plus key bindings that route
// bare Enter to "submit" and reserve alt+enter / ctrl+j for "insert
// newline" so longer prompts can be composed in place.
type chatInputState struct {
	ta      textarea.Model
	history []string // submitted prompts, oldest first
	histIdx int      // current cursor: -1 == "below history" (empty buffer)
}

// chatInputMaxRows caps the prompt panel growth so a runaway paste
// doesn't push the conversation off-screen. The textarea internally
// scrolls past this row count.
const chatInputMaxRows = 8

// newChatInput returns a focused chatInputState ready for bubbletea
// Update routing. The placeholder text matches the affordance line
// subtitle for the idle phase (design §4.3).
//
// Key map:
//   - bare Enter → unbound here (chatModel.Update treats it as submit)
//   - alt+enter, ctrl+j → insert newline (rebound from default Enter)
//   - other defaults retained: arrows, word/line nav, delete-word, etc.
func newChatInput() chatInputState {
	ta := textarea.New()
	ta.Placeholder = ""
	ta.Prompt = "" // we draw the › cue in the panel chrome, not per line
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	// Clear the textarea's internal styling so the magenta panel
	// border is the ONLY chrome around the input. textarea defaults
	// add a subtle background highlight on the focused line that
	// fights the magenta-only palette rule (spec §4.1).
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.Base = lipgloss.NewStyle()
	// Move newline-insertion to alt+enter / ctrl+j so bare Enter is
	// free for "submit" (handled in chatModel.Update). Without this
	// rebind, Enter would silently insert a newline and the user
	// could never finish a turn.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("alt+enter", "ctrl+j"),
		key.WithHelp("alt+enter", "newline"),
	)
	ta.Focus()
	return chatInputState{ta: ta, histIdx: -1}
}

// submit returns the current buffer, clears it, and appends to
// history. The caller is responsible for sending the returned text
// downstream. Multi-line buffers are joined with "\n" — the daemon's
// prompt API accepts the literal newline characters and surfaces them
// to the LLM as part of the user message.
func (in *chatInputState) submit() string {
	v := in.ta.Value()
	in.ta.Reset()
	in.histIdx = -1
	v = strings.TrimRight(v, "\n")
	if v == "" {
		return ""
	}
	in.history = append(in.history, v)
	return v
}

// historyUp walks back one entry. Idempotent at the oldest entry.
// History entries that contain newlines (multi-line submissions) are
// recalled verbatim — the prompt panel reflows to fit.
func (in *chatInputState) historyUp() {
	if len(in.history) == 0 {
		return
	}
	if in.histIdx == -1 {
		in.histIdx = len(in.history) - 1
	} else if in.histIdx > 0 {
		in.histIdx--
	}
	in.ta.SetValue(in.history[in.histIdx])
	in.ta.CursorEnd()
}

// historyDown walks forward one entry. Stepping past the last entry
// returns the buffer to empty.
func (in *chatInputState) historyDown() {
	if len(in.history) == 0 || in.histIdx == -1 {
		return
	}
	in.histIdx++
	if in.histIdx >= len(in.history) {
		in.histIdx = -1
		in.ta.Reset()
		return
	}
	in.ta.SetValue(in.history[in.histIdx])
	in.ta.CursorEnd()
}

// rowCount reports how many rendered rows the current buffer needs
// (rune-aware), clamped to [1, chatInputMaxRows]. Used by the prompt
// panel to grow vertically as the user composes longer multi-line
// prompts. Cap prevents a runaway paste from pushing the conversation
// region off-screen — the textarea scrolls past the cap internally.
func (in *chatInputState) rowCount() int {
	n := in.ta.LineCount()
	if n < 1 {
		n = 1
	}
	if n > chatInputMaxRows {
		n = chatInputMaxRows
	}
	return n
}
