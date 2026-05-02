package app

import (
	"github.com/charmbracelet/bubbles/textinput"
)

// chatInputState wraps a bubbles textinput with prompt-history
// navigation (↑/↓). The textinput owns the cursor + buffer; this
// wrapper adds the recall-history affordance described in design §3.1.
type chatInputState struct {
	ti      textinput.Model
	history []string // submitted prompts, oldest first
	histIdx int      // current cursor: -1 == "below history" (empty buffer)
}

// newChatInput returns a focused chatInputState ready for bubbletea
// Update routing. The placeholder text matches the affordance line
// subtitle for the idle phase (design §4.3).
func newChatInput() chatInputState {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.Focus()
	ti.Prompt = "" // we draw the › arrow in the panel chrome, not the input itself
	ti.CharLimit = 4096
	return chatInputState{ti: ti, histIdx: -1}
}

// submit returns the current buffer, clears it, and appends to history.
// The caller is responsible for sending the returned text downstream.
func (in *chatInputState) submit() string {
	v := in.ti.Value()
	in.ti.SetValue("")
	in.histIdx = -1
	if v == "" {
		return ""
	}
	in.history = append(in.history, v)
	return v
}

// historyUp walks back one entry. Idempotent at the oldest entry.
func (in *chatInputState) historyUp() {
	if len(in.history) == 0 {
		return
	}
	if in.histIdx == -1 {
		in.histIdx = len(in.history) - 1
	} else if in.histIdx > 0 {
		in.histIdx--
	}
	in.ti.SetValue(in.history[in.histIdx])
	in.ti.SetCursor(len(in.history[in.histIdx]))
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
		in.ti.SetValue("")
		return
	}
	in.ti.SetValue(in.history[in.histIdx])
	in.ti.SetCursor(len(in.history[in.histIdx]))
}
