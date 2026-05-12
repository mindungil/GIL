// Package web implements HTTP fetch + HTML→Markdown conversion for the
// agent's web_fetch / web_search tools. The conversion is intentionally
// "good enough for docs pages" rather than a perfect renderer — JS is
// not executed, complex layouts are flattened. Network access is fully
// honoured by the caller-supplied http.Client so tests can inject
// httptest.NewServer URLs without any global state.
//
// Lifted from:
//   - opencode/packages/opencode/src/tool/webfetch.ts (URL → markdown shape,
//     5 MiB cap, content-type sniffing — but we use stdlib instead of turndown)
//   - aider/aider/scrape.py (scrape→markdown contract; pandoc replaced with
//     a stdlib-only walker so we don't ship a Python dependency)
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mindungil/gil/core/version"
)

// Defaults kept here so callers can read them when constructing FetchOptions.
const (
	DefaultTimeout  = 30 * time.Second
	DefaultMaxBytes = int64(2 * 1024 * 1024) // 2 MiB
)

// FetchOptions configures a single Fetch call.
//
// All fields are optional; zero values pick the defaults above. URL is
// the one required field — Fetch returns an error if it is empty or
// missing a scheme.
type FetchOptions struct {
	URL       string
	Timeout   time.Duration
	MaxBytes  int64
	UserAgent string
	HTTP      *http.Client
}

// FetchResult is the outcome of a Fetch call. Non-2xx statuses are NOT
// errors — they're returned with StatusCode populated and Markdown set
// to whatever the server sent (truncated to MaxBytes). Network/timeout
// errors are wrapped and returned via the error return.
type FetchResult struct {
	URL         string
	StatusCode  int
	ContentType string
	Title       string
	Markdown    string
	SizeBytes   int64
	Truncated   bool
	FetchedAt   time.Time
}

// Fetch performs a GET on opts.URL and returns the response body
// converted to markdown when content-type is HTML (or to its original
// text form for text/plain, application/json, text/markdown, …).
//
// Errors:
//   - empty URL or missing scheme → returned synchronously
//   - DNS / connection / TLS / timeout / context cancellation → wrapped
//   - 4xx / 5xx → NOT errors; FetchResult.StatusCode is populated
func Fetch(ctx context.Context, opts FetchOptions) (*FetchResult, error) {
	if opts.URL == "" {
		return nil, errors.New("fetch: URL is empty")
	}
	if !strings.HasPrefix(opts.URL, "http://") && !strings.HasPrefix(opts.URL, "https://") {
		return nil, fmt.Errorf("fetch: URL must start with http:// or https:// (got %q)", opts.URL)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "gil/" + version.Version
	}

	client := opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	// Honour ctx + per-call timeout (whichever fires first).
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,text/markdown;q=0.9,text/plain;q=0.8,application/json;q=0.7,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: do: %w", err)
	}
	defer resp.Body.Close()

	// LimitReader caps memory; we set the limit to maxBytes+1 so we can
	// detect truncation cheaply (if we read maxBytes+1 bytes, the source
	// had more).
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch: read body: %w", err)
	}
	truncated := false
	if int64(len(body)) > maxBytes {
		body = body[:maxBytes]
		truncated = true
	}

	contentType := resp.Header.Get("Content-Type")
	markdown, title, err := HTMLToMarkdown(body, contentType)
	if err != nil {
		// HTML parse error is best-effort — fall back to raw body.
		markdown = string(body)
	}

	return &FetchResult{
		URL:         opts.URL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Title:       title,
		Markdown:    markdown,
		SizeBytes:   int64(len(body)),
		Truncated:   truncated,
		FetchedAt:   time.Now().UTC(),
	}, nil
}
