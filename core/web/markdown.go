package web

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToMarkdown converts HTML (or pass-through text) to a clean
// markdown approximation.
//
// Rules:
//   - text/markdown, text/plain, application/json → returned verbatim
//     (with empty title) so callers don't double-convert.
//   - text/html or unspecified content type with html-looking body → walk
//     the DOM, emit markdown for known tags, drop nav/script/style/iframe.
//   - parse failure on HTML returns ("", "", error) — caller may fall back
//     to the raw body.
//
// The output is normalized to collapse 3+ consecutive blank lines into 2.
func HTMLToMarkdown(body []byte, contentType string) (markdown, title string, err error) {
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mime {
	case "text/markdown", "text/x-markdown", "text/plain", "application/json":
		return string(body), "", nil
	}
	// Empty or unrecognized content type: probe for HTML markers.
	if mime != "text/html" && mime != "application/xhtml+xml" && mime != "" {
		if !looksLikeHTML(body) {
			return string(body), "", nil
		}
	}
	if mime == "" && !looksLikeHTML(body) {
		return string(body), "", nil
	}

	doc, perr := html.Parse(bytes.NewReader(body))
	if perr != nil {
		return "", "", fmt.Errorf("html.Parse: %w", perr)
	}

	c := newConverter()
	c.walk(doc)
	return collapseBlankLines(c.buf.String()), c.title, nil
}

func looksLikeHTML(body []byte) bool {
	lo := bytes.ToLower(body)
	if len(lo) > 2048 {
		lo = lo[:2048]
	}
	for _, marker := range [][]byte{
		[]byte("<!doctype html"),
		[]byte("<html"),
		[]byte("<body"),
		[]byte("<head"),
		[]byte("<div"),
		[]byte("<p>"),
	} {
		if bytes.Contains(lo, marker) {
			return true
		}
	}
	return false
}

// converter walks an html.Node tree and emits markdown.
type converter struct {
	buf       strings.Builder
	title     string
	listDepth int
	inPre     bool   // inside <pre> — preserve whitespace verbatim
	preLang   string // language hint for fenced code block
}

func newConverter() *converter {
	return &converter{}
}

// skipTags are dropped along with their entire subtree.
var skipTags = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Iframe:   true,
	atom.Nav:      true,
	atom.Footer:   true,
	atom.Form:     true, // forms in docs pages are rarely useful to an agent
}

