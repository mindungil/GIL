package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/sdk"
)

func TestSlugify_Basic(t *testing.T) {
	cases := map[string]string{
		"Add dark mode toggle":          "add-dark-mode-toggle",
		"  Fix the   FOO bar.  ":        "fix-the-foo-bar",
		"refactor: split big_module.go": "refactor-split-big-modul",
		"":                              "",
		"!!!":                           "",
		// Non-ASCII (Korean) drops every char — caller falls back to shortID.
		"안녕 다크모드":            "",
		"add 안녕 toggle":      "add-toggle",
		"v2.0 — bring it on": "v2-0-bring-it-on",
	}
	for in, want := range cases {
		got := slugify(in)
		require.Equal(t, want, got, "slugify(%q)", in)
	}
}

func TestSlugify_Cap24(t *testing.T) {
	in := "this is a very very long mission description that runs on forever"
	got := slugify(in)
	require.LessOrEqual(t, len(got), 24)
	// The cap should not leave a dangling hyphen.
	require.NotEqual(t, "-", got[len(got)-1:])
}

func TestDisplayName_FromGoal(t *testing.T) {
	s := &sdk.Session{
		ID:        "01HQXY7G8H9JMQRSV9XYZW000A",
		GoalHint:  "Add dark mode toggle",
		CreatedAt: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC),
	}
	require.Equal(t, "add-dark-mode-toggle-0428", displayName(s))
}

func TestDisplayName_FallsBackToShortID(t *testing.T) {
	// Korean-only goal → slug is empty → falls back to shortID.
	s := &sdk.Session{
		ID:        "01HQXY7G8H9JMQRSV9XYZW000A",
		GoalHint:  "안녕 다크모드 추가",
		CreatedAt: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC),
	}
	require.Equal(t, "01hqxy", displayName(s))
}

func TestDisplayName_NilSession(t *testing.T) {
	require.Equal(t, "", displayName(nil))
}

func TestRelTime_Buckets(t *testing.T) {
	now := time.Now()
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{0, "just now"},
		{30 * time.Second, "just now"},
		{60 * time.Second, "1m ago"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{75 * time.Minute, "1h ago"},
		{3 * time.Hour, "3h ago"},
		{36 * time.Hour, "yesterday"},
		{5 * 24 * time.Hour, "5d ago"},
	}
	for _, c := range cases {
		got := relTime(now.Add(-c.ago))
		require.Equal(t, c.want, got, "ago=%s", c.ago)
	}
}

func TestRelTime_ZeroIsEmpty(t *testing.T) {
	require.Equal(t, "", relTime(time.Time{}))
}

func TestRelTime_FutureCollapsesToJustNow(t *testing.T) {
	// Clock skew: server timestamp is 2s in the future. Don't render
	// "in 2s" — collapse to "just now" so users don't think gil sees
	// the future.
	require.Equal(t, "just now", relTime(time.Now().Add(2*time.Second)))
}

func TestDisplayName_ZeroCreatedAtUsesNow(t *testing.T) {
	s := &sdk.Session{
		ID:       "01HQXY7G8H9JMQRSV9XYZW000A",
		GoalHint: "Quick fix",
	}
	got := displayName(s)
	// Doesn't matter what today's MMDD is — just that we got a slug+date,
	// not a fallback to shortID.
	require.Contains(t, got, "quick-fix-")
	require.Len(t, got[len("quick-fix-"):], 4)
}
