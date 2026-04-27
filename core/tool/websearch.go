package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mindungil/gil/core/web"
)

// WebSearch is the agent-callable wrapper around a pluggable search
// backend (Brave / Tavily). When no backend env var is set we DO NOT
// fail the run — the tool returns IsError=true with a hint, and the
// agent learns to fall back to web_fetch on a known URL.
//
// Args (JSON): { query: string, max?: int }
//
// Output: line-delimited "title — url\n  snippet" blocks for the top
// max results (default 5).
type WebSearch struct {
	// Selector, when non-nil, overrides the default env-driven backend
	// selection. Tests inject a stub here to exercise the formatting
	// path without env mutation; production callers leave it nil.
	Selector func() (web.SearchBackend, error)
}

const webSearchSchema = `{
  "type":"object",
  "properties":{
    "query":{
      "type":"string",
      "description":"Search query — natural language is fine."
    },
    "max":{
      "type":"integer",
      "description":"Max results (default 5)."
    }
  },
  "required":["query"]
}`

func (w *WebSearch) Name() string { return "web_search" }

func (w *WebSearch) Description() string {
	return "Search the web and return top results with title + URL + snippet. Requires BRAVE_SEARCH_API_KEY or TAVILY_API_KEY. If neither is set, returns a hint and you should fall back to web_fetch on a known URL."
}

func (w *WebSearch) Schema() json.RawMessage { return json.RawMessage(webSearchSchema) }

func (w *WebSearch) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		Query string `json:"query"`
		Max   int    `json:"max"`
	}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return Result{}, fmt.Errorf("web_search unmarshal: %w", err)
		}
	}
	if strings.TrimSpace(args.Query) == "" {
		return Result{Content: "query is empty", IsError: true}, nil
	}
	max := args.Max
	if max <= 0 {
		max = 5
	}

	selector := w.Selector
	if selector == nil {
		selector = func() (web.SearchBackend, error) { return web.SelectBackend(nil) }
	}
	backend, err := selector()
	if err != nil {
		if errors.Is(err, web.ErrNoBackend) {
			return Result{
				Content: "web_search: no backend configured. Set BRAVE_SEARCH_API_KEY or TAVILY_API_KEY in the environment, or fall back to web_fetch on a known URL.",
				IsError: true,
			}, nil
		}
		return Result{Content: "web_search: " + err.Error(), IsError: true}, nil
	}

	results, err := backend.Search(ctx, args.Query, max)
	if err != nil {
		return Result{Content: fmt.Sprintf("web_search (%s): %v", backend.Name(), err), IsError: true}, nil
	}
	if len(results) == 0 {
		return Result{Content: fmt.Sprintf("web_search (%s): 0 results for %q", backend.Name(), args.Query)}, nil
	}

	return Result{Content: formatWebSearchResults(backend.Name(), args.Query, results)}, nil
}

func formatWebSearchResults(backendName, query string, results []web.SearchResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Backend: %s\nQuery: %s\nResults: %d\n\n", backendName, query, len(results)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			sb.WriteString("   ")
			sb.WriteString(r.Snippet)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}
