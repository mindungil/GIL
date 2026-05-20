package chatrender

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripChatMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain prose unchanged", "Just plain text.", "Just plain text."},
		{"strip bold star", "this is **bold** text", "this is bold text"},
		{"strip bold underscore", "this is __bold__ text", "this is bold text"},
		{"strip multi bold", "**foo** and **bar**", "foo and bar"},
		{"strip inline code in prose", "call `freeze_spec` and `start_run`", "call freeze_spec and start_run"},
		{"keep code fences", "```go\npackage main\n```", "```go\npackage main\n```"},
		{"keep tilde fences", "~~~\nx = 1\n~~~", "~~~\nx = 1\n~~~"},
		{"backticks inside fence preserved",
			"```\n`literal` inside\n```",
			"```\n`literal` inside\n```"},
		{"strip header h1", "# Title here\nbody", "Title here\nbody"},
		{"strip header h2", "## Section\nbody", "Section\nbody"},
		{"strip header h3 with leading ws", "   ### Subsection\nbody", "   Subsection\nbody"},
		{"too many hashes not stripped", "####### not a header", "####### not a header"},
		{"strip bullet dash", "- item one\n- item two", "item one\nitem two"},
		{"strip bullet star", "* item one\n* item two", "item one\nitem two"},
		{"strip bullet plus", "+ item one", "item one"},
		{"strip numbered list",
			"1. first step\n2. second step\n10. tenth step",
			"first step\nsecond step\ntenth step"},
		{"preserve filenames with underscores",
			"open foo_bar.go and check _internal_x.go",
			"open foo_bar.go and check _internal_x.go"},
		{"italics inside word boundaries",
			"this is _emphasized_ text",
			"this is emphasized text"},
		{"glob expression preserved", "ls *.go and check `main.go`", "ls *.go and check main.go"},
		{"combo: bold + bullet + code",
			"## Files\n- **README.md** — `entry`\n- `main.go` — `app`",
			"Files\nREADME.md — entry\nmain.go — app"},
		{"trailing newline preserved", "x\n", "x\n"},
		{"empty string", "", ""},
		{"only fence", "```\n```", "```\n```"},
		{"leading whitespace before bullet preserved",
			"  - nested item",
			"  nested item"},
		{"bold spanning newline left alone (line-oriented processor)",
			"this is **multi\nline** text",
			"this is **multi\nline** text"},
		{"backticks across newline NOT stripped",
			"`a\nb`",
			"`a\nb`"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripChatMarkdown(c.in)
			require.Equal(t, c.want, got)
		})
	}
}
