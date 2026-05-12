package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func mkEvent(typ string, dataJSON string) *gilv1.Event {
	return &gilv1.Event{
		Timestamp: timestamppb.New(time.Now()),
		Type:      typ,
		Source:    gilv1.EventSource_AGENT,
		Kind:      gilv1.EventKind_ACTION,
		DataJson:  []byte(dataJSON),
	}
}

func TestApplyEventToChatTree_NilTreeNoPanic(t *testing.T) {
	// Defensive nil-check: callers wire this through m.chatTreeOrNew()
	// which never returns nil, but unit tests + future callers may
	// pass nil so we don't want a crash.
	applyEventToChatTree(nil, mkEvent("tool_call", `{"id":"a","name":"x"}`))
}

func TestApplyEventToChatTree_NilEventNoPanic(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, nil)
	require.Empty(t, tree.Turns)
}

func TestApplyEventToChatTree_IterationStartOpensTurn(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("iteration_start", `{}`))
	require.Len(t, tree.Turns, 1)
	require.False(t, tree.Turns[0].Closed)
}

func TestApplyEventToChatTree_ToolCallAppendsNode(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c1","name":"bash","input":"{\"cmd\":\"ls\"}"}`))
	require.Len(t, tree.Turns, 1)
	require.Len(t, tree.Turns[0].Children, 1)
	require.Equal(t, "c1", tree.Turns[0].Children[0].ID)
	require.Equal(t, "bash", tree.Turns[0].Children[0].Name)
	require.Equal(t, NodeRunning, tree.Turns[0].Children[0].Status)
}

func TestApplyEventToChatTree_ToolResultTransitionsMatchingNode(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c1","name":"bash"}`))
	applyEventToChatTree(tree, mkEvent("tool_result", `{"id":"c1","content":"hi","is_error":false}`))
	require.Equal(t, NodeOK, tree.Turns[0].Children[0].Status)
	require.Equal(t, "hi", tree.Turns[0].Children[0].ResultPreview)
}

func TestApplyEventToChatTree_ToolResultErrorMarksFailed(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c1","name":"bash"}`))
	applyEventToChatTree(tree, mkEvent("tool_result", `{"id":"c1","content":"perm denied","is_error":true}`))
	require.Equal(t, NodeFailed, tree.Turns[0].Children[0].Status)
}

func TestApplyEventToChatTree_DoneClosesActiveTurn(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c1","name":"bash"}`))
	applyEventToChatTree(tree, mkEvent("run.done", `{}`))
	require.True(t, tree.Turns[0].Closed)
}

func TestApplyEventToChatTree_UnknownTypeIsNoop(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("garbage", `{"x":1}`))
	require.Empty(t, tree.Turns)
}

func TestApplyEventToChatTree_MalformedToolCallSilent(t *testing.T) {
	// Malformed JSON should not crash or open a stray turn — the
	// matching helper exits the case early.
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("tool_call", `not json`))
	require.Empty(t, tree.Turns)
}

func TestRenderChatTreePane_EmptyReturnsEmptyString(t *testing.T) {
	require.Equal(t, "", renderChatTreePane(40, nil, 10))
	require.Equal(t, "", renderChatTreePane(40, NewAgentTree(), 10))
}

func TestRenderChatTreePane_RendersHeadAndChildren(t *testing.T) {
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("iteration_start", `{}`))
	applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c1","name":"bash","input":"{\"cmd\":\"ls\"}"}`))
	applyEventToChatTree(tree, mkEvent("tool_result", `{"id":"c1","content":"ok","is_error":false}`))

	out := renderChatTreePane(80, tree, 10)
	require.Contains(t, out, "Turn 1")
	require.Contains(t, out, "bash")
}

func TestRenderChatTreePane_RespectsMaxRows(t *testing.T) {
	// Build enough nodes to overflow a tiny budget; assert we hand
	// back exactly maxRows lines (no terminal blowout).
	tree := NewAgentTree()
	applyEventToChatTree(tree, mkEvent("iteration_start", `{}`))
	for i := 0; i < 12; i++ {
		applyEventToChatTree(tree, mkEvent("tool_call", `{"id":"c","name":"x"}`))
	}
	out := renderChatTreePane(80, tree, 3)
	lines := 1
	for _, c := range out {
		if c == '\n' {
			lines++
		}
	}
	require.LessOrEqual(t, lines, 3)
}
