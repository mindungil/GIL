// Package edit implements precision file editing via SEARCH/REPLACE blocks.
//
// The matching algorithm is ported from Aider's editblock_coder.py
// (replace_most_similar_chunk and friends). Aider's fuzzy tier is dead code
// in their tree (early return); we re-enable it as Tier 4.
package edit

import (
	"errors"
	"strings"
)

// Tier identifies which matching strategy succeeded.
type Tier int

const (
	TierExact     Tier = iota + 1 // 1 — exact slice match
	TierLeadingWS                 // 2 — uniform leading-whitespace mismatch tolerated
	TierTrailingWS                // 3 — trailing-whitespace tolerated per line
	TierFuzzy                     // 4 — SequenceMatcher.ratio >= FuzzyThreshold
)

// String returns a human-readable name for the tier.
func (t Tier) String() string {
	switch t {
	case TierExact:
		return "exact"
	case TierLeadingWS:
		return "leading_ws"
	case TierTrailingWS:
		return "trailing_ws"
	case TierFuzzy:
		return "fuzzy"
	default:
		return "unknown"
	}
}

// Match describes the location of a matched chunk inside the whole text.
type Match struct {
	Tier      Tier
	StartLine int     // 1-indexed; line in `whole` where the matched chunk starts
	EndLine   int     // 1-indexed; line where it ends (inclusive)
	Score     float64 // 1.0 for tiers 1-3; ratio for tier 4
}

// ErrNoMatch is returned when no tier finds the part inside whole.
var ErrNoMatch = errors.New("edit: no matching chunk found")

// MatchEngine holds configuration for the matching algorithm.
type MatchEngine struct {
	// FuzzyThreshold is the minimum sequence ratio for tier 4. Defaults to 0.8
	// when <= 0.
	FuzzyThreshold float64
}

// Find locates the best chunk in `whole` that matches `part`. Returns
// ErrNoMatch when no tier matches. Tiers are tried in order (Exact first);
// the first match wins.
func (me *MatchEngine) Find(whole, part string) (Match, error) {
	threshold := me.FuzzyThreshold
	if threshold <= 0 {
		threshold = 0.8
	}

	_, wholeLines := prep(whole)
	_, partLines := prep(part)

	if mt, ok := tier1Exact(wholeLines, partLines); ok {
		return mt, nil
	}
	if mt, ok := tier2LeadingWS(wholeLines, partLines); ok {
		return mt, nil
	}
	if mt, ok := tier3TrailingWS(wholeLines, partLines); ok {
		return mt, nil
	}
	if mt, ok := tier4Fuzzy(wholeLines, partLines, threshold); ok {
		return mt, nil
	}
	return Match{}, ErrNoMatch
}

// Replace finds the chunk per Find and returns whole with the chunk replaced
// by `replace`. For TierLeadingWS, the replacement gets the same uniform
// indent the matched chunk had. Returns ErrNoMatch on miss.
func (me *MatchEngine) Replace(whole, part, replace string) (string, Match, error) {
	threshold := me.FuzzyThreshold
	if threshold <= 0 {
		threshold = 0.8
	}

	whole, wholeLines := prep(whole)
	_, partLines := prep(part)
	_, replaceLines := prep(replace)

	// Tier 1 — exact
	if mt, ok := tier1Exact(wholeLines, partLines); ok {
		result := applyReplacement(wholeLines, mt.StartLine-1, mt.EndLine, replaceLines)
		return result, mt, nil
	}

	// Tier 2 — leading-whitespace flexible (also rebuilds indent)
	if result, mt, ok := tier2LeadingWSReplace(wholeLines, partLines, replaceLines); ok {
		return result, mt, nil
	}

	// Tier 3 — trailing-whitespace flexible
	if mt, ok := tier3TrailingWS(wholeLines, partLines); ok {
		result := applyReplacement(wholeLines, mt.StartLine-1, mt.EndLine, replaceLines)
		return result, mt, nil
	}

	// Tier 4 — fuzzy
	if mt, ok := tier4Fuzzy(wholeLines, partLines, threshold); ok {
		result := applyReplacement(wholeLines, mt.StartLine-1, mt.EndLine, replaceLines)
		return result, mt, nil
	}

	return whole, Match{}, ErrNoMatch
}

// ---------------------------------------------------------------------------
// prep — mirrors Aider's prep(): ensures trailing newline; splits keeping
// per-line trailing newlines (so re-joining is lossless).
// ---------------------------------------------------------------------------

func prep(s string) (string, []string) {
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	lines := splitKeepNewline(s)
	return s, lines
}

// splitKeepNewline splits s into lines, keeping the '\n' at the end of each
// line (mirrors Python's str.splitlines(keepends=True)).
func splitKeepNewline(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:idx+1])
		s = s[idx+1:]
	}
	return lines
}

