package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
)

func seedOrphanSession(t *testing.T, sessionsDir, sessionID string, evts []event.Event) {
	t.Helper()
	dir := filepath.Join(sessionsDir, sessionID, "events")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p, err := event.NewPersister(dir)
	require.NoError(t, err)
	for _, e := range evts {
		require.NoError(t, p.Write(e))
	}
	require.NoError(t, p.Sync())
	require.NoError(t, p.Close())
}

func makeOrphanEvt(ts time.Time, reason string, autoResume bool) event.Event {
	data, _ := json.Marshal(map[string]any{
		"reason": reason, "prior_status": "running", "auto_resume": autoResume,
	})
	return event.Event{ID: 1, Timestamp: ts, Source: event.SourceSystem, Kind: event.KindAction, Type: "run_orphaned", Data: data}
}

func TestLoadOrphanRow_ParsesRunOrphanedEvent(t *testing.T) {
	sessionsDir := t.TempDir()
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	seedOrphanSession(t, sessionsDir, "01ABCDEFGH0000000000000001", []event.Event{makeOrphanEvt(ts, "daemon_restart", false)})
	row, found := loadOrphanRow(sessionsDir, "01ABCDEFGH0000000000000001", "test goal", "Stopped")
	require.True(t, found)
	require.Equal(t, "01ABCDEFGH0000000000000001", row.ID)
	require.Equal(t, "test goal", row.GoalHint)
	require.Equal(t, "daemon_restart", row.Reason)
	require.False(t, row.AutoResume)
	require.Equal(t, ts, row.OrphanedAt)
	require.Equal(t, "stopped", row.Status)
}

func TestLoadOrphanRow_NoRunOrphanedEvent(t *testing.T) {
	sessionsDir := t.TempDir()
	seedOrphanSession(t, sessionsDir, "01ABCDEFGH0000000000000002", []event.Event{
		{ID: 1, Timestamp: time.Now(), Source: event.SourceAgent, Kind: event.KindAction, Type: "provider_request", Data: []byte(`{}`)},
	})
	_, found := loadOrphanRow(sessionsDir, "01ABCDEFGH0000000000000002", "goal", "Running")
	require.False(t, found)
}

func TestLoadOrphanRow_MissingEventsFile(t *testing.T) {
	sessionsDir := t.TempDir()
	_, found := loadOrphanRow(sessionsDir, "01NONEXIST0000000000000000", "goal", "Running")
	require.False(t, found)
}

func TestLoadOrphanRow_MultipleOrphanedReturnsLatest(t *testing.T) {
	sessionsDir := t.TempDir()
	ts1 := time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2025, 6, 1, 14, 0, 0, 0, time.UTC)
	seedOrphanSession(t, sessionsDir, "01ABCDEFGH0000000000000003", []event.Event{
		makeOrphanEvt(ts1, "daemon_restart", false),
		{ID: 2, Timestamp: ts1.Add(30 * time.Minute), Source: event.SourceAgent, Kind: event.KindAction, Type: "provider_request", Data: []byte(`{}`)},
	})
	// Write second orphan via a fresh persister append
	dir := filepath.Join(sessionsDir, "01ABCDEFGH0000000000000003", "events")
	p, err := event.NewPersister(dir)
	require.NoError(t, err)
	require.NoError(t, p.Write(makeOrphanEvt(ts2, "stale_heartbeat", true)))
	require.NoError(t, p.Sync())
	require.NoError(t, p.Close())

	row, found := loadOrphanRow(sessionsDir, "01ABCDEFGH0000000000000003", "goal", "Stopped")
	require.True(t, found)
	require.Equal(t, "stale_heartbeat", row.Reason)
	require.True(t, row.AutoResume)
	require.Equal(t, ts2, row.OrphanedAt)
}

func TestLoadOrphanRow_AutoResumeTrue(t *testing.T) {
	sessionsDir := t.TempDir()
	seedOrphanSession(t, sessionsDir, "01ABCDEFGH0000000000000004", []event.Event{makeOrphanEvt(time.Now(), "daemon_restart", true)})
	row, found := loadOrphanRow(sessionsDir, "01ABCDEFGH0000000000000004", "goal", "Stopped")
	require.True(t, found)
	require.True(t, row.AutoResume)
}

func TestLoadOrphanRow_AutoResumeFalse(t *testing.T) {
	sessionsDir := t.TempDir()
	seedOrphanSession(t, sessionsDir, "01ABCDEFGH0000000000000005", []event.Event{makeOrphanEvt(time.Now(), "stale_heartbeat", false)})
	row, found := loadOrphanRow(sessionsDir, "01ABCDEFGH0000000000000005", "goal", "Stopped")
	require.True(t, found)
	require.False(t, row.AutoResume)
}

func TestTruncateGoal_ShortStringUnchanged(t *testing.T) {
	require.Equal(t, "short goal", truncateGoal("short goal", 60))
}

func TestTruncateGoal_LongStringEllipsized(t *testing.T) {
	long := "this is a very long goal string that exceeds the maximum allowed length"
	result := truncateGoal(long, 20)
	// truncateGoal does s[:max-1] + "…" — byte slice of 19 + 3-byte ellipsis rune = 22 bytes
	require.Equal(t, "this is a very long…", result)
	require.True(t, len(result) > 0 && result[len(result)-3:] == "\u2026", "expected trailing ellipsis, got: %q", result)
}

func TestTruncateGoal_NewlinesReplacedWithSpaces(t *testing.T) {
	input := "line one\nline two\nline three"
	result := truncateGoal(input, 60)
	require.Equal(t, "line one line two line three", result)
}
