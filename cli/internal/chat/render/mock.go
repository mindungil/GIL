package render

type MockCall struct {
	Method string
	Text   string
	Kind   NoteKind
	State  SessionState
	Hunks  []DiffHunk
	Spec   *SpecView
}

type MockRenderer struct {
	Calls          []MockCall
	ConfirmAnswers []bool
}

func NewMockRenderer() *MockRenderer { return &MockRenderer{} }

func (m *MockRenderer) Banner(s SessionState) {
	m.Calls = append(m.Calls, MockCall{Method: "Banner", State: s})
}
func (m *MockRenderer) AssistantText(c string) {
	m.Calls = append(m.Calls, MockCall{Method: "AssistantText", Text: c})
}
func (m *MockRenderer) SystemNote(k NoteKind, msg string) {
	m.Calls = append(m.Calls, MockCall{Method: "SystemNote", Kind: k, Text: msg})
}
func (m *MockRenderer) StatusStrip(s SessionState) {
	m.Calls = append(m.Calls, MockCall{Method: "StatusStrip", State: s})
}
func (m *MockRenderer) PromptCue() {
	m.Calls = append(m.Calls, MockCall{Method: "PromptCue"})
}
func (m *MockRenderer) Confirm(q string, def bool) (bool, error) {
	m.Calls = append(m.Calls, MockCall{Method: "Confirm", Text: q})
	if len(m.ConfirmAnswers) > 0 {
		ans := m.ConfirmAnswers[0]
		m.ConfirmAnswers = m.ConfirmAnswers[1:]
		return ans, nil
	}
	return def, nil
}
func (m *MockRenderer) Diff(h []DiffHunk) {
	m.Calls = append(m.Calls, MockCall{Method: "Diff", Hunks: h})
}
func (m *MockRenderer) Spec(s *SpecView) {
	m.Calls = append(m.Calls, MockCall{Method: "Spec", Spec: s})
}
func (m *MockRenderer) Close() error { return nil }

// Reset clears recorded calls but keeps queued Confirm answers.
func (m *MockRenderer) Reset() { m.Calls = nil }

// MethodSequence returns just the method names — handy for sequence assertions.
func (m *MockRenderer) MethodSequence() []string {
	out := make([]string, len(m.Calls))
	for i, c := range m.Calls {
		out[i] = c.Method
	}
	return out
}
