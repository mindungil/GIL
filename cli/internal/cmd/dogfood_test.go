package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P61 — dogfood runner pure-function tests.
// The integration shape (cli + grpc + recovery loop) is exercised by
// the live dogfood runs themselves; here we pin the small, testable
// helpers that decide turn behavior.

func TestRecoveryPromptFor_EndTurnNoTools_ReturnsEmpty(t *testing.T) {
	// Agent completed (end_turn) with no tool calls in the final turn
	// → treat as "done", caller exits the loop.
	rec := &turnRecord{StopReason: "end_turn", ToolCallCount: 0}
	require.Equal(t, "", recoveryPromptFor(rec))
}

func TestRecoveryPromptFor_EndTurnWithTools_KeepsGoing(t *testing.T) {
	// Agent did real work this turn but stopped — keep going so it
	// can summarize or continue.
	rec := &turnRecord{StopReason: "end_turn", ToolCallCount: 5}
	got := recoveryPromptFor(rec)
	require.Contains(t, got, "Continue executing")
}

func TestRecoveryPromptFor_VerifyMissing_IncludesVerifyTail(t *testing.T) {
	rec := &turnRecord{
		StopReason: "verify_missing",
		VerifyTail: "parser/node.go:4:2: \"chess/board\" imported and not used",
	}
	got := recoveryPromptFor(rec)
	require.Contains(t, got, "verify_missing")
	require.Contains(t, got, "imported and not used",
		"recovery prompt must include the actual verify error tail")
	require.Contains(t, got, "DO NOT ask",
		"non-interactive framing must be preserved")
}

func TestRecoveryPromptFor_VerifyMissing_NoTailIsHandled(t *testing.T) {
	// Verify never ran (or didn't produce parseable output) — still
	// emit the recovery prompt but without the tail block.
	rec := &turnRecord{StopReason: "verify_missing"}
	got := recoveryPromptFor(rec)
	require.NotEmpty(t, got)
	require.NotContains(t, got, "Last verify output")
}

func TestRecoveryPromptFor_Error_ExitsLoop(t *testing.T) {
	// Hard error from provider → give up, don't keep retrying.
	rec := &turnRecord{StopReason: "error"}
	require.Equal(t, "", recoveryPromptFor(rec))
}

func TestRecoveryPromptFor_ToolErrorLoop_GivesActionableRecovery(t *testing.T) {
	// P69 breaker fired. Recovery should nudge the agent to stop
	// repeating the malformed call and switch approach — not give up
	// (unlike tool_timeout_loop) and not the generic default.
	rec := &turnRecord{StopReason: "tool_error_loop"}
	got := recoveryPromptFor(rec)
	require.NotEmpty(t, got)
	require.Contains(t, got, "STOP repeating")
}

func TestRecoveryPromptFor_UnknownStopReason_ContinuesDefensively(t *testing.T) {
	rec := &turnRecord{StopReason: "some_future_reason"}
	got := recoveryPromptFor(rec)
	require.NotEmpty(t, got)
	require.Contains(t, got, "Continue")
}

func TestVerdictFromReason_AllPass(t *testing.T) {
	r := &dogfoodResult{
		Reason: "end_turn",
		Assertions: []assertResult{
			{Command: "go test", Passed: true},
		},
	}
	require.Equal(t, "PASS", verdictFromReason(r))
}

func TestVerdictFromReason_AssertionFails(t *testing.T) {
	r := &dogfoodResult{
		Reason: "end_turn",
		Assertions: []assertResult{
			{Command: "go test", Passed: false, Exit: 1},
		},
	}
	require.Equal(t, "FAIL", verdictFromReason(r))
}

func TestVerdictFromReason_BudgetExhausted_NoAssertion(t *testing.T) {
	for _, reason := range []string{"max_turns", "max_wall", "stalled"} {
		r := &dogfoodResult{Reason: reason}
		require.Equal(t, "INCOMPLETE", verdictFromReason(r),
			"budget-exhausted without assertions should be INCOMPLETE not PASS (reason=%s)", reason)
	}
}

