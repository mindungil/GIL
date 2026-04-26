package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runStatsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := statsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestStats_NoSessions(t *testing.T) {
	withCostEnv(t)
	out, err := runStatsCmd(t)
	require.NoError(t, err)
	require.Contains(t, out, "0 sessions")
	require.Contains(t, out, "no sessions to summarise")
}

func TestStats_AggregatesAcrossModels(t *testing.T) {
	layout := withCostEnv(t)
	now := time.Now()
	// Two sessions on opus, one on sonnet.
	seedSession(t, layout, "01OPUS000000000000000000A", "claude-opus-4-7", now,
		struct{ in, out int64 }{1_000_000, 100_000},
	)
	seedSession(t, layout, "01OPUS000000000000000000B", "claude-opus-4-7", now,
		struct{ in, out int64 }{500_000, 50_000},
	)
	seedSession(t, layout, "01SONNT000000000000000000C", "claude-sonnet-4-6", now,
		struct{ in, out int64 }{2_000_000, 200_000},
	)

	out, err := runStatsCmd(t)
	require.NoError(t, err)
	require.Contains(t, out, "3 sessions")
	require.Contains(t, out, "claude-opus-4-7")
	require.Contains(t, out, "claude-sonnet-4-6")
	// opus: 2 sessions; sonnet: 1
	require.Contains(t, out, "2 sessions")
	require.Contains(t, out, "1 sessions")

	// JSON form should match the same totals.
	jsonOut, err := runStatsCmd(t, "--json")
	require.NoError(t, err)
	var parsed statsReport
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &parsed))
	require.Equal(t, 3, parsed.Sessions)
	require.Len(t, parsed.ByModel, 2)

	var opus, sonnet *modelBreakdown
	for i := range parsed.ByModel {
		switch parsed.ByModel[i].Model {
		case "claude-opus-4-7":
			opus = &parsed.ByModel[i]
		case "claude-sonnet-4-6":
			sonnet = &parsed.ByModel[i]
		}
	}
	require.NotNil(t, opus)
	require.NotNil(t, sonnet)
	require.Equal(t, 2, opus.Sessions)
	require.Equal(t, int64(1_500_000), opus.InputTokens)
	require.Equal(t, int64(150_000), opus.OutputTokens)
	// opus cost: 1.5M * $15 + 150K * $75 = $22.5 + $11.25 = $33.75
	require.InDelta(t, 33.75, opus.CostUSD, 0.01)
	require.Equal(t, 1, sonnet.Sessions)
	// sonnet cost: 2M * $3 + 200K * $15 = $6 + $3 = $9
	require.InDelta(t, 9.00, sonnet.CostUSD, 0.01)
	// totals
	require.InDelta(t, 42.75, parsed.Totals.CostUSD, 0.01)
}

func TestStats_DaysWindowFiltersOldSessions(t *testing.T) {
	layout := withCostEnv(t)
	recent := time.Now()
	old := time.Now().Add(-60 * 24 * time.Hour)

	seedSession(t, layout, "01RECENT00000000000000000A", "claude-haiku-4-5", recent,
		struct{ in, out int64 }{1_000_000, 0},
	)
	seedSession(t, layout, "01OLD00000000000000000000B", "claude-haiku-4-5", old,
		struct{ in, out int64 }{1_000_000, 0},
	)

	// --days 30 → only the recent session should be counted.
	out, err := runStatsCmd(t, "--json", "--days", "30")
	require.NoError(t, err)
	var parsed statsReport
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, 1, parsed.Sessions)

	// --days 0 → all-time, both sessions counted.
	out, err = runStatsCmd(t, "--json", "--days", "0")
	require.NoError(t, err)
	parsed = statsReport{}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, 2, parsed.Sessions)
}

func TestStats_NegativeDaysRejected(t *testing.T) {
	withCostEnv(t)
	_, err := runStatsCmd(t, "--days", "-1")
	require.Error(t, err)
}
