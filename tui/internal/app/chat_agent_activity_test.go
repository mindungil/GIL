package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// chat_agent_activity_test.go — G2-UI V1. The agent tree from M6.1
// feeds the chat status strip when no other phase-specific copy
// overrides it. These tests pin the strip-body format so future
// changes can rely on stable user-visible text.

func TestChatAgentActivity_EmptyTreeReturnsEmpty(t *testing.T) {
	m := &chatModel{}
	require.Equal(t, "", chatAgentActivity(m, "·"))
}

func TestChatAgentActivity_ActiveTurnShowsRunningCount(t *testing.T) {
	m := &chatModel{}
	// Active turn = last turn not yet Closed. OnToolCall opens a turn
	// implicitly and leaves it open until OnTurnDone fires.
	tree := m.tree()
	tree.OnToolCall("c1", "read_file", `{}`)
	tree.OnToolCall("c2", "grep", `{}`)
	tree.OnToolResult("c1", "ok", false)

	body := chatAgentActivity(m, "·")
	require.Contains(t, body, "agent")
	require.Contains(t, body, "⚒ 1 running")
	require.Contains(t, body, "✓ 1")
}

func TestChatAgentActivity_ClosedTurnSummarizesLastTurn(t *testing.T) {
	m := &chatModel{}
	tree := m.tree()
	tree.OnToolCall("c1", "read_file", `{}`)
	tree.OnToolResult("c1", "ok", false)
	tree.OnToolCall("c2", "verify", `{}`)
	tree.OnToolResult("c2", "fail", true)
	tree.OnTurnDone()

	body := chatAgentActivity(m, "·")
	require.Contains(t, body, "ready")
	require.Contains(t, body, "last turn: 2 tools")
	require.Contains(t, body, "✓ 1")
	require.Contains(t, body, "✗ 1")
}

func TestChatAgentActivity_OverridesIdlePhase(t *testing.T) {
	// chatStatusBody short-circuits to chatAgentActivity output when
	// the tree has data, instead of falling through to "idle · ready".
	m := &chatModel{phase: ChatPhaseIdle}
	tree := m.tree()
	tree.OnToolCall("c1", "read_file", `{}`)
	tree.OnToolResult("c1", "ok", false)
	tree.OnTurnDone()

	body := chatStatusBody(m, "·")
	require.Contains(t, body, "ready  ·  last turn")
	require.NotContains(t, body, "idle  ·  ready")
}

