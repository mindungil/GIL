package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/paths"
)

// withCostEnv pins HOME, GIL_HOME, XDG_* so `gil cost` and `gil stats`
// operate entirely under t.TempDir(). Returns the resolved Layout for the
// caller's convenience.
func withCostEnv(t *testing.T) paths.Layout {
	t.Helper()
	gilHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIL_HOME", gilHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	require.NoError(t, layout.EnsureDirs())
	return layout
}

// seedSession writes a synthetic events.jsonl for sessionID under layout's
// SessionsDir. The first event is a provider_request carrying the model;
// subsequent provider_response events carry input/output token counts.
// firstTS is the timestamp on event 1 — used by stats tests to fall
// inside or outside a window.
func seedSession(t *testing.T, layout paths.Layout, sessionID, model string, firstTS time.Time, responses ...struct{ in, out int64 }) {
	t.Helper()
	dir := filepath.Join(layout.SessionsDir(), sessionID, "events")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p, err := event.NewPersister(dir)
	require.NoError(t, err)
	defer p.Close()

	require.NoError(t, p.Write(event.Event{
		ID:        1,
		Timestamp: firstTS,
		Source:    event.SourceAgent,
		Kind:      event.KindAction,
		Type:      "provider_request",
		Data:      []byte(`{"model":"` + model + `","msgs":1,"tools":0}`),
	}))
	for i, r := range responses {
		body, err := json.Marshal(map[string]any{
			"input_tokens":  r.in,
			"output_tokens": r.out,
		})
		require.NoError(t, err)
		require.NoError(t, p.Write(event.Event{
			ID:        int64(i + 2),
			Timestamp: firstTS.Add(time.Duration(i+1) * time.Second),
			Source:    event.SourceAgent,
			Kind:      event.KindObservation,
			Type:      "provider_response",
			Data:      body,
		}))
	}
	require.NoError(t, p.Sync())
}

func runCostCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := costCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestCost_KnownModelMatchesExpected(t *testing.T) {
	layout := withCostEnv(t)
	now := time.Now()
	// 1M input + 100K output for haiku-4-5 → 1*1.00 + 0.1*5.00 = $1.50
	seedSession(t, layout, "01TEST00000000000000000HAIKU", "claude-haiku-4-5", now,
		struct{ in, out int64 }{500_000, 50_000},
		struct{ in, out int64 }{500_000, 50_000},
	)
	out, err := runCostCmd(t, "01TEST00000000000000000HAIKU")
	require.NoError(t, err)
	require.Contains(t, out, "claude-haiku-4-5")
	require.Contains(t, out, "anthropic")
	require.Contains(t, out, "1,000,000")
	require.Contains(t, out, "100,000")
	require.Contains(t, out, "$1.5000")
}

func TestCost_DefaultsToLatestSession(t *testing.T) {
	layout := withCostEnv(t)
	now := time.Now()
	// ULIDs sort lexicographically; the "B" prefix sorts after "A" so the
	// "B" session is the "latest".
	seedSession(t, layout, "01AAAA00000000000000000000", "claude-haiku-4-5", now,
		struct{ in, out int64 }{1000, 1000},
	)
	seedSession(t, layout, "01BBBB00000000000000000000", "gpt-4o-mini", now,
		struct{ in, out int64 }{2000, 2000},
	)
	out, err := runCostCmd(t)
	require.NoError(t, err)
	require.Contains(t, out, "01BBBB00000000000000000000")
	require.Contains(t, out, "gpt-4o-mini")
}

func TestCost_NoSessionsErrors(t *testing.T) {
	withCostEnv(t)
	_, err := runCostCmd(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no sessions")
}

func TestCost_UnknownSessionErrors(t *testing.T) {
	withCostEnv(t)
	_, err := runCostCmd(t, "01DOESNOTEXIST")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no events")
}

func TestCost_JSONOutputParses(t *testing.T) {
	layout := withCostEnv(t)
	seedSession(t, layout, "01TEST000000000000000JSON1", "claude-sonnet-4-6", time.Now(),
		struct{ in, out int64 }{1_000_000, 100_000},
	)
	out, err := runCostCmd(t, "--json", "01TEST000000000000000JSON1")
	require.NoError(t, err)

	var parsed sessionCostReport
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, "01TEST000000000000000JSON1", parsed.Session)
	require.Equal(t, "claude-sonnet-4-6", parsed.Model)
	require.Equal(t, "anthropic", parsed.Provider)
	require.Equal(t, int64(1_000_000), parsed.Tokens.Input)
	require.Equal(t, int64(100_000), parsed.Tokens.Output)
	// 1M * $3 + 100K * $15 = $3 + $1.5 = $4.5
	require.InDelta(t, 4.50, parsed.CostUSD, 0.001)
	require.True(t, parsed.Known)
}

func TestCost_OutputJSONFlagAlias(t *testing.T) {
	// The persistent --output json flag should produce the same payload
	// shape as the legacy --json flag.
	layout := withCostEnv(t)
	seedSession(t, layout, "01TEST00000000000000OUTJ1", "claude-sonnet-4-6", time.Now(),
		struct{ in, out int64 }{1_000_000, 100_000},
	)

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	out, err := runCostCmd(t, "01TEST00000000000000OUTJ1")
	require.NoError(t, err)
	var parsed sessionCostReport
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, "01TEST00000000000000OUTJ1", parsed.Session)
	require.Equal(t, "claude-sonnet-4-6", parsed.Model)
	require.True(t, parsed.Known)
}

func TestCost_LegacyJSONStillWorks(t *testing.T) {
	// --json must keep producing JSON even when --output is the default.
	layout := withCostEnv(t)
	seedSession(t, layout, "01TEST00000000000000LEGAC", "claude-haiku-4-5", time.Now(),
		struct{ in, out int64 }{1000, 100},
	)
	out, err := runCostCmd(t, "--json", "01TEST00000000000000LEGAC")
	require.NoError(t, err)
	var parsed sessionCostReport
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, "01TEST00000000000000LEGAC", parsed.Session)
}

func TestCost_UnknownModelReturnsZeroAndFlag(t *testing.T) {
	layout := withCostEnv(t)
	seedSession(t, layout, "01TEST000000000000000UNKWN", "made-up-model-x", time.Now(),
		struct{ in, out int64 }{100, 100},
	)
	out, err := runCostCmd(t, "--json", "01TEST000000000000000UNKWN")
	require.NoError(t, err)

	var parsed sessionCostReport
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.False(t, parsed.Known)
	require.Equal(t, 0.0, parsed.CostUSD)
}
