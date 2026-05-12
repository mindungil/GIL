package compact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/provider"
	"github.com/stretchr/testify/require"
)

func TestBuildSummaryPrompt_ContainsRequiredHeadings(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "build a CLI"},
		{Role: provider.RoleAssistant, Content: "plan: 1) create cmd 2) parse flags"},
	}
	p := BuildSummaryPrompt(msgs)
	for _, want := range []string{"## Goal", "## Constraints & Preferences", "## Progress", "### Done", "### In Progress", "### Blocked"} {
		require.Contains(t, p, want, "expected heading %q in prompt", want)
	}
}

func TestBuildSummaryPrompt_EndsWithConversationBlock(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	}
	p := BuildSummaryPrompt(msgs)
	require.Contains(t, p, "Conversation segment:")
	// Conversation block should appear AFTER the rules
	rulesIdx := strings.Index(p, "Output the markdown immediately")
	convIdx := strings.Index(p, "Conversation segment:")
	require.Greater(t, convIdx, rulesIdx, "conversation block must come after the rules")
	require.Contains(t, p, "[user] hello")
}

func TestBuildSummaryPrompt_FormatsToolCalls(t *testing.T) {
	msgs := []provider.Message{
		{
			Role:    provider.RoleAssistant,
			Content: "calling tool",
			ToolCalls: []provider.ToolCall{
				{ID: "x", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
			},
		},
	}
	p := BuildSummaryPrompt(msgs)
	require.Contains(t, p, "→ tool_call bash")
	require.Contains(t, p, `{"command":"ls"}`)
}

func TestBuildSummaryPrompt_FormatsToolResults_IncludingErrors(t *testing.T) {
	msgs := []provider.Message{
		{
			Role: provider.RoleUser,
			ToolResults: []provider.ToolResult{
				{ToolUseID: "x", Content: "file not found", IsError: true},
				{ToolUseID: "y", Content: "ok"},
			},
		},
	}
	p := BuildSummaryPrompt(msgs)
	require.Contains(t, p, "← tool_result ERROR file not found")
	require.Contains(t, p, "← tool_result ok")
	require.NotContains(t, p, "ERROR ok")
}

func TestBuildSummaryPrompt_NewlinesInContentAreFlattened(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "line1\nline2\nline3"},
	}
	p := BuildSummaryPrompt(msgs)
	// Each message becomes one line in the formatted output
	require.Contains(t, p, "[user] line1 line2 line3")
}

func TestBuildSummaryPrompt_DeterministicForSameInput(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "a"},
		{Role: provider.RoleAssistant, Content: "b"},
	}
	require.Equal(t, BuildSummaryPrompt(msgs), BuildSummaryPrompt(msgs))
}
