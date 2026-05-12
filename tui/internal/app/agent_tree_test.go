package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// agent_tree_test.go covers M6.1 data model: turn boundaries, status
// transitions, ID matching. No render coverage here (M6.3+).

func TestAgentTree_StartTurnAndAppendCalls(t *testing.T) {
	tree := NewAgentTree()
	tree.OnTurnStart(time.Now())
	tree.OnToolCall("c1", "read_file", `{"path":"a.go"}`)
	tree.OnToolCall("c2", "grep", `{"pattern":"foo"}`)

	require.Len(t, tree.Turns, 1)
	require.Len(t, tree.Turns[0].Children, 2)
	require.Equal(t, NodeRunning, tree.Turns[0].Children[0].Status)
	require.Equal(t, NodeRunning, tree.Turns[0].Children[1].Status)
	require.True(t, tree.Turns[0].Expanded)
	require.False(t, tree.Turns[0].Closed)
}

func TestAgentTree_OnToolCall_AutoStartsTurn(t *testing.T) {
	tree := NewAgentTree()
	// No explicit OnTurnStart; first tool call must still land.
	node := tree.OnToolCall("c1", "read_file", `{}`)
	require.NotNil(t, node)
	require.Len(t, tree.Turns, 1)
	require.Equal(t, 1, tree.Turns[0].Number)
}

func TestAgentTree_OnToolResult_TransitionsOK(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "read_file", `{"path":"a.go"}`)
	time.Sleep(2 * time.Millisecond) // ensure duration > 0
	node := tree.OnToolResult("c1", "file contents", false)
	require.NotNil(t, node)
	require.Equal(t, NodeOK, node.Status)
	require.Greater(t, node.Duration, time.Duration(0))
	require.Equal(t, "file contents", node.ResultPreview)
}

func TestAgentTree_OnToolResult_TransitionsFailed(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "run_bash", `{"cmd":"false"}`)
	node := tree.OnToolResult("c1", "exit 1\nfailed", true)
	require.NotNil(t, node)
	require.Equal(t, NodeFailed, node.Status)
	require.Equal(t, "exit 1", node.ResultPreview, "preview takes first line only")
}

func TestAgentTree_OnToolResult_UnknownCallIDReturnsNil(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "read_file", `{}`)
	node := tree.OnToolResult("c-unknown", "x", false)
	require.Nil(t, node)
}

func TestAgentTree_OnTurnDone_ClosesAndNewCallStartsNewTurn(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "read_file", `{}`)
	tree.OnTurnDone()
	require.True(t, tree.Turns[0].Closed)

	// Next tool call must open a new turn, not append to closed turn.
	tree.OnToolCall("c2", "grep", `{}`)
	require.Len(t, tree.Turns, 2)
	require.Equal(t, 2, tree.Turns[1].Number)
	require.False(t, tree.Turns[1].Closed)
}

func TestAgentTree_NewTurnCollapsesPrevious(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "read_file", `{}`)
	require.True(t, tree.Turns[0].Expanded)

	tree.OnTurnDone()
	tree.OnTurnStart(time.Now())
	require.False(t, tree.Turns[0].Expanded, "previous turn collapses when next opens")
	require.True(t, tree.Turns[1].Expanded)
}

func TestAgentTree_ToggleExpand(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "x", `{}`)
	tree.OnTurnDone()
	tree.OnToolCall("c2", "y", `{}`)
	require.False(t, tree.Turns[0].Expanded)

	tree.ToggleExpand(0)
	require.True(t, tree.Turns[0].Expanded)
	tree.ToggleExpand(0)
	require.False(t, tree.Turns[0].Expanded)

	// Out-of-range is a no-op (no panic).
	tree.ToggleExpand(99)
	tree.ToggleExpand(-1)
}

func TestAgentTree_Reset(t *testing.T) {
	tree := NewAgentTree()
	tree.OnToolCall("c1", "x", `{}`)
	tree.OnToolCall("c2", "y", `{}`)
	require.NotEmpty(t, tree.Turns)
	tree.Reset()
	require.Empty(t, tree.Turns)
}

func TestPreviewArgs_TruncatesAndSinglelines(t *testing.T) {
	long := `{"path":"` + repeatStr("a", 200) + `"}`
	out := previewArgs(long)
	// 80 bytes + "…" (3-byte UTF-8 ellipsis) = 83 bytes max.
	require.LessOrEqual(t, len(out), 83)
	require.NotContains(t, out, "\n")

	multi := `{"a":1,
"b":2}`
	require.NotContains(t, previewArgs(multi), "\n")
}

func TestPreviewResult_FirstLineOnly(t *testing.T) {
	require.Equal(t, "first", previewResult("first\nsecond\nthird"))
	require.Equal(t, "", previewResult(""))
	require.Equal(t, "", previewResult("\n\n"))
}

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
