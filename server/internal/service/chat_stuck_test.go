package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// P39 — chat-side stuck detection. Pure-function tests on chatStuckSig
// and chatStuckCheck; plus an end-to-end Prompt test that scripts 3
// identical tool calls and asserts the warning Part lands on the stream.

func TestChatStuckSig_StableAndSensitive(t *testing.T) {
	// Same name + same input → same sig.
	a := chatStuckSig("read_file", []byte(`{"path":"main.go"}`))
	b := chatStuckSig("read_file", []byte(`{"path":"main.go"}`))
	require.Equal(t, a, b)

	// Same name, different input → different sig.
	c := chatStuckSig("read_file", []byte(`{"path":"other.go"}`))
	require.NotEqual(t, a, c)

	// Different name, same input → different sig.
	d := chatStuckSig("write_file", []byte(`{"path":"main.go"}`))
	require.NotEqual(t, a, d)

	// Both empty → still produces a stable sig (don't panic).
	e := chatStuckSig("", nil)
	f := chatStuckSig("", nil)
	require.Equal(t, e, f)
}

func TestChatStuckCheck_WindowSemantics(t *testing.T) {
	// Fewer than window → never fires.
	require.False(t, chatStuckCheck([]string{"a", "a"}, 3))

	// Window of 3, trailing 3 same → fires.
	require.True(t, chatStuckCheck([]string{"a", "a", "a"}, 3))

	// Trailing 3 same but earlier differ → still fires (only trailing matters).
	require.True(t, chatStuckCheck([]string{"x", "y", "a", "a", "a"}, 3))

	// Trailing not all same → no fire.
	require.False(t, chatStuckCheck([]string{"a", "a", "b"}, 3))

	// Different trailing patterns.
	require.False(t, chatStuckCheck([]string{"a", "b", "a"}, 3))
	require.False(t, chatStuckCheck([]string{"a", "b", "c"}, 3))

	// Zero/negative window → false.
	require.False(t, chatStuckCheck([]string{"a", "a", "a"}, 0))
	require.False(t, chatStuckCheck([]string{"a", "a", "a"}, -1))

	// Empty → false.
	require.False(t, chatStuckCheck(nil, 3))
}

// TestPrompt_RepeatedToolCalls_EmitsStuckWarning scripts 3 identical
// tool calls and verifies the chat surface receives the stuck_detected
// warning Part. The mock provider returns a read_file call on every
// turn; after 3 such turns the warning fires.
func TestPrompt_RepeatedToolCalls_EmitsStuckWarning(t *testing.T) {
	turns := []provider.MockTurn{
		// Turn 1: read_file
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 2: same read_file
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 3: same read_file — sweep at this point should fire stuck warning.
		{ToolCalls: []provider.ToolCall{{ID: "c3", Name: "read_file",
			Input: []byte(`{"path":"main.go"}`)}}, StopReason: "tool_use"},
		// Turn 4: stop
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	// Walk parts; find the system stuck warning.
	found := false
	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") && strings.Contains(td.GetContent(), "read_file") {
			found = true
			break
		}
	}
	require.True(t, found, "expected a stuck_detected system Part after 3 identical tool calls; got %d parts", len(stream.Parts))
}

// TestPrompt_DistinctToolCalls_NoStuckWarning: 3 different tool calls
// (or 3 read_file calls with different paths) must NOT fire the warning.
func TestPrompt_DistinctToolCalls_NoStuckWarning(t *testing.T) {
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file",
			Input: []byte(`{"path":"a.go"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read_file",
			Input: []byte(`{"path":"b.go"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{{ID: "c3", Name: "read_file",
			Input: []byte(`{"path":"c.go"}`)}}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read several files"), stream)

	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		require.NotContains(t, td.GetContent(), "stuck_detected",
			"3 distinct calls must NOT trigger stuck warning")
	}
}

// TestPrompt_StuckFiresOnce: even if the agent continues the stuck loop
// past the threshold, the warning must surface ONCE, not on every
// subsequent identical call.
func TestPrompt_StuckFiresOnce(t *testing.T) {
	identical := provider.ToolCall{ID: "c", Name: "read_file",
		Input: []byte(`{"path":"main.go"}`)}
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	count := 0
	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") {
			count++
		}
	}
	require.Equal(t, 1, count, "stuck warning must fire exactly once per turn, not on every identical call past threshold")
}

// Sanity: the warning Part is delivered as a Text part (not a Done
// or Reasoning part), so chat clients render it inline.
func TestPrompt_StuckWarning_IsTextPart(t *testing.T) {
	identical := provider.ToolCall{ID: "c", Name: "read_file",
		Input: []byte(`{"path":"main.go"}`)}
	turns := []provider.MockTurn{
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{ToolCalls: []provider.ToolCall{identical}, StopReason: "tool_use"},
		{Text: "done", StopReason: "end_turn"},
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "read main.go"), stream)

	for _, p := range stream.Parts {
		td := p.GetText()
		if td == nil {
			continue
		}
		if strings.Contains(td.GetContent(), "stuck_detected") {
			// Confirm it's a TextDelta (not Reasoning or Done).
			require.NotNil(t, p.GetText(), "stuck warning must be a Text part")
			require.IsType(t, &gilv1.Part_Text{}, p.Body)
			return
		}
	}
	t.Fatalf("did not find stuck_detected warning part")
}
