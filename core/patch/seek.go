package patch

import "strings"

// seekSequence finds the starting index of `pattern` lines within `lines`
// at or after `start`. Three-tier strictness: exact → rstrip → trim both.
// When eof is true, searches from the end of the file backwards (matching
// Codex's behavior for chunks marked is_end_of_file).
//
// Returns -1 when not found. Empty pattern returns start (no-op match).
//
// Lifted from codex-rs/apply-patch/src/seek_sequence.rs.
func seekSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}

	// When eof, prefer matching at the very end of the file.
	searchStart := start
	if eof && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}

	last := len(lines) - len(pattern)

	// Tier 1: exact match.
	for i := searchStart; i <= last; i++ {
		ok := true
		for j := 0; j < len(pattern); j++ {
			if lines[i+j] != pattern[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}

	// Tier 2: rstrip equal (ignore trailing whitespace).
	for i := searchStart; i <= last; i++ {
		ok := true
		for j := 0; j < len(pattern); j++ {
			if strings.TrimRight(lines[i+j], " \t") != strings.TrimRight(pattern[j], " \t") {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}

	// Tier 3: trim both sides.
	for i := searchStart; i <= last; i++ {
		ok := true
		for j := 0; j < len(pattern); j++ {
			if strings.TrimSpace(lines[i+j]) != strings.TrimSpace(pattern[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}

	return -1
}