func TestVerdictFromReason_BudgetExhausted_WithPassingAssertion(t *testing.T) {
	// If the user's assertion (e.g. "go test ./...") passes after a
	// max_turns/max_wall stop, the run is still INCOMPLETE because
	// the agent didn't naturally declare end_turn — even if the code
	// happens to work. The assertion success is independent of
	// the agent's self-reported completion.
	r := &dogfoodResult{
		Reason: "max_turns",
		Assertions: []assertResult{
			{Command: "go test", Passed: true},
		},
	}
	require.Equal(t, "INCOMPLETE", verdictFromReason(r))
}

func TestVerdictFromReason_DaemonGone(t *testing.T) {
	r := &dogfoodResult{Reason: "daemon_gone"}
	require.Equal(t, "ERROR", verdictFromReason(r))
}

func TestSummaryLine_UsesSameVerdictAsStructuredSummary(t *testing.T) {
	r := &dogfoodResult{
		Reason:      "stalled",
		Turns:       3,
		TotalWallMs: int64((2 * time.Second).Milliseconds()),
		SessionID:   "01DOGFOOD",
	}
	line := r.summaryLine()
	require.Contains(t, line, "INCOMPLETE")
	require.Equal(t, "INCOMPLETE", r.summaryRecord()["verdict"])
}

func TestDogfoodResultSuccessOnlyForPass(t *testing.T) {
	require.True(t, (&dogfoodResult{Reason: "end_turn"}).success())
	require.False(t, (&dogfoodResult{Reason: "max_turns"}).success())
	require.False(t, (&dogfoodResult{Reason: "stalled"}).success())
	require.False(t, (&dogfoodResult{Reason: "daemon_gone"}).success())
	require.False(t, (&dogfoodResult{
		Reason: "end_turn",
		Assertions: []assertResult{
			{Command: "go test", Exit: 1, Passed: false},
		},
	}).success())
}

func TestHead_LongStringTruncates(t *testing.T) {
	long := "this is a long string"
	require.Equal(t, "this is…", head(long, 7))
}

func TestHead_ShortStringPassThrough(t *testing.T) {
	require.Equal(t, "short", head("short", 100))
}

func TestTailRunes_LongStringTruncates(t *testing.T) {
	long := "abcdefghij"
	require.Equal(t, "…hij", tailRunes(long, 3))
}

func TestTailRunes_ShortStringPassThrough(t *testing.T) {
	require.Equal(t, "ok", tailRunes("ok", 100))
}

func TestTailLines_CutsAtLineBoundary(t *testing.T) {
	s := "header line\nstart of tail\nmiddle\nend of body"
	got := tailLines(s, 30)
	// The cut lands at "f tail\n..." so the helper advances to the
	// next newline boundary; result starts at "middle".
	require.Contains(t, got, "middle")
	require.Contains(t, got, "end of body")
	require.NotContains(t, got, "header line")
}

func TestIsDaemonGoneClient_Patterns(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"rpc error: code = Unavailable", true},
		{"dial unix /tmp/x: connect: connection refused", true},
		{"transport: error while dialing", true},
		{"broken pipe", true},
		{"401 unauthorized", false},
		{"invalid argument: bad goal", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			var err error
			if c.msg != "" {
				err = &stringErr{c.msg}
			}
			require.Equal(t, c.want, isDaemonGoneClient(err))
		})
	}
}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// P63b: assertionRecoveryPrompt frames the "agent said done but
// assertion failed" message clearly + includes the failure tail.
func TestAssertionRecoveryPrompt_ContainsTailAndFraming(t *testing.T) {
	tail := "Command: go test ./...\nExit: 1\nOutput tail:\nFAIL chess/movegen perft mismatch"
	got := assertionRecoveryPrompt(tail)
	require.Contains(t, got, "declared the task complete")
	require.Contains(t, got, "NOT done")
	require.Contains(t, got, "FAIL chess/movegen perft mismatch")
	require.Contains(t, got, "DO NOT ask")
}
