package app

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// chat_markdown.go renders agent transcript text through glamour so
// fenced code blocks pick up syntax highlighting and inline markup
// (bold, italic, inline `code`) reads styled instead of as raw `**`
// asterisks. The renderer is initialised once and shared — glamour's
// internal compilation of the chroma styles is heavy enough to want
// caching.
//
// Scope decisions:
//   - Only AGENT lines (transcript entries that start with "‹") get
//     rendered. User echoes (`›`), system events (`   ‹  `), and
//     errors (`!`) stay literal.
//   - Rendering happens at View() time, not at append time. The raw
//     text is preserved in the transcript so coalescing logic
//     (which folds streaming chunks onto one line) keeps working.
//   - `--ascii` and NO_COLOR users get the raw text verbatim — glamour
//     emits ANSI even with chroma disabled, and the dim-style chroma
//     palette is a poor fit for ascii-only terminals.

// renderAgentMarkdown renders body as markdown when it contains
// markdown-worthy markers; falls back to body verbatim otherwise.
// The detection is deliberately conservative: glamour adds layout
// (paragraph spacing, list bullets, code-block padding) so we only
// pay that cost when the source actually has structure.
//
// The renderer is gated on color + non-ascii — both modes run
// without glamour because the styled output would either look wrong
// (ASCII-mode chroma) or no-op the styling (NO_COLOR strips ANSI).
func renderAgentMarkdown(body string) string {
	if IsNoColor() || IsAsciiMode() {
		return body
	}
	if !looksLikeMarkdown(body) {
		return body
	}
	r := agentMarkdownRenderer()
	if r == nil {
		return body
	}
	out, err := r.Render(body)
	if err != nil {
		return body
	}
	// glamour wraps every render with surrounding blank lines and a
	// trailing newline. Strip those so the rendered block flows
	// directly into the transcript without disrupting the rail
	// rhythm. Internal blank lines (e.g. between paragraphs and code
	// blocks) are preserved.
	out = strings.TrimRight(out, "\n")
	out = strings.TrimLeft(out, "\n")
	return out
}

// looksLikeMarkdown returns true when body contains at least one
// markdown construct that benefits from glamour's styling. We skip
// glamour for plain prose chunks because the layout post-processing
// (added paragraph breaks, indentation) costs more than the styling
// gains.
func looksLikeMarkdown(body string) bool {
	if strings.Contains(body, "```") {
		return true // fenced code block
	}
	if strings.Contains(body, "`") {
		return true // inline code
	}
	if strings.Contains(body, "**") || strings.Contains(body, "__") {
		return true // bold
	}
	// Headings — at start of body or after a newline.
	for _, prefix := range []string{"# ", "## ", "### "} {
		if strings.HasPrefix(body, prefix) || strings.Contains(body, "\n"+prefix) {
			return true
		}
	}
	// Lists.
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(body, prefix) || strings.Contains(body, "\n"+prefix) {
			return true
		}
	}
	return false
}

var (
	agentMarkdownOnce sync.Once
	agentMarkdownR    *glamour.TermRenderer
)

// agentMarkdownRenderer constructs the shared TermRenderer once and
// returns it. WithStandardStyle("dark") matches gil's terminal-
// aesthetic.md surface palette (dark background, mint-cyan / amber
// accents); the dim heading colour and slightly brighter inline-code
// background read coherently with the chat surface chrome.
//
// WordWrap is set to 0 (disabled) so glamour doesn't introduce its
// own wrapping — the transcript renderer handles wrapping by way of
// the conversation viewport's clip behaviour.
func agentMarkdownRenderer() *glamour.TermRenderer {
	agentMarkdownOnce.Do(func() {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(0),
		)
		if err != nil {
			return
		}
		agentMarkdownR = r
	})
	return agentMarkdownR
}
