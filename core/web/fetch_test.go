package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFetch_HTML_ConvertsToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Hello Docs</title></head><body><h1>API Reference</h1><p>Welcome.</p><ul><li>one</li><li>two</li></ul></body></html>`))
	}))
	t.Cleanup(srv.Close)

	res, err := Fetch(context.Background(), FetchOptions{URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "Hello Docs", res.Title)
	require.Contains(t, res.Markdown, "# API Reference")
	require.Contains(t, res.Markdown, "Welcome.")
	require.Contains(t, res.Markdown, "- one")
	require.Contains(t, res.Markdown, "- two")
}

func TestFetch_PlainText_PassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain string with <tags>"))
	}))
	t.Cleanup(srv.Close)

	res, err := Fetch(context.Background(), FetchOptions{URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, "plain string with <tags>", res.Markdown)
	require.Empty(t, res.Title)
}

func TestFetch_404_NotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Not Found</h1>"))
	}))
	t.Cleanup(srv.Close)

	res, err := Fetch(context.Background(), FetchOptions{URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, 404, res.StatusCode)
	require.Contains(t, res.Markdown, "Not Found")
}

func TestFetch_Truncation_AtMaxBytes(t *testing.T) {
	big := strings.Repeat("A", 10_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)

	res, err := Fetch(context.Background(), FetchOptions{URL: srv.URL, MaxBytes: 1024})
	require.NoError(t, err)
	require.True(t, res.Truncated)
	require.Equal(t, int64(1024), res.SizeBytes)
	require.Equal(t, 1024, len(res.Markdown))
}

func TestFetch_Timeout_Wrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), FetchOptions{URL: srv.URL, Timeout: 50 * time.Millisecond})
	require.Error(t, err)
	require.Contains(t, err.Error(), "fetch:")
}

func TestFetch_RejectsBadScheme(t *testing.T) {
	_, err := Fetch(context.Background(), FetchOptions{URL: "ftp://example.com"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must start with http")
}

func TestFetch_EmptyURL_Errors(t *testing.T) {
	_, err := Fetch(context.Background(), FetchOptions{})
	require.Error(t, err)
}

func TestFetch_UserAgentDefault(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("User-Agent"):
		default:
		}
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), FetchOptions{URL: srv.URL})
	require.NoError(t, err)
	ua := <-got
	require.True(t, strings.HasPrefix(ua, "gil/"), "UA was %q", ua)
}

func TestFetch_UserAgentOverride(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("User-Agent"):
		default:
		}
	}))
	t.Cleanup(srv.Close)

	_, err := Fetch(context.Background(), FetchOptions{URL: srv.URL, UserAgent: "test/1.0"})
	require.NoError(t, err)
	require.Equal(t, "test/1.0", <-got)
}

func TestFetch_LargeHTML_TitleStillExtracted(t *testing.T) {
	body := fmt.Sprintf(`<html><head><title>Big</title></head><body><h1>Top</h1><p>%s</p></body></html>`, strings.Repeat("x", 4096))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	res, err := Fetch(context.Background(), FetchOptions{URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, "Big", res.Title)
}
