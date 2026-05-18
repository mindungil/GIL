package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// P66 — consecutive-tool-timeout abort. Pure-function tests pinning
// the prefix match for isToolTimeoutResult. The end-to-end loop
// behavior (3 consecutive timeouts → tool_timeout_loop Done part)
// is exercised indirectly through dogfood runs against tasks that
// produce hung subprocesses.

func TestIsToolTimeoutResult_BashShape(t *testing.T) {
	// agent_tools_write.go run_bash emits this prefix on context
	// deadline exceeded.
	require.True(t, isToolTimeoutResult("timeout after 30s\n--- partial output ---\n"))
	require.True(t, isToolTimeoutResult("timeout after 1m0s\n"))
}

func TestIsToolTimeoutResult_VerifyShape(t *testing.T) {
	// agent_tools_plan_verify.go formats its verdict prefix.
	require.True(t, isToolTimeoutResult("[TIMEOUT] run tests — exit=124, duration=60s\n$ go test ./..."))
}

func TestIsToolTimeoutResult_NonTimeoutFailures(t *testing.T) {
	// Real errors that should NOT trigger the loop:
	require.False(t, isToolTimeoutResult("$ go build .\n[exit 1]\n./main.go:5: undefined: foo"))
	require.False(t, isToolTimeoutResult("[FAIL] verify — exit=1, duration=2s"))
	require.False(t, isToolTimeoutResult("path escapes session root"))
	require.False(t, isToolTimeoutResult(""))
}