func (c *converter) walk(n *html.Node) {
	if n == nil {
		return
	}

	switch n.Type {
	case html.TextNode:
		if c.inPre {
			c.buf.WriteString(n.Data)
			return
		}
		// Collapse internal whitespace runs to a single space; the
		// surrounding block elements add the real newlines.
		text := whitespaceRun.ReplaceAllString(n.Data, " ")
		// Trim leading space when previous char is a newline so we don't
		// indent paragraphs accidentally.
		if endsWithNewline(c.buf.String()) {
			text = strings.TrimLeft(text, " ")
		}
		c.buf.WriteString(text)
		return

	case html.ElementNode:
		if skipTags[n.DataAtom] {
			return
		}
		c.openElement(n)
		return

	case html.DocumentNode, html.DoctypeNode:
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch)
		}
		return
	}

	// Default: recurse.
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// openElement handles opening + closing semantics for one element.
func (c *converter) openElement(n *html.Node) {
	switch n.DataAtom {
	case atom.Title:
		// Capture <title> text but don't emit into body.
		if c.title == "" {
			var sb strings.Builder
			collectText(n, &sb)
			c.title = strings.TrimSpace(whitespaceRun.ReplaceAllString(sb.String(), " "))
		}
		return

	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level := int(n.DataAtom-atom.H1) + 1
		c.ensureBlankLine()
		c.buf.WriteString(strings.Repeat("#", level))
		c.buf.WriteString(" ")
		c.walkChildrenInline(n)
		c.buf.WriteString("\n\n")
		return

	case atom.P, atom.Div, atom.Section, atom.Article, atom.Header, atom.Main, atom.Aside:
		// Treat as block: ensure blank line before, walk, then blank line after.
		c.ensureBlankLine()
		c.walkChildren(n)
		c.ensureBlankLine()
		return

	case atom.Br:
		c.buf.WriteString("  \n") // markdown line break
		return

	case atom.Hr:
		c.ensureBlankLine()
		c.buf.WriteString("---\n\n")
		return

	case atom.Strong, atom.B:
		c.buf.WriteString("**")
		c.walkChildrenInline(n)
		c.buf.WriteString("**")
		return

	case atom.Em, atom.I:
		c.buf.WriteString("*")
		c.walkChildrenInline(n)
		c.buf.WriteString("*")
		return

	case atom.Code:
		// Inline code unless we're already inside <pre> (which renders the fence itself).
		if c.inPre {
			c.walkChildren(n)
			return
		}
		c.buf.WriteString("`")
		c.walkChildrenInline(n)
		c.buf.WriteString("`")
		return

	case atom.Pre:
		// Fenced code block. Look for a child <code class="language-X"> for the hint.
		lang := preLanguage(n)
		c.ensureBlankLine()
		c.buf.WriteString("```")
		c.buf.WriteString(lang)
		c.buf.WriteString("\n")
		prevPre := c.inPre
		prevLang := c.preLang
		c.inPre = true
		c.preLang = lang
		c.walkChildren(n)
		c.inPre = prevPre
		c.preLang = prevLang
		// Ensure trailing newline before fence close.
		if !endsWithNewline(c.buf.String()) {
			c.buf.WriteString("\n")
		}
		c.buf.WriteString("```\n\n")
		return

	case atom.A:
		href := getAttr(n, "href")
		var sb strings.Builder
		collectText(n, &sb)
		text := strings.TrimSpace(whitespaceRun.ReplaceAllString(sb.String(), " "))
		if href == "" {
			c.buf.WriteString(text)
			return
		}
		if text == "" {
			text = href
		}
		c.buf.WriteString("[")
		c.buf.WriteString(text)
		c.buf.WriteString("](")
		c.buf.WriteString(href)
		c.buf.WriteString(")")
		return

	case atom.Img:
		alt := getAttr(n, "alt")
		src := getAttr(n, "src")
		// Skip data: URIs (huge inline blobs) and images without alt — they
		// rarely help an agent and bloat the context.
		if src == "" || strings.HasPrefix(src, "data:") {
			return
		}
		if alt == "" {
			return
		}
		c.buf.WriteString("![")
		c.buf.WriteString(alt)
		c.buf.WriteString("](")
		c.buf.WriteString(src)
		c.buf.WriteString(")")
		return

	case atom.Ul, atom.Ol:
		c.ensureBlankLine()
		c.listDepth++
		marker := "-"
		ordered := n.DataAtom == atom.Ol
		idx := 1
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type != html.ElementNode || ch.DataAtom != atom.Li {
				continue
			}
			c.buf.WriteString(strings.Repeat("  ", c.listDepth-1))
			if ordered {
				c.buf.WriteString(fmt.Sprintf("%d. ", idx))
				idx++
			} else {
				c.buf.WriteString(marker + " ")
			}
			c.walkChildrenInline(ch)
			if !endsWithNewline(c.buf.String()) {
				c.buf.WriteString("\n")
			}
		}
		c.listDepth--
		c.buf.WriteString("\n")
		return

	case atom.Li:
		// Standalone <li> outside <ul>/<ol>: emit as bullet.
		c.buf.WriteString("- ")
		c.walkChildrenInline(n)
		c.buf.WriteString("\n")
		return

	case atom.Blockquote:
		c.ensureBlankLine()
		// Quote each child line.
		var sub strings.Builder
		tmp := *c
		tmp.buf = sub
		tmp.walkChildren(n)
		text := tmp.buf.String()
		c.title = tmp.title
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			c.buf.WriteString("> ")
			c.buf.WriteString(line)
			c.buf.WriteString("\n")
		}
		c.buf.WriteString("\n")
		return

	case atom.Table:
		c.ensureBlankLine()
		emitTable(c, n)
		c.ensureBlankLine()
		return

	case atom.Head, atom.Html, atom.Body:
		c.walkChildren(n)
		return
	}

	// Unknown / generic inline element: just recurse into children.
	c.walkChildren(n)
}

