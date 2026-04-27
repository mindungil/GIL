package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebFetch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>API v1</title></head><body><h1>API v1</h1><p>Reference.</p></body></html>`))
	}))
	t.Cleanup(srv.Close)

	wf := &WebFetch{}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "URL: "+srv.URL)
	require.Contains(t, res.Content, "Title: API v1")
	require.Contains(t, res.Content, "Status: 200")
	require.Contains(t, res.Content, "# API v1")
	require.Contains(t, res.Content, "Reference.")
}

func TestWebFetch_404_NotIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Missing</h1>"))
	}))
	t.Cleanup(srv.Close)

	wf := &WebFetch{}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	require.NoError(t, err)
	// 404 surfaces in the body, not as a tool-level error.
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "Status: 404")
	require.Contains(t, res.Content, "Missing")
}

func TestWebFetch_EmptyURL_IsError(t *testing.T) {
	wf := &WebFetch{}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":""}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "url is empty")
}

func TestWebFetch_BadScheme_IsError(t *testing.T) {
	wf := &WebFetch{}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":"ftp://example.com"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "must start with http")
}

func TestWebFetch_TruncationHint(t *testing.T) {
	big := strings.Repeat("AB", 4000) // 8000 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)

	wf := &WebFetch{}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`","max_bytes":1024}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "truncated")
}

func TestWebFetch_CeilingClamp(t *testing.T) {
	// MaxBytesCeiling=512 should clamp the agent's max_bytes=999999.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("z", 5000)))
	}))
	t.Cleanup(srv.Close)

	wf := &WebFetch{MaxBytesCeiling: 512}
	res, err := wf.Run(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`","max_bytes":999999}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "Size: 512 B")
	require.Contains(t, res.Content, "truncated")
}

func TestWebFetch_BadJSON_Errors(t *testing.T) {
	wf := &WebFetch{}
	_, err := wf.Run(context.Background(), json.RawMessage(`{not json`))
	require.Error(t, err)
}

func TestWebFetch_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&WebFetch{}).Schema(), &v))
}

func TestWebFetch_ImplementsToolInterface(t *testing.T) {
	var _ Tool = (*WebFetch)(nil)
}

func TestHumanBytes(t *testing.T) {
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "2 KiB", humanBytes(2048))
	require.Equal(t, "3 MiB", humanBytes(3*1024*1024))
}
