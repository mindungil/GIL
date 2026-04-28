package render

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMock_RecordsCallsInOrder(t *testing.T) {
	m := NewMockRenderer()
	m.Banner(SessionState{DisplayName: "x"})
	m.AssistantText("hi")
	m.SystemNote(NoteSpec, "slot")
	require.Equal(t, []MockCall{
		{Method: "Banner", State: SessionState{DisplayName: "x"}},
		{Method: "AssistantText", Text: "hi"},
		{Method: "SystemNote", Kind: NoteSpec, Text: "slot"},
	}, m.Calls)
}
