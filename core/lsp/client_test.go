package lsp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestClient pairs a Client with a mockServer over in-process pipes.
// Tests register their handler shape and then call the Client method
// under test. Returns both ends so tests can register handlers / close.
func newTestClient(t *testing.T) (*Client, *mockServer) {
	t.Helper()
	mock := newMockServer(t)
	c := NewClient("test", "file:///tmp", nil, nopWriteCloser{Writer: mock.stdinW}, mock.stdoutR)
	t.Cleanup(func() {
		_ = c.Shutdown(context.Background())
		mock.close()
	})
	return c, mock
}

func TestClient_Initialize_RoundTrip(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("initialize", func(_ rawJSONOrNothing) (any, error) {
		return map[string]any{"capabilities": map[string]any{}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))
}

// rawJSONOrNothing is just an alias for json.RawMessage that makes the test
// signatures read clearly. The mock's handler signature uses raw JSON; we
// don't decode the params for these tests.
type rawJSONOrNothing = json.RawMessage

func TestClient_Hover_DecodesMarkupContent(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/hover", func(_ rawJSONOrNothing) (any, error) {
		return map[string]any{
			"contents": map[string]string{
				"kind":  "markdown",
				"value": "func Foo()",
			},
		}, nil
	})
	ctx := context.Background()
	hov, err := c.Hover(ctx, "/tmp/x.go", 5, 3)
	require.NoError(t, err)
	require.NotNil(t, hov)
	require.Contains(t, hov.Contents, "Foo")
}

func TestClient_Hover_NullContents(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/hover", func(_ rawJSONOrNothing) (any, error) {
		return nil, nil
	})
	hov, err := c.Hover(context.Background(), "/tmp/x.go", 1, 1)
	require.NoError(t, err)
	require.Nil(t, hov)
}

func TestClient_Definition_FlatArray(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/definition", func(_ rawJSONOrNothing) (any, error) {
		return []map[string]any{
			{"uri": "file:///tmp/x.go", "range": map[string]any{
				"start": map[string]int{"line": 0, "character": 0},
				"end":   map[string]int{"line": 0, "character": 5},
			}},
		}, nil
	})
	locs, err := c.Definition(context.Background(), "/tmp/x.go", 0, 0)
	require.NoError(t, err)
	require.Len(t, locs, 1)
	require.Equal(t, "file:///tmp/x.go", locs[0].URI)
}

func TestClient_Definition_LocationLink(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/definition", func(_ rawJSONOrNothing) (any, error) {
		return []map[string]any{
			{
				"targetUri": "file:///tmp/y.go",
				"targetRange": map[string]any{
					"start": map[string]int{"line": 1, "character": 2},
					"end":   map[string]int{"line": 1, "character": 7},
				},
			},
		}, nil
	})
	locs, err := c.Definition(context.Background(), "/tmp/x.go", 0, 0)
	require.NoError(t, err)
	require.Len(t, locs, 1)
	require.Equal(t, "file:///tmp/y.go", locs[0].URI)
}

func TestClient_References_IncludesDeclaration(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/references", func(_ rawJSONOrNothing) (any, error) {
		return []map[string]any{
			{"uri": "file:///tmp/a.go", "range": map[string]any{
				"start": map[string]int{"line": 1, "character": 2},
				"end":   map[string]int{"line": 1, "character": 5},
			}},
			{"uri": "file:///tmp/b.go", "range": map[string]any{
				"start": map[string]int{"line": 3, "character": 4},
				"end":   map[string]int{"line": 3, "character": 7},
			}},
		}, nil
	})
	locs, err := c.References(context.Background(), "/tmp/a.go", 1, 2)
	require.NoError(t, err)
	require.Len(t, locs, 2)
}

func TestClient_Rename_ReturnsWorkspaceEdit(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/rename", func(_ rawJSONOrNothing) (any, error) {
		return map[string]any{
			"changes": map[string]any{
				"file:///tmp/a.go": []map[string]any{
					{
						"range": map[string]any{
							"start": map[string]int{"line": 0, "character": 5},
							"end":   map[string]int{"line": 0, "character": 8},
						},
						"newText": "Bar",
					},
				},
			},
		}, nil
	})
	we, err := c.Rename(context.Background(), "/tmp/a.go", 0, 5, "Bar")
	require.NoError(t, err)
	require.NotNil(t, we)
	require.Contains(t, we.Changes, "file:///tmp/a.go")
}

