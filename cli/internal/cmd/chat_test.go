package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

// TestFilterActiveSessions covers Phase 24 § E pruning behaviour.
// CREATED sessions older than 24h with no events are abandoned dummies
// and should not pollute the chat preamble; everything else stays.
func TestFilterActiveSessions(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	in := []*sdk.Session{
		{ID: "abandoned-old", Status: "CREATED", CreatedAt: old},
		{ID: "fresh-created", Status: "CREATED", CreatedAt: recent},
		{ID: "running", Status: "RUNNING", CreatedAt: old},
		{ID: "done-old", Status: "DONE", CreatedAt: old},
		nil, // tolerated
		{ID: "no-timestamp", Status: "CREATED"}, // CreatedAt zero → kept
	}
	got := filterActiveSessions(in)
	require.Len(t, got, 4, "only the abandoned-old row should be dropped")
	ids := make([]string, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	require.NotContains(t, ids, "abandoned-old")
	require.Contains(t, ids, "fresh-created")
	require.Contains(t, ids, "running")
	require.Contains(t, ids, "done-old")
	require.Contains(t, ids, "no-timestamp")
}

// TestRunChat_HandsOffToREPL_AtSessionReady verifies that a fully
// initialised gil home (init done + creds present) causes runChat to
// pass the onboarding gate and reach the REPL layer.
//
// This test requires a live daemon (ensureDaemon → gild binary) which
// is not available in the unit-test harness. It is therefore skipped
// here and will be exercised in T16 dogfood / integration suite.
//
// The onboarding-gate contracts are covered by the companion tests in
// chat_onboarding_test.go (TestDetectPreDaemonState_*). The REPL layer
// itself is covered by cli/internal/chat/repl tests.
func TestRunChat_HandsOffToREPL_AtSessionReady(t *testing.T) {
	t.Skip("requires live gild daemon — deferred to T16 integration suite")
}
