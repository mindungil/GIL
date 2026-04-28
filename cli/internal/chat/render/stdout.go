package render

import (
	"bufio"
	"fmt"
	"io"
	"strings"

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

func (r *StdoutChatRenderer) SystemNote(kind NoteKind, msg string) {
	fmt.Fprintf(r.out, "[%s] %s\n", kind, msg)
}

func (r *StdoutChatRenderer) Confirm(question string, def bool) (bool, error) {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(r.out, "%s %s ", question, suffix)
	if r.reader == nil {
		return def, nil
	}
	line, err := r.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return def, err
	}
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

func (r *StdoutChatRenderer) Diff(hunks []DiffHunk) {
	if len(hunks) == 0 {
		fmt.Fprintln(r.out, "(no changes)")
		return
	}
	for _, h := range hunks {
		fmt.Fprintf(r.out, "  %s  +%d -%d\n", h.Path, h.Added, h.Removed)
	}
}

func (r *StdoutChatRenderer) Spec(view *SpecView) {
	if view == nil {
		fmt.Fprintln(r.out, "(no spec)")
		return
	}
	fmt.Fprintln(r.out, view.YAML)
}

func (r *StdoutChatRenderer) Close() error { return nil }
