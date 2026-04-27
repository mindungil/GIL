package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectBackend_NoEnv_ReturnsErr(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	_, err := SelectBackend(nil)
	require.ErrorIs(t, err, ErrNoBackend)
}

func TestSelectBackend_BravePreferred(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")
	b, err := SelectBackend(nil)
	require.NoError(t, err)
	require.Equal(t, "brave", b.Name())
}

func TestSelectBackend_TavilyFallback(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "tavily-key")
	b, err := SelectBackend(nil)
	require.NoError(t, err)
	require.Equal(t, "tavily", b.Name())
}

func TestBraveBackend_Search_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-key", r.Header.Get("X-Subscription-Token"))
		require.Equal(t, "go testing", r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "web": {
    "results": [
      {"title": "<strong>Go Testing</strong>", "url": "https://go.dev/test", "description": "package <em>testing</em>"},
      {"title": "Testify", "url": "https://github.com/stretchr/testify", "description": "tests in Go"}
    ]
  }
}`))
	}))
	t.Cleanup(srv.Close)

	b := &BraveBackend{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}
	results, err := b.Search(context.Background(), "go testing", 5)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "Go Testing", results[0].Title) // <strong> stripped
	require.Equal(t, "https://go.dev/test", results[0].URL)
	require.Equal(t, "package testing", results[0].Snippet)
}

func TestTavilyBackend_Search_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "results": [
    {"title": "Foo", "url": "https://foo.example", "content": "snippet"}
  ]
}`))
	}))
	t.Cleanup(srv.Close)

	b := &TavilyBackend{
		APIKey:  "tk",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}
	results, err := b.Search(context.Background(), "foo", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "Foo", results[0].Title)
	require.Equal(t, "snippet", results[0].Snippet)
}

func TestBraveBackend_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	b := &BraveBackend{APIKey: "bad", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.Search(context.Background(), "x", 5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestStripHTML(t *testing.T) {
	require.Equal(t, "hello world", stripHTML("<b>hello</b> <i>world</i>"))
	require.Equal(t, "no tags", stripHTML("no tags"))
}
