package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mindungil/gil/core/web"
	"github.com/stretchr/testify/require"
)

// stubBackend is a deterministic SearchBackend used by websearch tests.
type stubBackend struct {
	name    string
	results []web.SearchResult
	err     error
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Search(_ context.Context, _ string, _ int) ([]web.SearchResult, error) {
	return s.results, s.err
}

func TestWebSearch_NoBackend_ReturnsHint(t *testing.T) {
	ws := &WebSearch{
		Selector: func() (web.SearchBackend, error) { return nil, web.ErrNoBackend },
	}
	res, err := ws.Run(context.Background(), json.RawMessage(`{"query":"go testing"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "no backend configured")
	require.Contains(t, res.Content, "BRAVE_SEARCH_API_KEY")
	require.Contains(t, res.Content, "TAVILY_API_KEY")
}

func TestWebSearch_BackendReturnsResults(t *testing.T) {
	stub := &stubBackend{
		name: "stub",
		results: []web.SearchResult{
			{Title: "Go testing", URL: "https://go.dev/test", Snippet: "package testing"},
			{Title: "Testify", URL: "https://github.com/stretchr/testify", Snippet: "tests"},
		},
	}
	ws := &WebSearch{
		Selector: func() (web.SearchBackend, error) { return stub, nil },
	}
	res, err := ws.Run(context.Background(), json.RawMessage(`{"query":"go testing"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "Backend: stub")
	require.Contains(t, res.Content, "1. Go testing")
	require.Contains(t, res.Content, "https://go.dev/test")
	require.Contains(t, res.Content, "package testing")
	require.Contains(t, res.Content, "2. Testify")
}

func TestWebSearch_BackendError(t *testing.T) {
	ws := &WebSearch{
		Selector: func() (web.SearchBackend, error) {
			return &stubBackend{name: "stub", err: errors.New("boom")}, nil
		},
	}
	res, err := ws.Run(context.Background(), json.RawMessage(`{"query":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "boom")
	require.Contains(t, res.Content, "stub")
}

func TestWebSearch_EmptyResults(t *testing.T) {
	ws := &WebSearch{
		Selector: func() (web.SearchBackend, error) {
			return &stubBackend{name: "stub"}, nil
		},
	}
	res, err := ws.Run(context.Background(), json.RawMessage(`{"query":"asdf"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "0 results")
}

func TestWebSearch_EmptyQuery_IsError(t *testing.T) {
	ws := &WebSearch{}
	res, err := ws.Run(context.Background(), json.RawMessage(`{"query":""}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "query is empty")
}

func TestWebSearch_BadJSON_Errors(t *testing.T) {
	ws := &WebSearch{}
	_, err := ws.Run(context.Background(), json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestWebSearch_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&WebSearch{}).Schema(), &v))
}

func TestWebSearch_ImplementsToolInterface(t *testing.T) {
	var _ Tool = (*WebSearch)(nil)
}
