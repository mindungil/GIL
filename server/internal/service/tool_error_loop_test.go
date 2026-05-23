package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
)

// P69 — consecutive-identical-tool-error abort. The chat agent loop
// must break when the model locks onto a malformed call that keeps
// returning the same error, instead of running all maxAgentTurns
// iterations and burning the wall budget. Real trigger (bytecode-vm
// dogfood, 2026-05-23): qwen3.6-27b emitted write_file with empty `{}`
// input 20+ times, each returning "missing required `path` arg".
func TestPrompt_RepeatedToolError_BreaksLoop(t *testing.T) {
	// Emit the same malformed write_file (empty input) on every turn,
	// more times than the breaker threshold. The real write_file tool
	// returns "missing required `path` arg" with IsError=true for each.
	var turns []provider.MockTurn
	for i := 0; i < 10; i++ {
		turns = append(turns, provider.MockTurn{
			ToolCalls:  []provider.ToolCall{{ID: "c", Name: "write_file", Input: []byte(`{}`)}},
			StopReason: "tool_use",
		})
	}
	svc, sid := newTestSessionServiceWithMockTurns(t, turns)
	stream := &fakePromptStream{ctx: context.Background()}
	_ = svc.Prompt(promptReq(sid, "write a file"), stream)

	// The loop must terminate via a tool_error_loop Done part — not run
	// all 10 scripted turns.
	sawErrorLoop := false
	doneCount := 0
	for _, p := range stream.Parts {
		if d := p.GetDone(); d != nil {
			doneCount++
			if d.GetStopReason() == "tool_error_loop" {
				sawErrorLoop = true
			}
		}
	}
	require.True(t, sawErrorLoop, "expected a Done part with stop_reason=tool_error_loop; got %d done parts", doneCount)

	// Sanity: the breaker fired well before the 10 scripted turns were
	// exhausted. Count distinct tool_call parts emitted.
	toolCalls := 0
	for _, p := range stream.Parts {
		if p.GetToolCall() != nil {
			toolCalls++
		}
	}
	require.LessOrEqual(t, toolCalls, 5, "breaker should fire within ~4 identical errors, saw %d tool calls", toolCalls)
}

// A different error each iteration must NOT trip the identical-error
// breaker — the streak only counts byte-identical (tool, error) pairs.
func TestErrorSignature_StableAndDistinct(t *testing.T) {
	// Identical bodies → identical signature.
	a := errorSignature("missing required `path` arg — the tool input JSON must include {\"path\":\"<file>\", ...}")
	b := errorSignature("missing required `path` arg — the tool input JSON must include {\"path\":\"<file>\", ...}")
	require.Equal(t, a, b)

	// Trailing variable detail past the first line is ignored.
	c := errorSignature("missing required `path` arg\nattempt 1")
	d := errorSignature("missing required `path` arg\nattempt 2")
	require.Equal(t, c, d)

	// Genuinely different reasons stay distinct.
	require.NotEqual(t, errorSignature("path escapes session root"), a)
}
