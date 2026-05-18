Write a Go module in the current working directory that converts markdown to HTML.

Requirements:
- Accept markdown on stdin, write HTML to stdout (a `md2html` binary)
- Support these features:
  - ATX headers `# `, `## `, `### ` → `<h1>`, `<h2>`, `<h3>`
  - Paragraphs (blank-line separated)
  - Bold `**text**` → `<strong>text</strong>`
  - Italic `*text*` → `<em>text</em>` (single asterisks not paired with bold)
  - Inline code `` `code` `` → `<code>code</code>`
  - Fenced code blocks ```` ``` ```` → `<pre><code>…</code></pre>`
  - Unordered lists (lines starting with `- ` or `* `) → `<ul><li>…</li></ul>`
- Include `go test ./...` that exercises each of the 6 features above with at least one case each.
- Initialize go.mod yourself (module name your choice).
- Build the binary as `md2html` (cmd/md2html or root, agent's call).

Done when `go test ./...` passes from this directory.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. Make any unspecified choices yourself and proceed.