// walkChildren walks all element children with default rules.
func (c *converter) walkChildren(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// walkChildrenInline walks children but strips trailing whitespace from
// the buffer afterwards — used inside headings and list items so we don't
// emit spurious newlines that break the inline flow.
func (c *converter) walkChildrenInline(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// ensureBlankLine guarantees the buffer ends with "\n\n" (or starts empty).
func (c *converter) ensureBlankLine() {
	s := c.buf.String()
	if s == "" {
		return
	}
	if strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		c.buf.WriteString("\n")
		return
	}
	c.buf.WriteString("\n\n")
}

// preLanguage returns the language hint for a <pre> block by inspecting
// its first <code> child's class attribute (e.g. "language-go" → "go").
func preLanguage(pre *html.Node) string {
	for ch := pre.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.DataAtom == atom.Code {
			class := getAttr(ch, "class")
			for _, part := range strings.Fields(class) {
				if strings.HasPrefix(part, "language-") {
					return strings.TrimPrefix(part, "language-")
				}
				if strings.HasPrefix(part, "lang-") {
					return strings.TrimPrefix(part, "lang-")
				}
			}
		}
	}
	return ""
}

// emitTable renders a simple GFM table from rows of <tr><th|td>...
func emitTable(c *converter, table *html.Node) {
	var rows [][]string
	walkRows(table, &rows)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return
	}
	// Pad rows to equal column count.
	for i := range rows {
		for len(rows[i]) < cols {
			rows[i] = append(rows[i], "")
		}
	}
	// Header row: first row.
	c.buf.WriteString("| ")
	c.buf.WriteString(strings.Join(rows[0], " | "))
	c.buf.WriteString(" |\n|")
	for i := 0; i < cols; i++ {
		c.buf.WriteString(" --- |")
	}
	c.buf.WriteString("\n")
	for _, r := range rows[1:] {
		c.buf.WriteString("| ")
		c.buf.WriteString(strings.Join(r, " | "))
		c.buf.WriteString(" |\n")
	}
}

func walkRows(n *html.Node, out *[][]string) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
		var row []string
		for cell := n.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode {
				continue
			}
			if cell.DataAtom == atom.Th || cell.DataAtom == atom.Td {
				var sb strings.Builder
				collectText(cell, &sb)
				cellText := strings.TrimSpace(whitespaceRun.ReplaceAllString(sb.String(), " "))
				// Pipe characters in cells break the table; escape them.
				cellText = strings.ReplaceAll(cellText, "|", "\\|")
				row = append(row, cellText)
			}
		}
		if len(row) > 0 {
			*out = append(*out, row)
		}
		return
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		walkRows(ch, out)
	}
}

// getAttr returns the named attribute or "" when absent.
func getAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// collectText recursively appends all text node content under n. Skips
// the same dropped tags as walk() does so we don't leak <script> content
// through alt attributes.
func collectText(n *html.Node, sb *strings.Builder) {
	if n == nil {
		return
	}
	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode && skipTags[n.DataAtom] {
		return
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		collectText(ch, sb)
	}
}

func endsWithNewline(s string) bool {
	return strings.HasSuffix(s, "\n")
}

var (
	whitespaceRun  = regexp.MustCompile(`\s+`)
	multiBlankLine = regexp.MustCompile(`\n{3,}`)
)

func collapseBlankLines(s string) string {
	s = multiBlankLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s) + "\n"
}
