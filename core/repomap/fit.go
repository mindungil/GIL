package repomap

import (
	"fmt"
	"strings"
)

// EstimateTokens returns approximate token count of s using a 4-chars-per-token
// heuristic. Exposed for callers that want to budget rendered chunks.
func EstimateTokens(s string) int {
	return len(s) / 4
}

// Render produces the markdown for a specific prefix length, useful for
// callers that need finer control. Pass len(ranked) for the full map.
//
// Format per file:
//
//	## file/path.go
//	- func Foo (line 12)
//	- struct Bar (line 30-45)
//
// Files are listed in the order their highest-ranked symbol first appears.
func Render(ranked []ScoredSymbol, prefix int) string {
	if prefix > len(ranked) {
		prefix = len(ranked)
	}
	if prefix <= 0 || len(ranked) == 0 {
		return ""
	}

	// Group by file, preserving the order of first appearance among the
	// top-prefix entries.
	type fileBlock struct {
		file  string
		lines []string
	}
	fileIdx := map[string]int{}
	var blocks []*fileBlock
	for i := 0; i < prefix; i++ {
		s := ranked[i].Symbol
		idx, ok := fileIdx[s.File]
		if !ok {
			idx = len(blocks)
			fileIdx[s.File] = idx
			blocks = append(blocks, &fileBlock{file: s.File})
		}
		var line string
		if s.EndLine > s.Line && s.EndLine != 0 {
			line = fmt.Sprintf("- %s %s (line %d-%d)", s.Kind, s.Name, s.Line, s.EndLine)
		} else {
			line = fmt.Sprintf("- %s %s (line %d)", s.Kind, s.Name, s.Line)
		}
		blocks[idx].lines = append(blocks[idx].lines, line)
	}

	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString("## ")
		sb.WriteString(b.file)
		sb.WriteString("\n")
		for _, l := range b.lines {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Fit returns the highest-fidelity markdown repomap that fits within
// maxTokens. Strategy: binary-search the prefix length K of the ranked
// slice, render the top-K symbols, and pick the largest K whose
// rendered output's estimated token count is <= maxTokens.
//
// EstimateTokens uses the same 4-chars-per-token heuristic the runner does.
//
// Returns "" when ranked is empty or maxTokens is too small for even one symbol.
func Fit(ranked []ScoredSymbol, maxTokens int) string {
	if len(ranked) == 0 || maxTokens <= 0 {
		return ""
	}
	// Quick check: does a single symbol fit?
	if EstimateTokens(Render(ranked, 1)) > maxTokens {
		return ""
	}
	// Binary search for the largest prefix that fits.
	lo, hi := 1, len(ranked)
	best := 1
	for lo <= hi {
		mid := (lo + hi) / 2
		out := Render(ranked, mid)
		if EstimateTokens(out) <= maxTokens {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return Render(ranked, best)
}
