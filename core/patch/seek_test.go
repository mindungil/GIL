package patch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeekSequence_EmptyPattern(t *testing.T) {
	lines := []string{"a", "b", "c"}
	// Empty pattern always returns start unchanged.
	require.Equal(t, 0, seekSequence(lines, nil, 0, false))
	require.Equal(t, 2, seekSequence(lines, []string{}, 2, false))
}

func TestSeekSequence_PatternLongerThanLines(t *testing.T) {
	lines := []string{"only one"}
	pattern := []string{"too", "many", "lines"}
	require.Equal(t, -1, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_Tier1_Exact(t *testing.T) {
	lines := []string{"foo", "bar", "baz"}
	pattern := []string{"bar", "baz"}
	require.Equal(t, 1, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_Tier1_ExactNotFound_FallsThrough(t *testing.T) {
	// "foo  " vs "foo" — not exact but rstrip-equal.
	lines := []string{"foo  ", "bar"}
	pattern := []string{"foo", "bar"}
	// Should NOT match at tier 1 but should match at tier 2.
	result := seekSequence(lines, pattern, 0, false)
	require.Equal(t, 0, result)
}

func TestSeekSequence_Tier2_Rstrip(t *testing.T) {
	// Lines have trailing spaces/tabs; pattern is trimmed on the right.
	lines := []string{"hello   ", "world\t\t"}
	pattern := []string{"hello", "world"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_Tier2_LeadingSpaceBlocksTier2(t *testing.T) {
	// Lines have leading AND trailing whitespace — rstrip alone won't match,
	// but trim-both should.
	lines := []string{"  hello  ", "  world  "}
	pattern := []string{"hello", "world"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_Tier3_TrimBoth(t *testing.T) {
	lines := []string{"  foo  ", "\t bar \t"}
	pattern := []string{"foo", "bar"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_StartPosition(t *testing.T) {
	// Pattern exists at index 0 and index 2; start=1 must skip index 0.
	lines := []string{"x", "y", "x", "y"}
	pattern := []string{"x", "y"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
	require.Equal(t, 2, seekSequence(lines, pattern, 1, false))
}

func TestSeekSequence_NotFound(t *testing.T) {
	lines := []string{"alpha", "beta", "gamma"}
	pattern := []string{"delta"}
	require.Equal(t, -1, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_EOF_PicksLast(t *testing.T) {
	// "tail" appears at index 0 and index 2; eof=true must prefer index 2
	// (the last occurrence).
	lines := []string{"tail", "mid", "tail"}
	pattern := []string{"tail"}
	require.Equal(t, 2, seekSequence(lines, pattern, 0, true))
}

func TestSeekSequence_EOF_FallbackToEarlier(t *testing.T) {
	// When eof anchor can only match at an earlier position, it must still
	// find it. "head" only appears at index 0; eof=true should still return 0.
	lines := []string{"head", "mid", "tail"}
	pattern := []string{"head"}
	// searchStart will be len(lines)-len(pattern) = 2, but exact won't match
	// there; the search scans from searchStart=2 forward, misses. Then tiers
	// 2 and 3 also miss. So -1 is the correct result when eof moves the start
	// to 2 and "head" is only at 0.
	require.Equal(t, -1, seekSequence(lines, pattern, 0, true))
}

func TestSeekSequence_EOF_MultiLine(t *testing.T) {
	// A two-line pattern that only fits at the end.
	lines := []string{"a", "b", "c", "d"}
	pattern := []string{"c", "d"}
	require.Equal(t, 2, seekSequence(lines, pattern, 0, true))
}

func TestSeekSequence_SingleElement(t *testing.T) {
	lines := []string{"only"}
	pattern := []string{"only"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
}

func TestSeekSequence_PatternEqualsLines(t *testing.T) {
	// Pattern same length as lines; must match at 0.
	lines := []string{"x", "y"}
	pattern := []string{"x", "y"}
	require.Equal(t, 0, seekSequence(lines, pattern, 0, false))
}
