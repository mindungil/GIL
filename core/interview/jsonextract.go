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

	// Find the first JSON-y opener.
	start := -1
	var open, close byte
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			start, open, close = i, '{', '}'
			break
		}
		if s[i] == '[' {
			start, open, close = i, '[', ']'
			break
		}
	}
	if start == -1 {
		// No JSON found — return as-is so json.Unmarshal can produce
		// the canonical "looking for beginning of value" diagnostic.
		return s
	}

	// Walk from start, balancing brackets while honoring strings so a
	// `"key": "value with } in it"` doesn't fool the counter. Returns
	// only the first balanced object/array — anything trailing (more
	// reasoning prose, a second JSON fragment, etc.) is dropped.
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
				return s[start : i+1]
			}
		}
	}
	// Unbalanced — return what we have so json.Unmarshal yields a
	// useful "unexpected end" diagnostic instead of a silent retry.
	return s[start:]
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

	if !hadTagBlock && !hadHeader {
		// No preamble shape recognised — preserve the original
		// (including any leading whitespace) so caller-visible
		// behaviour is identical to "no cleaning attempted".
		return original
	}
	return s
}
