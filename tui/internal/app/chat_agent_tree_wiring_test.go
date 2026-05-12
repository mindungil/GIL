package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// chat_agent_tree_wiring_test.go — M6.2. Exercises the chatModel's
// Update path for tool_call / tool_result / stream_done msgs and
// verifies the AgentTree side-channel populates correctly. The
// transcript-rendering path is unchanged (M6.3 will replace the pane).

func TestChatModel_PromptToolCallFeedsTree(t *testing.T) {
	m := &chatModel{}

	// Implicit turn — no explicit OnTurnStart needed.
	m.Update(chatPromptToolCallMsg{id: "c1", name: "read_file", inputJSON: `{"path":"a.go"}`})
	tree := m.tree()
	require.Len(t, tree.Turns, 1)
	require.Len(t, tree.Turns[0].Children, 1)
	require.Equal(t, "read_file", tree.Turns[0].Children[0].Name)
	require.Equal(t, NodeRunning, tree.Turns[0].Children[0].Status)
}

func TestChatModel_PromptToolResultTransitionsNode(t *testing.T) {
	m := &chatModel{}
	m.Update(chatPromptToolCallMsg{id: "c1", name: "read_file", inputJSON: `{}`})
	m.Update(chatPromptToolResultMsg{callID: "c1", content: "ok", isError: false})

	tree := m.tree()
	require.Equal(t, NodeOK, tree.Turns[0].Children[0].Status)
	require.Equal(t, "ok", tree.Turns[0].Children[0].ResultPreview)
}

func TestChatModel_StreamDoneClosesTurnAndNextCallOpensNew(t *testing.T) {
	m := &chatModel{}
	m.Update(chatPromptToolCallMsg{id: "c1", name: "read_file", inputJSON: `{}`})
	m.Update(chatStreamDoneMsg{})

	tree := m.tree()
	require.True(t, tree.Turns[0].Closed)

	m.Update(chatPromptToolCallMsg{id: "c2", name: "grep", inputJSON: `{}`})
	require.Len(t, tree.Turns, 2, "next tool call after stream_done opens a fresh turn")
	require.Equal(t, "grep", tree.Turns[1].Children[0].Name)
}

func TestChatModel_PromptStreamStartedOpensTurnExplicitly(t *testing.T) {
	m := &chatModel{}
	// Construct a started-stream msg with nil stream/cancel — handler
	// stores them but neither is invoked synchronously, so the test
	// can observe just the tree side-effect.
	m.Update(chatPromptStreamStartedMsg{stream: nil, cancel: nil})

	tree := m.tree()
	require.Len(t, tree.Turns, 1)
	require.True(t, tree.Turns[0].Expanded)
	require.False(t, tree.Turns[0].Closed)
}

func TestChatModel_ToolResultForUnknownIDIsSafeNoOp(t *testing.T) {
	m := &chatModel{}
	require.NotPanics(t, func() {
		m.Update(chatPromptToolResultMsg{callID: "never-saw-this", content: "x"})
	})
	// No turn was opened by a tool_result alone — that's the agent
	// tree's contract; the chat transcript still appends the line
	// for visibility.
}
