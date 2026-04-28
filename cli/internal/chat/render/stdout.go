package render

import (
	"bufio"
	"fmt"
	"io"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
)

type StdoutChatRenderer struct {
	out    io.Writer
	in     io.Reader
	ascii  bool
	g      uistyle.Glyphs
	p      uistyle.Palette
	reader *bufio.Reader
}

func NewStdoutChatRenderer(out io.Writer, in io.Reader, ascii, noColor bool) *StdoutChatRenderer {
	var br *bufio.Reader
	if in != nil {
		br = bufio.NewReader(in)
	}
	return &StdoutChatRenderer{
		out:    out,
		in:     in,
		ascii:  ascii,
		g:      uistyle.NewGlyphs(ascii),
		p:      uistyle.NewPalette(noColor),
		reader: br,
	}
}

func (r *StdoutChatRenderer) Banner(s SessionState) {
	fmt.Fprintf(r.out, "%s %s\n", r.p.Primary("gil"), r.p.Dim(s.DisplayName))
}

func (r *StdoutChatRenderer) PromptCue() {
	fmt.Fprint(r.out, "> ")
}

func (r *StdoutChatRenderer) AssistantText(chunk string) {
	fmt.Fprint(r.out, chunk)
}

func (r *StdoutChatRenderer) StatusStrip(s SessionState) {
	var body string
	switch s.Phase {
	case PhaseIdle:
		body = "idle · type a prompt to start, or /sessions to resume"
	case PhaseInterview:
		body = formatInterviewStrip(s)
	case PhaseAwaitingConfirm:
		body = "interview · ready to freeze · /run to start, prompt to keep iterating"
	case PhaseRun:
		body = fmt.Sprintf("run · iter %d/%d · $%.2f · %s", s.Iter, s.MaxIter, s.CostUSD, s.Autonomy)
	case PhaseStuck:
		body = fmt.Sprintf("run · iter %d/%d · STUCK after recovery", s.Iter, s.MaxIter)
	case PhaseDone:
		body = formatDoneStrip(s, r.ascii)
	default:
		body = string(s.Phase)
	}
	fmt.Fprintf(r.out, "[%s]\n", body)
}

func formatInterviewStrip(s SessionState) string {
	base := fmt.Sprintf("interview · %d/%d slots · sat %d%%",
		s.SlotsFilled, s.SlotsTotal, int(s.Saturation*100+0.5))
	switch {
	case s.AdvFindings == 0:
		return base
	case s.AdvFindings == 1:
		return base + " · 1 adv finding"
	default:
		return fmt.Sprintf("%s · %d adv findings", base, s.AdvFindings)
	}
}

func formatDoneStrip(s SessionState, ascii bool) string {
	mark := "✓"
	if s.ChecksPassed < s.ChecksTotal {
		mark = "✗"
	}
	if ascii {
		if s.ChecksPassed == s.ChecksTotal {
			mark = "OK"
		} else {
			mark = "FAIL"
		}
	}
	return fmt.Sprintf("done · %d iters · $%.2f · %s %d/%d checks · /diff /merge",
		s.Iter, s.CostUSD, mark, s.ChecksPassed, s.ChecksTotal)
}

// Stubs for the rest of the interface; later tasks fill them in.
func (r *StdoutChatRenderer) SystemNote(NoteKind, string)              {}
func (r *StdoutChatRenderer) Confirm(string, bool) (bool, error)       { return false, nil }
func (r *StdoutChatRenderer) Diff([]DiffHunk)                          {}
func (r *StdoutChatRenderer) Spec(*SpecView)                           {}
func (r *StdoutChatRenderer) Close() error                             { return nil }
