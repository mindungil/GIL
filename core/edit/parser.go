package edit

import (
	"fmt"
	"regexp"
	"strings"
)

// DSL marker constants (ported verbatim from Aider editblock_coder.py lines 386-388).
const (
	DefaultFence = "```"
	headPat      = `^<{5,9} SEARCH>?\s*$`
	dividerPat   = `^={5,9}\s*$`
	updatedPat   = `^>{5,9} REPLACE\s*$`
)

var (
	headRe    = regexp.MustCompile(headPat)
	dividerRe = regexp.MustCompile(dividerPat)
	updatedRe = regexp.MustCompile(updatedPat)
)

// Block is one parsed SEARCH/REPLACE edit block.
type Block struct {
	File    string // path to the file being edited (relative or absolute as written)
	Search  string // contents of the SEARCH section, including original line endings
	Replace string // contents of the REPLACE section
}

// ParseError describes a malformed block. Includes the line number where
// parsing failed plus a snippet of context.
type ParseError struct {
	Line    int
	Reason  string
	Context string // last few lines before the error
}

func (p *ParseError) Error() string {
	return fmt.Sprintf("edit parse error at line %d: %s\n--- context ---\n%s", p.Line, p.Reason, p.Context)
}

// Parse reads content and returns all SEARCH/REPLACE blocks. The first
// occurrence of a malformed block returns ParseError; previously-parsed
// blocks are still returned in the slice.
//
// File-name resolution mirrors Aider's find_filename:
//  1. Look at the 1-3 lines before the HEAD marker.
//  2. Skip blank lines and lines containing only a code-fence marker.
//  3. Strip backticks/asterisks/colons from the first non-skip line.
//  4. If still empty, fall back to the most recently seen filename
//     (so consecutive blocks targeting the same file work).
//  5. If no filename can be resolved AND no current filename is set,
//     return ParseError "missing filename".
func Parse(content string) ([]Block, error) {
	lines := splitKeepNewline(content)
	var blocks []Block
	var currentFilename string
	i := 0

	for i < len(lines) {
		trimmed := strings.TrimRight(lines[i], "\n\r")
		if !headRe.MatchString(trimmed) {
			i++
			continue
		}

		// Found a HEAD marker at line i (1-indexed: i+1).
		// Resolve filename from preceding 1-3 lines OR fall back to currentFilename.
		startCtx := i - 3
		if startCtx < 0 {
			startCtx = 0
		}
		filename := findFilename(lines[startCtx:i])
		if filename == "" {
			if currentFilename != "" {
				filename = currentFilename
			} else {
				return blocks, &ParseError{
					Line: i + 1,
					Reason: "missing filename for SEARCH block. Format:\n" +
						"  <filename>\n" +
						"  <<<<<<< SEARCH\n" +
						"  <old_lines>\n" +
						"  =======\n" +
						"  <new_lines>\n" +
						"  >>>>>>> REPLACE\n" +
						"The filename goes on its own line BEFORE the SEARCH marker (not as a 'path:' label inside the block).",
					Context: lastLines(lines, i+1, 5),
				}
			}
		}
		currentFilename = filename

		// Collect search lines until divider.
		var searchBuf strings.Builder
		i++
		for i < len(lines) && !dividerRe.MatchString(strings.TrimRight(lines[i], "\n\r")) {
			// If we encounter another HEAD or an UPDATED marker before a divider, error.
			t := strings.TrimRight(lines[i], "\n\r")
			if headRe.MatchString(t) || updatedRe.MatchString(t) {
				return blocks, &ParseError{
					Line:    i + 1,
					Reason:  "expected ======= divider before next marker",
					Context: lastLines(lines, i+1, 5),
				}
			}
			searchBuf.WriteString(lines[i])
			i++
		}
		if i >= len(lines) {
			return blocks, &ParseError{
				Line:   i,
				Reason: "expected ======= divider, reached end of input",
				Context: lastLines(lines, i, 5),
			}
		}
		// Skip the divider line.
		i++

		// Collect replace lines until UPDATED marker.
		var replaceBuf strings.Builder
		for i < len(lines) && !updatedRe.MatchString(strings.TrimRight(lines[i], "\n\r")) {
			// Be tolerant: if we hit a HEAD or DIVIDER before UPDATED, error.
			t := strings.TrimRight(lines[i], "\n\r")
			if headRe.MatchString(t) || dividerRe.MatchString(t) {
				return blocks, &ParseError{
					Line:    i + 1,
					Reason:  "expected >>>>>>> REPLACE before next marker",
					Context: lastLines(lines, i+1, 5),
				}
			}
			replaceBuf.WriteString(lines[i])
			i++
		}
		if i >= len(lines) {
			return blocks, &ParseError{
				Line:   i,
				Reason: "expected >>>>>>> REPLACE, reached end of input",
				Context: lastLines(lines, i, 5),
			}
		}
		// Skip the UPDATED marker.
		i++

		blocks = append(blocks, Block{
			File:    filename,
			Search:  searchBuf.String(),
			Replace: replaceBuf.String(),
		})
	}
	return blocks, nil
}

