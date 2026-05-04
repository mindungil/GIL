package interview

import (
	"regexp"
	"strings"
)

// thinkingBlockRE matches Anthropic / generic <thinking>…</thinking>
// AND DeepSeek-R1 / Qwen3 <think>…</think> pre-content, including
// newlines, non-greedy. The case-insensitive flag covers stray
// <THINKING> casings observed in some model outputs.
var thinkingBlockRE = regexp.MustCompile(`(?si)<think(?:ing)?>.*?</think(?:ing)?>`)

// qwenPreambleRE matches a leading "Thinking Process:" / "Thought:" /
// "Reasoning:" preamble that some Qwen / DeepSeek deployments emit
// before the answer. Anchored to start-of-string to avoid eating
// matching substrings that happen to appear in legitimate answers.
var qwenPreambleRE = regexp.MustCompile(`(?si)^\s*(?:thinking process|thought|reasoning|let me think)\s*[:\-—]\s*`)

// quotedQuestionRE matches a `"…?"` substring of at least 15 inner
// characters ending in `?`. Used as a high-precision "lift the last
// candidate question" rule when reasoning models propose several
// quoted alternatives in their prose. Non-greedy on the inner content
// so multiple candidates each match independently.
var quotedQuestionRE = regexp.MustCompile(`"([^"]{15,}?\?)"`)

// extractJSON returns the user-prompted JSON object (or array) from a
// model response that may include reasoning preambles. Handles the
// common "thinking" patterns that break strict JSON parsers:
//
//   - Anthropic extended-thinking: `<thinking>...</thinking>{"…"}` → strip
//     the block, keep what follows.
//   - Qwen / DeepSeek-R1 style: `Thinking Process:\n\n…\n\n{"…"}` → cut
//     to the first `{` (or `[` for arrays).
//   - Markdown-fenced JSON: ` ```json\n{…}\n``` ` → strip code fence.
//
// Returns the cleaned string, or the input unchanged when no preamble
// is detected. Never errors — leaves the stricter json.Unmarshal to
// reject malformed payloads with its own diagnostic.
//
// This is a defensive read-side cleaner; the right long-term fix is
// provider adapters that surface reasoning as a separate event kind
// (see followup #4 in docs/plans/phase-26.5-followups.md). Until that
// lands, this helper unblocks every model that wraps its answer in
// a reasoning preamble.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Drop any <thinking>…</thinking> blocks first.
	s = thinkingBlockRE.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	// Strip markdown fence (```json … ``` or ``` … ```).
	s = stripCodeFence(s)

	// Find every balanced top-level {...} or [...] in the string and
	// return the LARGEST. Earlier versions returned the first opener,
	// but reasoning models commonly emit short array literals like
	//   `Value: ["foo", "bar"]`
	// in their prose BEFORE delivering the real JSON answer. The real
	// answer is invariably the largest balanced structure; preferring
	// it picks the right candidate while still doing the right thing
	// when the response contains exactly one JSON value.
	candidates := findBalancedStructures(s)
	if len(candidates) == 0 {
		// No balanced structure found. If the string contains an
		// opening bracket but no matching close, fall back to
		// returning the tail from the first opener so json.Unmarshal
		// produces a useful "unexpected end" diagnostic.
		for i := 0; i < len(s); i++ {
			if s[i] == '{' || s[i] == '[' {
				return s[i:]
			}
		}
		return s
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.end-c.start > best.end-best.start {
			best = c
		}
	}
	return s[best.start:best.end]
}

// jsonRange marks one balanced JSON structure inside the source: half-
// open [start, end) over the cleaned string.
type jsonRange struct{ start, end int }

// findBalancedStructures scans s and returns the byte ranges of every
// top-level balanced {...} or [...] block, honoring string literals so
// braces inside JSON strings don't fool the counter.
//
// Top-level here means the bracket that began the structure was at
// depth 0; nested structures are not reported separately. Unbalanced
// openers (the model truncated mid-emit) are silently skipped — caller
// handles the empty-result fallback.
func findBalancedStructures(s string) []jsonRange {
	var out []jsonRange
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '{' && c != '[' {
			i++
			continue
		}
		open := c
		var close byte
		if open == '{' {
			close = '}'
		} else {
			close = ']'
		}
		end, ok := scanBalanced(s, i, open, close)
		if !ok {
			// Unbalanced from this position — skip past this opener
			// and keep searching (later positions may still balance).
			i++
			continue
		}
		out = append(out, jsonRange{start: i, end: end})
		i = end
	}
	return out
}

