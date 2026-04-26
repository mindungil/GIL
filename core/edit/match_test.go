package edit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatch_Tier1_ExactMatch(t *testing.T) {
	whole := "alpha\nbeta\ngamma\ndelta\n"
	part := "beta\ngamma\n"
	m := &MatchEngine{}
	got, err := m.Find(whole, part)
	require.NoError(t, err)
	require.Equal(t, TierExact, got.Tier)
	require.Equal(t, 2, got.StartLine)
	require.Equal(t, 3, got.EndLine)
}

func TestMatch_Tier2_LeadingWhitespaceFlex(t *testing.T) {
	whole := "func F() {\n    if x {\n        y()\n    }\n}\n"
	// Part has same logical content but missing the 4-space leading indent
	part := "if x {\n    y()\n}\n"
	m := &MatchEngine{}
	got, err := m.Find(whole, part)
	require.NoError(t, err)
	require.Equal(t, TierLeadingWS, got.Tier)
}

func TestReplace_Tier2_PreservesIndent(t *testing.T) {
	whole := "func F() {\n    if x {\n        y()\n    }\n}\n"
	part := "if x {\n    y()\n}\n"
	replace := "if x {\n    z()\n}\n"
	m := &MatchEngine{}
	out, mt, err := m.Replace(whole, part, replace)
	require.NoError(t, err)
	require.Equal(t, TierLeadingWS, mt.Tier)
	// The replacement should have the original 4-space indent reapplied
	require.Contains(t, out, "    if x {\n        z()\n    }")
	require.NotContains(t, out, "        y()")
}

func TestMatch_Tier3_TrailingWhitespace(t *testing.T) {
	whole := "alpha   \nbeta\n"
	part := "alpha\nbeta\n"
	m := &MatchEngine{}
	got, err := m.Find(whole, part)
	require.NoError(t, err)
	require.Equal(t, TierTrailingWS, got.Tier)
}

func TestMatch_Tier4_FuzzyApproachThreshold(t *testing.T) {
	whole := "func Foo(name string) error {\n    if name == \"\" {\n        return ErrEmpty\n    }\n    return nil\n}\n"
	// part has a small typo but >80% similar
	part := "func Foo(name string) error {\n    if name == \"\" {\n        return ErrBlank\n    }\n    return nil\n}\n"
	m := &MatchEngine{FuzzyThreshold: 0.8}
	got, err := m.Find(whole, part)
	require.NoError(t, err)
	require.Equal(t, TierFuzzy, got.Tier)
	require.GreaterOrEqual(t, got.Score, 0.8)
}

func TestMatch_NoMatch(t *testing.T) {
	whole := "foo\nbar\nbaz\n"
	part := "completely different\nunrelated lines\n"
	m := &MatchEngine{}
	_, err := m.Find(whole, part)
	require.ErrorIs(t, err, ErrNoMatch)
}

func TestReplace_Tier1_ExactReplacement(t *testing.T) {
	whole := "a\nold\nold2\nz\n"
	part := "old\nold2\n"
	replace := "new\nnew2\n"
	m := &MatchEngine{}
	out, _, err := m.Replace(whole, part, replace)
	require.NoError(t, err)
	require.Equal(t, "a\nnew\nnew2\nz\n", out)
}

func TestReplace_AppendTrailingNewlineIfMissing(t *testing.T) {
	// Aider's prep ensures trailing newline. Verify same semantics.
	whole := "a\nb\nc" // no trailing \n
	part := "b\n"
	replace := "B\n"
	m := &MatchEngine{}
	out, _, err := m.Replace(whole, part, replace)
	require.NoError(t, err)
	require.Equal(t, "a\nB\nc\n", out)
}

func TestSequenceRatio_BasicSanity(t *testing.T) {
	require.InDelta(t, 1.0, sequenceRatio("abc", "abc"), 0.001)
	require.InDelta(t, 0.0, sequenceRatio("abc", "xyz"), 0.001)
	// partial overlap → between 0 and 1
	r := sequenceRatio("abcdef", "abxdef")
	require.Greater(t, r, 0.5)
	require.Less(t, r, 1.0)
}

func TestTier_String(t *testing.T) {
	require.Equal(t, "exact", TierExact.String())
	require.Equal(t, "leading_ws", TierLeadingWS.String())
	require.Equal(t, "trailing_ws", TierTrailingWS.String())
	require.Equal(t, "fuzzy", TierFuzzy.String())
}

func TestFind_PrefersHigherTier(t *testing.T) {
	// Same chunk appears with exact match earlier and as fuzzy candidate later;
	// exact must win.
	whole := "TARGET\nfoo\nbar\nTARGetx\n"
	part := "TARGET\n"
	m := &MatchEngine{FuzzyThreshold: 0.8}
	got, err := m.Find(whole, part)
	require.NoError(t, err)
	require.Equal(t, TierExact, got.Tier)
	require.Equal(t, 1, got.StartLine)
}
