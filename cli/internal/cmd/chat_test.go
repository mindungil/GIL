package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/cmd/uistyle"
	"github.com/mindungil/gil/sdk"
)

// TestRenderChatBanner asserts the chat surface header keeps the spec's
// vocabulary: letterspaced "G I L", version, the active-session line
// when applicable. We render with NO_COLOR so the substring assertions
// are stable across terminal types.
func TestRenderChatBanner(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	g := uistyle.NewGlyphs(false)
	p := uistyle.NewPalette(true)

	renderChatBanner(&buf, g, p, 0)
	out := buf.String()
	require.Contains(t, out, "G I L", "letterspaced header")
	require.Contains(t, out, "AUTONOMOUS", "subtitle present")
	require.Contains(t, out, "Standing by. Describe the mission.", "mission-briefing prompt")
	require.Contains(t, out, "No active sessions.", "explicit empty-state line when count=0")

	buf.Reset()
	renderChatBanner(&buf, g, p, 1)
	out = buf.String()
	require.Contains(t, out, "1 active session.", "singular form for one session")

	buf.Reset()
	renderChatBanner(&buf, g, p, 4)
	out = buf.String()
	require.Contains(t, out, "4 active sessions.", "plural form")
}

// TestRenderChatStatus checks the conversational session listing. We
// expect short IDs, status glyphs, and a truncated goal — but no budget
// columns (those live in the verb-mode surfaces).
func TestRenderChatStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	g := uistyle.NewGlyphs(false)
	p := uistyle.NewPalette(true)

	renderChatStatus(&buf, g, p, []*sdk.Session{
		{ID: "01ABCDEFGH", Status: "RUNNING", GoalHint: "Add dark mode"},
		{ID: "01XYZQWERTY", Status: "DONE", GoalHint: "Migrate auth to OAuth2"},
	})
	out := buf.String()
	require.Contains(t, out, "2 session(s)")
	require.Contains(t, out, "01abcd")
	require.Contains(t, out, "Add dark mode")
	require.Contains(t, out, "01xyzq")
	require.Contains(t, out, "Migrate auth to OAuth2")
}

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

// TestMatchSessionByPrefix checks the resume-fast-path helper.
func TestMatchSessionByPrefix(t *testing.T) {
	in := []*sdk.Session{
		{ID: "01HXYZAB"},
		{ID: "01HXYZCD"},
		{ID: "02ABCD"},
		nil,
	}
	require.Len(t, matchSessionByPrefix(in, "01hxyz"), 2)
	require.Len(t, matchSessionByPrefix(in, "02"), 1)
	require.Len(t, matchSessionByPrefix(in, "ff"), 0)
}

// TestIsQuitWord covers the chat surface's exit lexicon.
func TestIsQuitWord(t *testing.T) {
	for _, w := range []string{"/quit", "/q", "/exit", "quit", "exit", "bye", "  QUIT  "} {
		require.True(t, isQuitWord(w), "%q should be a quit word", w)
	}
	for _, w := range []string{"", "continue", "yes", "no", "/help"} {
		require.False(t, isQuitWord(w), "%q should not be a quit word", w)
	}
}

// TestRenderChatHelp ensures the help text mentions the four core
// capabilities so users can discover them. The exact wording is not
// asserted — we only check the load-bearing nouns.
func TestRenderChatHelp(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderChatHelp(&buf, uistyle.NewGlyphs(false), uistyle.NewPalette(true))
	out := buf.String()
	for _, want := range []string{"task", "continue", "status", "/quit"} {
		require.True(t, strings.Contains(out, want), "help text should mention %q (got %q)", want, out)
	}
}

// TestRenderChatExplain asserts the meta-explanation hits the three
// stages of the gil flow so users learn the harness's vocabulary.
func TestRenderChatExplain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	renderChatExplain(&buf, uistyle.NewGlyphs(false), uistyle.NewPalette(true))
	out := buf.String()
	for _, want := range []string{"Interview", "Freeze", "Run", "agent loop"} {
		require.True(t, strings.Contains(out, want), "explain text should mention %q", want)
	}
}

// TestIntentModelFor maps known providers to their small-model defaults.
// When --model is set, every provider returns the user-supplied value.
func TestIntentModelFor(t *testing.T) {
	require.Equal(t, "claude-haiku-4-5", intentModelFor("anthropic", ""))
	require.Equal(t, "gpt-4o-mini", intentModelFor("openai", ""))
	require.Equal(t, "anthropic/claude-haiku-4-5", intentModelFor("openrouter", ""))
	require.Equal(t, "", intentModelFor("vllm", ""))
	require.Equal(t, "user-pin", intentModelFor("anthropic", "user-pin"))
}

// TestShortHex truncates SHA-style strings to a 12-char glanceable
// prefix — short enough for a chat line, unique enough to disambiguate.
func TestShortHex(t *testing.T) {
	require.Equal(t, "abcdef012345", shortHex("abcdef0123456789"))
	require.Equal(t, "ab", shortHex("ab"))
}
