package web

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTMLToMarkdown_Headings(t *testing.T) {
	html := `<html><body><h1>Top</h1><h2>Sub</h2><h3>Deep</h3></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "# Top")
	require.Contains(t, md, "## Sub")
	require.Contains(t, md, "### Deep")
}

func TestHTMLToMarkdown_Links(t *testing.T) {
	html := `<html><body><p>See <a href="https://docs.example.com/x">the docs</a> please.</p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "[the docs](https://docs.example.com/x)")
}

func TestHTMLToMarkdown_StripsScriptAndStyle(t *testing.T) {
	html := `<html><head><style>.x{color:red}</style><script>alert("hi")</script></head><body><p>visible</p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.NotContains(t, md, "alert")
	require.NotContains(t, md, "color:red")
	require.Contains(t, md, "visible")
}

func TestHTMLToMarkdown_StripsNavFooter(t *testing.T) {
	html := `<html><body><nav>menu</nav><main>content</main><footer>copyright</footer></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.NotContains(t, md, "menu")
	require.NotContains(t, md, "copyright")
	require.Contains(t, md, "content")
}

func TestHTMLToMarkdown_FencedCodeWithLanguage(t *testing.T) {
	html := `<html><body><pre><code class="language-go">func main() { fmt.Println("hi") }</code></pre></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "```go")
	require.Contains(t, md, `func main() { fmt.Println("hi") }`)
	require.Contains(t, md, "```")
}

func TestHTMLToMarkdown_FencedCodeNoLanguage(t *testing.T) {
	html := `<html><body><pre><code>echo hello</code></pre></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "```\necho hello")
}

func TestHTMLToMarkdown_UnorderedList(t *testing.T) {
	html := `<html><body><ul><li>alpha</li><li>beta</li></ul></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "- alpha")
	require.Contains(t, md, "- beta")
}

func TestHTMLToMarkdown_OrderedList(t *testing.T) {
	html := `<html><body><ol><li>one</li><li>two</li><li>three</li></ol></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "1. one")
	require.Contains(t, md, "2. two")
	require.Contains(t, md, "3. three")
}

func TestHTMLToMarkdown_Title(t *testing.T) {
	html := `<html><head><title>Page Title 123</title></head><body><p>x</p></body></html>`
	_, title, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Equal(t, "Page Title 123", title)
}

func TestHTMLToMarkdown_Table(t *testing.T) {
	html := `<html><body><table>
<tr><th>name</th><th>kind</th></tr>
<tr><td>foo</td><td>func</td></tr>
<tr><td>bar</td><td>struct</td></tr>
</table></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "| name | kind |")
	require.Contains(t, md, "| --- | --- |")
	require.Contains(t, md, "| foo | func |")
}

func TestHTMLToMarkdown_PlainTextPassThrough(t *testing.T) {
	body := []byte("just text, no html")
	md, _, err := HTMLToMarkdown(body, "text/plain")
	require.NoError(t, err)
	require.Equal(t, "just text, no html", md)
}

func TestHTMLToMarkdown_JSONPassThrough(t *testing.T) {
	body := []byte(`{"k": "v"}`)
	md, _, err := HTMLToMarkdown(body, "application/json")
	require.NoError(t, err)
	require.Equal(t, `{"k": "v"}`, md)
}

func TestHTMLToMarkdown_MarkdownPassThrough(t *testing.T) {
	body := []byte("# Heading\n\nbody")
	md, _, err := HTMLToMarkdown(body, "text/markdown")
	require.NoError(t, err)
	require.Equal(t, "# Heading\n\nbody", md)
}

func TestHTMLToMarkdown_CollapsesBlankLines(t *testing.T) {
	html := `<html><body><p>one</p><p>two</p><p>three</p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	// Should not have 3+ consecutive newlines.
	require.NotContains(t, md, "\n\n\n")
}

func TestHTMLToMarkdown_BoldEm(t *testing.T) {
	html := `<html><body><p><strong>important</strong> and <em>nuanced</em></p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "**important**")
	require.Contains(t, md, "*nuanced*")
}

func TestHTMLToMarkdown_InlineCode(t *testing.T) {
	html := `<html><body><p>Run <code>go test</code> please.</p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "`go test`")
}

func TestHTMLToMarkdown_ImageWithAlt(t *testing.T) {
	html := `<html><body><img alt="Logo" src="/logo.png"></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "![Logo](/logo.png)")
}

func TestHTMLToMarkdown_ImageNoAltSkipped(t *testing.T) {
	html := `<html><body><img src="/decorative.png"></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.NotContains(t, md, "decorative.png")
}

func TestHTMLToMarkdown_LooksLikeHTML_DetectsWithoutContentType(t *testing.T) {
	html := `<!DOCTYPE html><html><body><h1>X</h1></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "")
	require.NoError(t, err)
	require.Contains(t, md, "# X")
}

func TestHTMLToMarkdown_HRBreak(t *testing.T) {
	html := `<html><body><p>before</p><hr><p>after</p></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.Contains(t, md, "---")
}

func TestHTMLToMarkdown_BlockQuote(t *testing.T) {
	html := `<html><body><blockquote><p>quoted</p></blockquote></body></html>`
	md, _, err := HTMLToMarkdown([]byte(html), "text/html")
	require.NoError(t, err)
	require.True(t, strings.Contains(md, "> quoted"), "got %q", md)
}