// findFilename inspects the up-to-3 lines before a HEAD marker and returns
// the most plausible filename. Ported from Aider's find_filename (lines 538-600)
// with simplification: we drop valid_fnames fuzzy matching and just return the
// first clean candidate found walking backwards.
//
// Walking order: closest line wins (reversed, take first non-fence, non-blank line).
// Skips blank lines, DSL marker lines, and pure code-fence lines (``` or ~~~).
// Strips wrapping backticks, asterisks, colons, leading #, and surrounding space.
// Returns "" if no candidate found.
func findFilename(preceding []string) string {
	// Walk in reverse order; the closest line to the HEAD marker wins.
	for j := len(preceding) - 1; j >= 0; j-- {
		line := strings.TrimRight(preceding[j], "\n\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			continue
		}

		// Skip DSL marker lines — they are never filenames.
		if headRe.MatchString(trimmed) || dividerRe.MatchString(trimmed) || updatedRe.MatchString(trimmed) {
			continue
		}

		// A pure code-fence line (``` or ~~~) with no extra content is a fence-only line.
		// If it has content after the fence (e.g., "```go"), it acts as a language tag —
		// skip it too since it's not a filename.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			// Check if it has a dot or slash after the fence — that would be a filename.
			afterFence := strings.TrimLeft(trimmed, "`~")
			afterFence = strings.TrimSpace(afterFence)
			if afterFence != "" && (strings.Contains(afterFence, ".") || strings.Contains(afterFence, "/")) {
				// Looks like a filename embedded after the fence token — extract it.
				candidate := strings.Trim(afterFence, "`*: ")
				candidate = strings.TrimLeft(candidate, "#")
				candidate = strings.TrimSpace(candidate)
				if candidate != "" {
					return candidate
				}
			}
			// It's a language tag or bare fence — keep scanning upward only if this
			// was a fence line (mirrors Aider's "only continue as long as we keep seeing fences").
			continue
		}

		// Non-fence, non-blank line — strip wrapping markers and return.
		// Mirrors Aider's strip_filename logic (lines 408-436).
		candidate := trimmed
		if candidate == "..." {
			continue
		}
		candidate = strings.TrimRight(candidate, ":")
		candidate = strings.TrimLeft(candidate, "#")
		candidate = strings.TrimSpace(candidate)
		candidate = strings.Trim(candidate, "`")
		candidate = strings.Trim(candidate, "*")
		candidate = strings.TrimSpace(candidate)

		// Aider's find_filename (without valid_fnames) requires a "." in the
		// filename to consider it a real path (see lines 593-599: "look for a
		// file w/extension"). We also accept "/" as a path separator hint.
		// This prevents replace-section content like "A" being treated as a
		// filename when blocks are consecutive with no intervening name line.
		if candidate != "" && (strings.Contains(candidate, ".") || strings.Contains(candidate, "/")) {
			return candidate
		}
	}
	return ""
}

// lastLines returns the last n lines ending at (1-indexed) endLine as a string.
func lastLines(lines []string, endLine, n int) string {
	start := endLine - n
	if start < 0 {
		start = 0
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return strings.Join(lines[start:endLine], "")
}
