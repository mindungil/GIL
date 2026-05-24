package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/sdk"
)

// fakeWatchFetcher returns the same snapshot every call. The runWatch
// loop will exit after one frame because the test passes once=true.
type fakeWatchFetcher struct {
	snap *watchSnapshot
}

func (f *fakeWatchFetcher) Snapshot(ctx context.Context) (*watchSnapshot, error) {
	return f.snap, nil
}

func TestWatch_RendersOneFrame(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	workdir := initGitRepo(t, "feat/watch-goal", true)
	var buf bytes.Buffer
	r := &watchRenderer{
		out:     &buf,
		glyphs:  uistyle.NewGlyphs(false),
		palette: uistyle.NewPalette(true),
		clear:   false,
	}
	f := &fakeWatchFetcher{
		snap: &watchSnapshot{
			Session: &sdk.Session{
				ID:               "01ABCDEFG",
				Status:           "RUNNING",
				GoalHint:         "Add dark mode",
				WorkingDir:       workdir,
				CurrentIteration: 23,
			},
			FrozenGoal: "Add dark mode with proper contrast",
			GitSummary: "feat/watch-goal · dirty (1 files)",
			Tasks:      []string{"Audit theme tokens", "Wire toggle state"},
			Events: []watchEvent{
				{When: time.Date(2026, 4, 26, 18, 34, 0, 0, time.UTC), Iter: 22, Type: "tool_call", Body: "bash"},
				{When: time.Date(2026, 4, 26, 18, 35, 0, 0, time.UTC), Iter: 22, Type: "verify_result", Body: "ok"},
			},
		},
	}
	require.NoError(t, runWatch(context.Background(), r, f, time.Millisecond, true))
	out := buf.String()
	require.Contains(t, out, "G I L")
	require.Contains(t, out, "01abcd")
	require.Contains(t, out, "Add dark mode")
	require.Contains(t, out, "Frozen goal")
	require.Contains(t, out, "Audit theme tokens")
	require.Contains(t, out, "Git")
	require.Contains(t, out, "feat/watch-goal · dirty")
	require.Contains(t, out, "Progress")
	require.Contains(t, out, "23 / 100")
	require.Contains(t, out, "tool_call")
	require.Contains(t, out, "verify_result")
	require.Contains(t, out, "live · ctrl-c to exit")
}

// TestWatch_NoClearMeansNoEscapes guards the --no-clear contract:
// piping into less must not see the screen-clear escape.
func TestWatch_NoClearMeansNoEscapes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	r := &watchRenderer{
		out:     &buf,
		glyphs:  uistyle.NewGlyphs(false),
		palette: uistyle.NewPalette(true),
		clear:   false,
	}
	f := &fakeWatchFetcher{snap: &watchSnapshot{Session: &sdk.Session{ID: "01X", Status: "DONE", GoalHint: "g"}}}
	require.NoError(t, runWatch(context.Background(), r, f, time.Millisecond, true))
	require.False(t, strings.Contains(buf.String(), "\x1b[2J"), "no-clear must omit the screen-clear escape")
}

// TestWatch_ClearEmitsAnsi is the inverse: when clear=true (default),
// each frame begins with the screen-clear sequence.
func TestWatch_ClearEmitsAnsi(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	r := &watchRenderer{
		out:     &buf,
		glyphs:  uistyle.NewGlyphs(false),
		palette: uistyle.NewPalette(true),
		clear:   true,
	}
	f := &fakeWatchFetcher{snap: &watchSnapshot{Session: &sdk.Session{ID: "01X", Status: "DONE", GoalHint: "g"}}}
	require.NoError(t, runWatch(context.Background(), r, f, time.Millisecond, true))
	require.True(t, strings.Contains(buf.String(), "\x1b[2J"), "clear=true must emit screen-clear escape")
}
