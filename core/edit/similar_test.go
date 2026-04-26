package edit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindSimilar_ExactReturnsMatched(t *testing.T) {
	// Search block whose first and last lines exactly match the chunk in content.
	// Expect: just the matched chunk, no extra context lines.
	content := strings.Join([]string{
		"package main",
		"",
		"import \"fmt\"",
		"",
		"func hello() {",
		"\tfmt.Println(\"hello\")",
		"}",
	}, "\n")

	search := strings.Join([]string{
		"func hello() {",
		"\tfmt.Println(\"hello\")",
		"}",
	}, "\n")

	result := FindSimilar(search, content, 0.6)
	require.NotEmpty(t, result, "should find a similar chunk above threshold")
	// First and last lines of matched chunk equal search lines → no widening.
	lines := strings.Split(result, "\n")
	require.Equal(t, "func hello() {", lines[0])
	require.Equal(t, "}", lines[len(lines)-1])
	require.Equal(t, 3, len(lines), "exact match: no extra context lines")
}

func TestFindSimilar_FuzzyAddsContextLines(t *testing.T) {
	// Search block that approximately (but not exactly) matches a chunk: first/last
	// lines differ slightly, so FindSimilar should widen by up to N=5 on each side.
	contentLines := []string{
		"line01", "line02", "line03", "line04", "line05",
		"func foo() {",
		"\tx := 1",
		"\treturn x",
		"}",
		"line10", "line11", "line12", "line13", "line14",
	}
	content := strings.Join(contentLines, "\n")

	// Slightly different first/last lines so widening kicks in.
	search := strings.Join([]string{
		"func foo() {",
		"\tx := 2", // differs from content's "\tx := 1"
		"}",        // differs: content has "\treturn x\n}" but last line matches
	}, "\n")

	result := FindSimilar(search, content, 0.5)
	// Result should be non-empty (similarity above 0.5).
	require.NotEmpty(t, result, "should find fuzzy match above threshold=0.5")
	// Should include extra context (lines before/after the matched region).
	resultLines := strings.Split(result, "\n")
	require.Greater(t, len(resultLines), 3, "widened result should have more than 3 lines")
}

func TestFindSimilar_BelowThreshold_ReturnsEmpty(t *testing.T) {
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	// Totally unrelated search block.
	search := "xyz\nabc\n123"

	result := FindSimilar(search, content, 0.6)
	require.Empty(t, result, "completely unrelated search should return empty below threshold")
}

func TestFindSimilar_ContentShorterThanSearch_ReturnsEmpty(t *testing.T) {
	content := "a\nb"
	search := "a\nb\nc\nd\ne"

	result := FindSimilar(search, content, 0.6)
	require.Empty(t, result, "content shorter than search should return empty")
}

func TestFindSimilar_DefaultThreshold(t *testing.T) {
	// threshold <= 0 should default to 0.6.
	content := "hello world\nfoo bar\nbaz qux\n"
	search := "TOTALLY DIFFERENT\nNOTHING MATCHES\nAT ALL HERE"

	result := FindSimilar(search, content, 0) // threshold=0 → default 0.6
	require.Empty(t, result, "with default threshold 0.6, unrelated search should return empty")
}

func TestFindSimilar_EmptySearch_ReturnsEmpty(t *testing.T) {
	content := "some content\nhere\n"
	result := FindSimilar("", content, 0.6)
	require.Empty(t, result)
}
