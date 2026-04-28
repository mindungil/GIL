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

// Stubs for the rest of the interface; later tasks fill them in.
func (r *StdoutChatRenderer) SystemNote(NoteKind, string)              {}
func (r *StdoutChatRenderer) StatusStrip(SessionState)                 {}
func (r *StdoutChatRenderer) Confirm(string, bool) (bool, error)       { return false, nil }
func (r *StdoutChatRenderer) Diff([]DiffHunk)                          {}
func (r *StdoutChatRenderer) Spec(*SpecView)                           {}
func (r *StdoutChatRenderer) Close() error                             { return nil }
