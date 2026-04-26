package edit

import "strings"

// FindSimilar returns up to len(searchLines)+10 lines of content centered
// on the best-matching chunk, when the chunk's ratio against searchLines
// is >= threshold (0.6 default in Aider). Returns "" when no chunk meets
// the threshold or when content is shorter than search.
//
// Lifted from aider/coders/editblock_coder.py find_similar_lines (line 602).
// Used by the edit tool to give the agent a "did you mean this?" hint
// when a SEARCH block misses.
func FindSimilar(search, content string, threshold float64) string {
	if threshold <= 0 {
		threshold = 0.6
	}
	searchLines := strings.Split(strings.TrimRight(search, "\n"), "\n")
	contentLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(contentLines) < len(searchLines) || len(searchLines) == 0 {
		return ""
	}

	bestRatio := 0.0
	bestStart := -1
	bestEnd := -1

	for i := 0; i+len(searchLines) <= len(contentLines); i++ {
		chunk := contentLines[i : i+len(searchLines)]
		ratio := linesRatio(searchLines, chunk)
		if ratio > bestRatio {
			bestRatio = ratio
			bestStart = i
			bestEnd = i + len(searchLines)
		}
	}
	if bestRatio < threshold || bestStart < 0 {
		return ""
	}

	// Aider returns just the matched chunk if its first+last lines are exact;
	// otherwise widens by N=5 lines on each side.
	matched := contentLines[bestStart:bestEnd]
	if matched[0] == searchLines[0] && matched[len(matched)-1] == searchLines[len(searchLines)-1] {
		return strings.Join(matched, "\n")
	}
	const N = 5
	start := bestStart - N
	if start < 0 {
		start = 0
	}
	end := bestEnd + N
	if end > len(contentLines) {
		end = len(contentLines)
	}
	return strings.Join(contentLines[start:end], "\n")
}

// linesRatio computes a sequence-matcher-like ratio over two []string.
// Reuses the byte-level sequenceRatio from match.go by joining lines with
// newlines (good enough for thresholding decisions).
func linesRatio(a, b []string) float64 {
	return sequenceRatio(strings.Join(a, "\n"), strings.Join(b, "\n"))
}
