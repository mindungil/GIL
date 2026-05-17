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

// AssistantReasoning prints the model's separated chain-of-thought
// with a dim "[think]" prefix on each non-empty line, so the user can
// scan the transcript and tell reasoning apart from the final answer
// at a glance. Empty chunks (or chunks that are only whitespace) are
// emitted as-is to preserve streaming-cadence cues from the model.
// P33.
func (r *StdoutChatRenderer) AssistantReasoning(chunk string) {
	if chunk == "" {
		return
	}
	prefix := r.p.Dim("[think] ")
	// Split on newlines so we can prefix EVERY line of multi-line
	// reasoning, not just the first. Trailing newline survives the
	// split as an empty tail element that we emit verbatim.
	lines := strings.Split(chunk, "\n")
	for i, line := range lines {
		if i > 0 {
			fmt.Fprintln(r.out)
		}
		if line == "" {
			continue
		}
		fmt.Fprint(r.out, prefix, r.p.Dim(line))
	}
}

func (r *StdoutChatRenderer) StatusStrip(s SessionState) {
	var body string
	switch s.Phase {
	case PhaseIdle:
		// §2.6: keep the idle strip free of slash-jargon. Discovery of
		// past sessions is handled by the entry self-disclosure, not by
		// telling the user to memorize a slash command.
		body = "idle · type a prompt to start a new session"
	case PhaseInterview:
		body = formatInterviewStrip(s)
	case PhaseAwaitingConfirm:
		body = "interview · ready to freeze · /run to start, prompt to keep iterating"
	case PhaseRun:
		body = formatRunStrip(s)
	case PhaseStuck:
		body = fmt.Sprintf("run · iter %d/%d · STUCK after recovery", s.Iter, s.MaxIter)
	case PhaseDone:
		body = formatDoneStrip(s, r.ascii)
	default:
		body = string(s.Phase)
	}
	// --ascii: collapse the `·` middle-dot separator into a 7-bit
	// alternative so terminals without UTF-8 don't render mojibake
	// (the previous behaviour). Format strings keep `·` for readability
	// of the source — the substitution happens once per strip render.
	if r.ascii {
		body = stripMiddleDot(body)
	}
	fmt.Fprintf(r.out, "[%s]\n", body)
}

// stripMiddleDot replaces ` · ` (space + middle-dot + space) with ` | `
// (space + pipe + space). The flanking spaces preserve column rhythm
// so an ASCII-mode strip lines up with the unicode version, just with
// a different separator glyph.
func stripMiddleDot(s string) string {
	return strings.ReplaceAll(s, " · ", " | ")
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

// formatRunStrip composes the in-flight run strip. Tokens / latency
// are appended only when the daemon has reported them so a
// just-started run still reads "run · iter 1/N · $0.00 · ASK_DESTRUCTIVE"
// instead of "0 toks · 0ms" placeholders.
func formatRunStrip(s SessionState) string {
	body := fmt.Sprintf("run · iter %d/%d · $%.2f", s.Iter, s.MaxIter, s.CostUSD)
	if s.Tokens > 0 {
		body += " · " + formatTokens(s.Tokens) + " toks"
	}
	if s.LatencyMs > 0 {
		body += " · " + formatLatency(s.LatencyMs)
	}
	if s.Autonomy != "" {
		body += " · " + s.Autonomy
	}
	return body
}

// formatTokens compacts a token count for the strip cell:
//
//	   42 → "42"
//	  942 → "942"
//	 4231 → "4.2k"
//	42301 → "42.3k"
func formatTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// formatLatency renders the most recent provider RTT as a compact
// string. Sub-second uses ms; anything 1s+ collapses to "Xs" with one
// decimal so the strip stays narrow when the model is slow.
func formatLatency(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
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
	fmt.Fprintf(r.out, "[%s] %s\n", kind, sanitizeNoteForTerminal(msg))
}

// sanitizeNoteForTerminal strips control bytes (ESC, BEL, backspace,
// DEL, …) from a single-line system note before it lands on the
// terminal. iter118a-dod: defence-in-depth — every loop callsite
// already passes safe text, but a future event-type that surfaces
// agent-controlled fields shouldn't be able to repaint the screen.
// Tabs collapse to spaces, embedded newlines drop (system notes are
// single-line).
func sanitizeNoteForTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop ESC, BEL, backspace, newline, DEL, etc.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
