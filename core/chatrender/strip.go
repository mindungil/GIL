// Package chatrender provides shared text-shaping helpers used by chat
// surfaces (CLI stdout, TUI). The render pipeline elsewhere is
// surface-specific; the helpers here are surface-neutral. P68d.
package chatrender

import (
	"regexp"
	"strings"
)

// StripChatMarkdown removes the chat-app-shape markdown the agent
// system prompt (P68a) instructs the model to omit. This is the
// belt-suspenders second layer: even when high-temperature variance
// leaks `**bold**` / numbered lists / `inline-code` into the agent
// reply, the renderer still presents plain text.
//
// What is stripped:
//   - `**X**` / `__X__` → X (bold/strong emphasis)
//   - `*X*` / `_X_` → X (italic emphasis), but only when both delimiters
//     are present and not adjacent to alphanumerics (so file paths like
//     `foo_bar.go` keep their underscores).
//   - `` `X` `` → X (inline code in prose; outside fenced blocks).
//   - Leading `#`, `##`, `###` headers → header text only.
//   - Leading bullet markers `- `, `* `, `+ ` → bullet text only.
//   - Leading numbered-list markers `1. `, `2. ` etc. → list text only.
//
// What is preserved:
//   - Fenced code blocks (```...``` or ~~~...~~~). Content inside a
//     fence is passed through verbatim — the model may legitimately
//     quote code it just read or wrote.
//   - Blockquotes (`>`), tables, horizontal rules — these are rare in
//     agent prose and leaving them alone avoids surprising the user.
//
// The function is line-oriented so streaming chunks that contain
// partial lines render cleanly enough; only completed lines apply
// list/header detection.
func StripChatMarkdown(s string) string {
	var out strings.Builder
	out.Grow(len(s))

	inFence := false
	lines := strings.SplitAfter(s, "\n") // keep newlines attached
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\n")
		trailing := raw[len(line):] // "\n" or ""

		trimmed := strings.TrimLeft(line, " \t")
		leadingWS := line[:len(line)-len(trimmed)]

		// Fence toggle — written as-is.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out.WriteString(line)
			out.WriteString(trailing)
			continue
		}
		if inFence {
			out.WriteString(line)
			out.WriteString(trailing)
			continue
		}

		// Header strip: leading `#` runs followed by space.
		if i := countHeaderHashes(trimmed); i > 0 {
			trimmed = strings.TrimLeft(trimmed[i:], " \t")
		}

		// Bullet strip.
		if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && trimmed[1] == ' ' {
			trimmed = trimmed[2:]
		} else if m := numberedListPrefix.FindStringIndex(trimmed); m != nil && m[0] == 0 {
			trimmed = trimmed[m[1]:]
		}

		// Inline emphasis + inline code strip on the remaining content.
		stripped := stripInline(trimmed)

		out.WriteString(leadingWS)
		out.WriteString(stripped)
		out.WriteString(trailing)
	}

	return out.String()
}

var numberedListPrefix = regexp.MustCompile(`^\d+\.[ \t]+`)

func countHeaderHashes(s string) int {
	i := 0
	for i < len(s) && s[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return 0
	}
	if i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		return i + 1 // include the space so caller skips it
	}
	return 0
}

// stripInline removes `**X**`, `__X__`, single-backtick `X`, and
// (cautiously) `*X*` / `_X_` italics. Backtick handling is
// non-greedy paired; emphasis is paired-only (orphan `*`/`_` stays).
// Italic regexes require non-alphanumeric boundaries so file paths
// like foo_bar or globs *.go survive untouched.
func stripInline(s string) string {
	s = strongBoldRe.ReplaceAllString(s, "$1")
	s = strongBoldUnderscoreRe.ReplaceAllString(s, "$1")
	s = inlineCodeRe.ReplaceAllString(s, "$1")
	s = italicStarRe.ReplaceAllStringFunc(s, italicReplace)
	s = italicUnderscoreRe.ReplaceAllStringFunc(s, italicReplace)
	return s
}

// (?s) so . matches across newlines, in case ** wraps a multi-line span.
// Non-greedy inner to avoid swallowing adjacent runs.
var (
	strongBoldRe           = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)
	strongBoldUnderscoreRe = regexp.MustCompile(`(?s)__(.+?)__`)
	inlineCodeRe           = regexp.MustCompile("`([^`\n]+)`")
	// Italic with `*`: require non-`*` on both sides; inner content has
	// no `*` to avoid eating into adjacent bold.
	italicStarRe = regexp.MustCompile(`(^|[^\*])\*([^*\n]+)\*([^\*]|$)`)
	// Italic with `_`: require non-alphanumeric boundary so file paths
	// like `foo_bar.go` keep their underscores.
	italicUnderscoreRe = regexp.MustCompile(`(^|[^\w])_([^_\n]+)_([^\w]|$)`)
)

func italicReplace(match string) string {
	// Find the inner content by reapplying the regex on the original.
	// Caller uses ReplaceAllStringFunc so we need a closure that
	// reconstructs left/inner/right groups by name.
	if m := italicStarRe.FindStringSubmatch(match); len(m) == 4 {
		return m[1] + m[2] + m[3]
	}
	if m := italicUnderscoreRe.FindStringSubmatch(match); len(m) == 4 {
		return m[1] + m[2] + m[3]
	}
	return match
}