func TestClient_Rename_NilEdit(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/rename", func(_ rawJSONOrNothing) (any, error) {
		return nil, nil
	})
	we, err := c.Rename(context.Background(), "/tmp/a.go", 0, 0, "Bar")
	require.NoError(t, err)
	require.Nil(t, we)
}

func TestClient_Completion_HandlesList(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/completion", func(_ rawJSONOrNothing) (any, error) {
		return map[string]any{
			"items": []map[string]any{
				{"label": "Foo", "detail": "func()", "kind": 3},
				{"label": "Foobar", "kind": 6},
			},
		}, nil
	})
	items, err := c.Completion(context.Background(), "/tmp/a.go", 0, 0)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "Foo", items[0].Label)
}

func TestClient_DocumentSymbols_Hierarchical(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/documentSymbol", func(_ rawJSONOrNothing) (any, error) {
		return []map[string]any{
			{
				"name":           "Foo",
				"kind":           12,
				"range":          rangeJSON(0, 0, 5, 0),
				"selectionRange": rangeJSON(0, 5, 0, 8),
			},
		}, nil
	})
	syms, err := c.DocumentSymbols(context.Background(), "/tmp/a.go")
	require.NoError(t, err)
	require.Len(t, syms, 1)
	require.Equal(t, "Foo", syms[0].Name)
}

func TestClient_WorkspaceSymbols(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("workspace/symbol", func(_ rawJSONOrNothing) (any, error) {
		return []map[string]any{
			{
				"name": "Bar",
				"kind": 12,
				"location": map[string]any{
					"uri":   "file:///tmp/x.go",
					"range": rangeJSON(0, 0, 0, 3),
				},
			},
		}, nil
	})
	syms, err := c.WorkspaceSymbols(context.Background(), "Bar")
	require.NoError(t, err)
	require.Len(t, syms, 1)
	require.Equal(t, "Bar", syms[0].Name)
}

func TestClient_SignatureHelp(t *testing.T) {
	c, mock := newTestClient(t)
	mock.handle("textDocument/signatureHelp", func(_ rawJSONOrNothing) (any, error) {
		return map[string]any{
			"signatures": []map[string]any{
				{"label": "Foo(x int) error"},
			},
			"activeSignature": 0,
			"activeParameter": 0,
		}, nil
	})
	sh, err := c.SignatureHelp(context.Background(), "/tmp/a.go", 0, 0)
	require.NoError(t, err)
	require.NotNil(t, sh)
	require.Len(t, sh.Signatures, 1)
}

func TestClient_Diagnostics_PublishedNotification(t *testing.T) {
	c, mock := newTestClient(t)
	// Send a publishDiagnostics notification before any request.
	mock.notify("textDocument/publishDiagnostics", map[string]any{
		"uri": "file:///tmp/foo.go",
		"diagnostics": []map[string]any{
			{
				"range":    rangeJSON(1, 0, 1, 5),
				"severity": 1,
				"message":  "undefined: Bar",
			},
		},
	})
	// Give the read loop a moment to process the notification.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d := c.Diagnostics("/tmp/foo.go"); len(d) > 0 {
			require.Equal(t, "undefined: Bar", d[0].Message)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("diagnostics not delivered")
}

func TestClient_Timeout(t *testing.T) {
	c, mock := newTestClient(t)
	// Handler that never replies — wait beyond timeout in the test.
	blocked := make(chan struct{})
	mock.handle("textDocument/hover", func(_ rawJSONOrNothing) (any, error) {
		<-blocked
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Hover(ctx, "/tmp/x.go", 0, 0)
	require.Error(t, err)
	close(blocked)
}

// rangeJSON is a tiny test helper for building Range payloads.
func rangeJSON(sl, sc, el, ec int) map[string]any {
	return map[string]any{
		"start": map[string]int{"line": sl, "character": sc},
		"end":   map[string]int{"line": el, "character": ec},
	}
}