// ---------------------------------------------------------------------------
// Tier 1 — exact slice match (port of perfect_replace)
// ---------------------------------------------------------------------------

func tier1Exact(wholeLines, partLines []string) (Match, bool) {
	n := len(partLines)
	for i := 0; i <= len(wholeLines)-n; i++ {
		if sliceEq(wholeLines[i:i+n], partLines) {
			return Match{
				Tier:      TierExact,
				StartLine: i + 1,
				EndLine:   i + n,
				Score:     1.0,
			}, true
		}
	}
	return Match{}, false
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Tier 2 — leading-whitespace flexible
// Port of replace_part_with_missing_leading_whitespace +
// match_but_for_leading_whitespace.
// ---------------------------------------------------------------------------

// tier2LeadingWS is the Find-only variant (doesn't need replaceLines).
func tier2LeadingWS(wholeLines, partLines []string) (Match, bool) {
	// Compute common leading whitespace across all non-blank lines in partLines.
	strippedPart := stripCommonLeading(partLines)
	n := len(strippedPart)

	for i := 0; i <= len(wholeLines)-n; i++ {
		add := matchButForLeadingWS(wholeLines[i:i+n], strippedPart)
		if add == nil {
			continue
		}
		return Match{
			Tier:      TierLeadingWS,
			StartLine: i + 1,
			EndLine:   i + n,
			Score:     1.0,
		}, true
	}
	return Match{}, false
}

// tier2LeadingWSReplace is the Replace variant that also rebuilds the indent.
func tier2LeadingWSReplace(wholeLines, partLines, replaceLines []string) (string, Match, bool) {
	// Outdent part and replace by the max common leading across both.
	combined := append(partLines, replaceLines...)
	numLeading := commonLeadingCount(combined)

	strippedPart := partLines
	strippedReplace := replaceLines
	if numLeading > 0 {
		strippedPart = stripLeadingN(partLines, numLeading)
		strippedReplace = stripLeadingN(replaceLines, numLeading)
	}

	n := len(strippedPart)
	for i := 0; i <= len(wholeLines)-n; i++ {
		add := matchButForLeadingWS(wholeLines[i:i+n], strippedPart)
		if add == nil {
			continue
		}
		// Rebuild replace lines with the chunk's leading whitespace.
		indentedReplace := make([]string, len(strippedReplace))
		for j, rline := range strippedReplace {
			if strings.TrimRight(rline, " \t\r\n") != "" {
				indentedReplace[j] = *add + rline
			} else {
				indentedReplace[j] = rline
			}
		}
		mt := Match{Tier: TierLeadingWS, StartLine: i + 1, EndLine: i + n, Score: 1.0}
		result := applyReplacement(wholeLines, i, i+n, indentedReplace)
		return result, mt, true
	}
	return "", Match{}, false
}

// stripCommonLeading strips the common leading whitespace from all non-blank
// lines (used when we only have partLines, no replaceLines).
func stripCommonLeading(lines []string) []string {
	n := commonLeadingCount(lines)
	if n == 0 {
		return lines
	}
	return stripLeadingN(lines, n)
}

// commonLeadingCount returns the minimum leading whitespace count across all
// non-blank lines. Returns 0 if no non-blank lines found.
func commonLeadingCount(lines []string) int {
	min := -1
	for _, l := range lines {
		stripped := strings.TrimLeft(l, " \t")
		if strings.TrimRight(stripped, " \t\r\n") == "" {
			// blank line — skip
			continue
		}
		n := len(l) - len(stripped)
		if min < 0 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// stripLeadingN removes the first n bytes from each non-blank line.
func stripLeadingN(lines []string, n int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r\n") != "" {
			if len(l) >= n {
				out[i] = l[n:]
			} else {
				out[i] = l
			}
		} else {
			out[i] = l
		}
	}
	return out
}

// matchButForLeadingWS checks if chunkLines matches partLines after stripping
// leading whitespace from each line. Returns a pointer to the uniform added
// leading string if all lines have the same leading addition. Returns nil on
// mismatch.
//
// Port of Aider's match_but_for_leading_whitespace.
func matchButForLeadingWS(chunkLines, partLines []string) *string {
	num := len(chunkLines)
	if num != len(partLines) {
		return nil
	}
	// Non-whitespace content must agree (lstripped).
	for i := 0; i < num; i++ {
		if strings.TrimLeft(chunkLines[i], " \t") != strings.TrimLeft(partLines[i], " \t") {
			return nil
		}
	}
	// All leading-whitespace additions for non-blank lines must be identical.
	var add *string
	for i := 0; i < num; i++ {
		if strings.TrimRight(chunkLines[i], " \t\r\n") == "" {
			continue // blank line — skip
		}
		wLen := len(chunkLines[i]) - len(strings.TrimLeft(chunkLines[i], " \t"))
		pLen := len(partLines[i]) - len(strings.TrimLeft(partLines[i], " \t"))
		// The addition = leading of whole_line minus leading of part_line.
		// Aider's formula: whole_lines[i][: len(whole_lines[i]) - len(part_lines[i])]
		// which strips using length difference (assumes same content post-leading).
		diff := len(chunkLines[i]) - len(partLines[i])
		if diff < 0 {
			return nil
		}
		addStr := chunkLines[i][:wLen-pLen]
		if add == nil {
			add = &addStr
		} else if *add != addStr {
			return nil
		}
	}
	if add == nil {
		empty := ""
		add = &empty
	}
	return add
}

// ---------------------------------------------------------------------------
// Tier 3 — trailing-whitespace flexible
// ---------------------------------------------------------------------------

func tier3TrailingWS(wholeLines, partLines []string) (Match, bool) {
	n := len(partLines)
	rtrimPart := rtrimLines(partLines)

	for i := 0; i <= len(wholeLines)-n; i++ {
		rtrimChunk := rtrimLines(wholeLines[i : i+n])
		if sliceEq(rtrimChunk, rtrimPart) {
			return Match{
				Tier:      TierTrailingWS,
				StartLine: i + 1,
				EndLine:   i + n,
				Score:     1.0,
			}, true
		}
	}
	return Match{}, false
}

func rtrimLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		// Right-strip trailing spaces/tabs but keep the newline.
		nl := ""
		if strings.HasSuffix(l, "\n") {
			nl = "\n"
			l = l[:len(l)-1]
		}
		out[i] = strings.TrimRight(l, " \t") + nl
	}
	return out
}

