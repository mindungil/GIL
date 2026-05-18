package render

import "testing"

func TestRendererInterfaceShape(t *testing.T) {
	// Compile-time assertion: any implementation must satisfy Renderer.
	var _ Renderer = (*nopRenderer)(nil)
}

type nopRenderer struct{}

func (nopRenderer) Banner(SessionState)              {}
func (nopRenderer) AssistantText(string)             {}
func (nopRenderer) AssistantReasoning(string)        {}
func (nopRenderer) SystemNote(NoteKind, string)      {}
func (nopRenderer) StatusStrip(SessionState)         {}
func (nopRenderer) PromptCue()                       {}
func (nopRenderer) Confirm(string, bool) (bool, error) { return false, nil }
func (nopRenderer) Diff([]DiffHunk)                  {}
func (nopRenderer) Spec(*SpecView)                   {}
func (nopRenderer) Close() error                     { return nil }