// scanBalanced walks from start (which must point at an `open` byte)
// and returns the index just past the matching close, or (0, false) if
// no balanced match exists in s. Honors JSON string literals (`"..."`
// with backslash escapes) so brackets inside strings don't move the
// depth counter.
func scanBalanced(s string, start int, open, close byte) (int, bool) {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if inStr {
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// stripCodeFence removes a single markdown code fence wrapping the
// whole string (e.g. "```json\n{...}\n```"). When the input doesn't
// start with a fence, returns it unchanged.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence + optional language tag + newline.
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop the trailing fence.
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// extractAnswerText cleans a free-text model response for direct
// display to the user. Removes the same reasoning-preamble shapes
// extractJSON handles, but for prose answers (the interviewer's next
// question, the audit's reason, etc.) where there is no JSON anchor
// to balance against.
//
// Strategy:
//   - Strip <thinking>…</thinking> / <think>…</think> blocks.
//   - Strip a leading "Thinking Process:" / "Thought:" / "Reasoning:"
//     header if present (case-insensitive).
//   - When a stripped <think> tag was followed by text, that text is
//     the answer. When a "Thinking Process:" preamble was followed by
//     a blank-line gap and then another paragraph, keep only the last
//     non-empty paragraph — that's empirically where Qwen-thinking and
//     DeepSeek-R1 deployments place the actual answer.
//   - Trim surrounding whitespace.
//
// Returns the input unchanged when no preamble is detected. This is a
// defensive read-side cleaner mirroring extractJSON; the proper fix is
// provider adapters surfacing reasoning as a typed event (followups #4
// in docs/plans/phase-26.5-followups.md).
func extractAnswerText(s string) string {
	original := s
	s = strings.TrimSpace(s)

	// Strip <think> / <thinking> blocks.
	stripped := thinkingBlockRE.ReplaceAllString(s, "")
	hadTagBlock := stripped != s
	s = strings.TrimSpace(stripped)

	// Strip leading "Thinking Process:" header if present.
	hadHeader := false
	if loc := qwenPreambleRE.FindStringIndex(s); loc != nil {
		s = strings.TrimSpace(s[loc[1]:])
		hadHeader = true
	}

	// If a header was stripped, drop everything before the last
	// blank-line gap — Qwen-style preambles typically reason for one
	// or more paragraphs, then deliver the final answer after a blank
	// line. With no blank line, keep what remains (the model didn't
	// separate thinking from answer; safer to show the whole thing
	// than to guess where the answer starts and silently drop it).
	if hadHeader {
		if idx := strings.LastIndex(s, "\n\n"); idx >= 0 {
			s = strings.TrimSpace(s[idx+2:])
		}
	}

	// High-precision rescue (1): models often propose several quoted
	// candidate questions in their prose while self-critiquing; the
	// final choice is the LAST `"…?"` substring. Tried first because
	// it survives candidates that are embedded inline ("But maybe
	// simpler: "actual question?"") rather than on their own line.
	if q := lastQuotedQuestion(s); q != "" {
		return q
	}
	// High-precision rescue (2): when no quoted-question pattern fires
	// but the response IS multi-line and the last trimmed line is a
	// fully-quoted string, lift its content. Catches the simpler
	// "reasoning prose, then quoted final answer" shape.
	if quoted := tailQuotedLine(s); quoted != "" {
		return quoted
	}

	if !hadTagBlock && !hadHeader {
		// No preamble shape recognised — preserve the original
		// (including any leading whitespace) so caller-visible
		// behaviour is identical to "no cleaning attempted".
		return original
	}
	return s
}

// lastQuotedQuestion scans s for `"…?"` substrings and returns the
// inner text of the last one. Returns "" when no candidate matches OR
// when the matched substring is essentially the entire response (the
// model passed through a single quoted question — that's intentional
// formatting, not a reasoning leak).
//
// "?" requirement plus a 15-char minimum (enforced by quotedQuestionRE)
// keeps this from latching onto short interjections like `"tiny"` that
// reasoning models scatter through their prose.
func lastQuotedQuestion(s string) string {
	matches := quotedQuestionRE.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	// Reject when the whole-match (with surrounding quotes) IS the
	// entire trimmed response — pass-through is the right behavior.
	if strings.TrimSpace(s) == last[0] {
		return ""
	}
	return last[1]
}

// tailQuotedLine returns the inner content of the LAST fully-quoted
// line (`"..."` after trimming) when the response also contains at
// least one earlier non-empty line. Returns "" otherwise.
//
// Reasoning models routinely emit several candidate questions wrapped
// in quotes while critiquing themselves before settling on the final
// one — empirically the last fully-quoted line is the chosen answer.
// Walking from the bottom skips trailing prose ("This is good." etc.)
// and tolerates a truncated final line (no closing `"` from MaxTokens
// hitting mid-emit) by falling back to the prior complete one.
//
// Single-line fully-quoted responses pass through unchanged — those
// are most likely intentional formatting, not the reasoning pattern.
func tailQuotedLine(s string) string {
	lines := strings.Split(s, "\n")
	// Find the last non-blank line index for "is multi-line?" check.
	lastNonBlank := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastNonBlank = i
			break
		}
	}
	if lastNonBlank <= 0 {
		// Single-line or all-empty — nothing to lift safely.
		return ""
	}

	// Walk from the bottom, return the inner of the first complete
	// `"…"` line found. A "complete" quoted line has matching quotes
	// at both ends after trim, length ≥ 2, and the inner string is
	// not empty (avoids `""` false-positive).
	for i := lastNonBlank; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if len(t) < 3 || t[0] != '"' || t[len(t)-1] != '"' {
			continue
		}
		inner := t[1 : len(t)-1]
		if strings.TrimSpace(inner) == "" {
			continue
		}
		// Require at least one earlier non-empty line so a single
		// quoted-line response still passes through.
		hasEarlier := false
		for j := 0; j < i; j++ {
			if strings.TrimSpace(lines[j]) != "" {
				hasEarlier = true
				break
			}
		}
		if !hasEarlier {
			return ""
		}
		return inner
	}
	return ""
}