// ---------------------------------------------------------------------------
// Tier 4 — fuzzy via sequence ratio (port of replace_closest_edit_distance)
// ---------------------------------------------------------------------------

func tier4Fuzzy(wholeLines, partLines []string, threshold float64) (Match, bool) {
	partStr := strings.Join(partLines, "")
	partLen := len(partLines)

	scale := 0.1
	minLen := int(float64(partLen) * (1 - scale))
	if minLen < 1 {
		minLen = 1
	}
	maxLen := int(float64(partLen)*(1+scale)) + 1

	bestScore := 0.0
	bestStart := -1
	bestEnd := -1

	for length := minLen; length <= maxLen; length++ {
		for i := 0; i <= len(wholeLines)-length; i++ {
			chunk := strings.Join(wholeLines[i:i+length], "")
			ratio := sequenceRatio(chunk, partStr)
			if ratio > bestScore {
				bestScore = ratio
				bestStart = i
				bestEnd = i + length
			}
		}
	}

	if bestScore < threshold || bestStart < 0 {
		return Match{}, false
	}

	return Match{
		Tier:      TierFuzzy,
		StartLine: bestStart + 1,
		EndLine:   bestEnd,
		Score:     bestScore,
	}, true
}

// ---------------------------------------------------------------------------
// sequenceRatio — LCS-based analogue of Python's SequenceMatcher.ratio().
//
// Python's SequenceMatcher uses the Ratcliff/Obershelp algorithm (find the
// longest contiguous matching block, then recurse on unmatched sides). Our
// implementation uses an LCS (Longest Common Subsequence) DP instead: it is
// monotonically related to the true ratio and accurate enough for threshold
// comparisons at the ~1–50 line chunk sizes we operate on.
//
// Formula: 2 * matchedLen / (len(a) + len(b)), same as SequenceMatcher.ratio.
// ---------------------------------------------------------------------------

func sequenceRatio(a, b string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	lcs := longestCommonSubsequenceLen(a, b)
	return 2.0 * float64(lcs) / float64(len(a)+len(b))
}

// longestCommonSubsequenceLen computes len(LCS(a, b)) using O(len(a)*len(b))
// DP with a two-row space optimisation.
func longestCommonSubsequenceLen(a, b string) int {
	// Keep the shorter string as the column dimension to minimise allocation.
	if len(a) > len(b) {
		a, b = b, a
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for j := 1; j <= len(b); j++ {
		for i := 1; i <= len(a); i++ {
			if a[i-1] == b[j-1] {
				cur[i] = prev[i-1] + 1
			} else if prev[i] > cur[i-1] {
				cur[i] = prev[i]
			} else {
				cur[i] = cur[i-1]
			}
		}
		prev, cur = cur, prev
		for i := range cur {
			cur[i] = 0
		}
	}
	return prev[len(a)]
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// applyReplacement builds the result string: wholeLines[:start] + replaceLines
// + wholeLines[end:]. start and end are 0-indexed, end is exclusive.
func applyReplacement(wholeLines []string, start, end int, replaceLines []string) string {
	result := make([]string, 0, len(wholeLines)-end+start+len(replaceLines))
	result = append(result, wholeLines[:start]...)
	result = append(result, replaceLines...)
	result = append(result, wholeLines[end:]...)
	return strings.Join(result, "")
}
