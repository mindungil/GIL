package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SearchResult is one hit from a web_search backend.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// SearchBackend is what each provider (Brave, Tavily) implements. Returning
// an empty slice + nil error means "no results"; returning ErrNoBackend
// (only from SelectBackend) means the runtime is not configured.
type SearchBackend interface {
	Name() string
	Search(ctx context.Context, query string, max int) ([]SearchResult, error)
}

// ErrNoBackend signals that no API key is configured. Callers should
// surface a hint to the agent so it can fall back to web_fetch on a
// known URL.
var ErrNoBackend = errors.New("no search backend configured (set BRAVE_SEARCH_API_KEY or TAVILY_API_KEY)")

// SelectBackend inspects environment variables and returns the first
// available backend. Brave is preferred over Tavily because it has a
// more generous free tier; either is acceptable.
//
// The returned backend uses the supplied http.Client (defaults to a 30s
// client when nil). Tests inject httptest URLs by passing a custom
// http.Client with a base URL override via the *Override fields below.
func SelectBackend(client *http.Client) (SearchBackend, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if k := os.Getenv("BRAVE_SEARCH_API_KEY"); k != "" {
		return &BraveBackend{APIKey: k, Client: client}, nil
	}
	if k := os.Getenv("TAVILY_API_KEY"); k != "" {
		return &TavilyBackend{APIKey: k, Client: client}, nil
	}
	return nil, ErrNoBackend
}

// BraveBackend talks to https://api.search.brave.com/res/v1/web/search.
//
// BaseURL is overridable for tests; production callers leave it empty
// so the default is used.
type BraveBackend struct {
	APIKey  string
	BaseURL string // override for tests
	Client  *http.Client
}

func (b *BraveBackend) Name() string { return "brave" }

func (b *BraveBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if max <= 0 {
		max = 5
	}
	base := b.BaseURL
	if base == "" {
		base = "https://api.search.brave.com/res/v1/web/search"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("brave: parse base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", max))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)

	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("brave: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("brave: decode: %w", err)
	}
	out := make([]SearchResult, 0, len(payload.Web.Results))
	for i, r := range payload.Web.Results {
		if i >= max {
			break
		}
		out = append(out, SearchResult{
			Title:   strings.TrimSpace(stripHTML(r.Title)),
			URL:     r.URL,
			Snippet: strings.TrimSpace(stripHTML(r.Description)),
		})
	}
	return out, nil
}

// TavilyBackend talks to https://api.tavily.com/search (POST JSON body).
type TavilyBackend struct {
	APIKey  string
	BaseURL string // override for tests
	Client  *http.Client
}

func (t *TavilyBackend) Name() string { return "tavily" }

func (t *TavilyBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if max <= 0 {
		max = 5
	}
	base := t.BaseURL
	if base == "" {
		base = "https://api.tavily.com/search"
	}
	body := map[string]any{
		"api_key":     t.APIKey,
		"query":       query,
		"max_results": max,
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("tavily: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}
	out := make([]SearchResult, 0, len(payload.Results))
	for i, r := range payload.Results {
		if i >= max {
			break
		}
		out = append(out, SearchResult{
			Title:   strings.TrimSpace(r.Title),
			URL:     r.URL,
			Snippet: strings.TrimSpace(r.Content),
		})
	}
	return out, nil
}

// stripHTML removes simple <tag> markup that Brave returns in titles
// (e.g. "<strong>") so the agent sees plain text.
func stripHTML(s string) string {
	var sb strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				sb.WriteRune(r)
			}
		}
	}
	return sb.String()
}
