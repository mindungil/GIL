package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mindungil/gil/core/repomap"
)

// Repomap is the agent-callable tool that returns a markdown overview of the
// project's symbols, ranked by PageRank-style importance and trimmed to a
// token budget.
//
// Args: { "max_tokens": 4096 (optional) }
//
// Caches the rendered output for 60s per (root, max_tokens) pair to avoid
// re-walking the project on rapid repeated calls.
type Repomap struct {
	Root      string // project root; required
	MaxTokens int    // default 4096

	mu    sync.Mutex
	cache map[string]repomapCacheEntry
}

type repomapCacheEntry struct {
	rendered string
	expires  time.Time
}

const (
	repomapCacheTTL      = 60 * time.Second
	repomapDefaultTokens = 4096
)

const repomapSchema = `{
  "type":"object",
  "properties":{
    "max_tokens":{"type":"integer","description":"max tokens of repomap output (default 4096)"}
  }
}`

func (r *Repomap) Name() string { return "repomap" }

func (r *Repomap) Description() string {
	return "Return a markdown overview of the project's important symbols (functions, structs, classes), ranked by call-graph centrality and trimmed to a token budget. Use to orient yourself in an unfamiliar codebase."
}

func (r *Repomap) Schema() json.RawMessage { return json.RawMessage(repomapSchema) }

func (r *Repomap) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	var args struct {
		MaxTokens int `json:"max_tokens"`
	}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return Result{}, fmt.Errorf("repomap unmarshal: %w", err)
		}
	}
	if r.Root == "" {
		return Result{Content: "repomap: root is not configured for this run", IsError: true}, nil
	}
	maxTokens := args.MaxTokens
	if maxTokens <= 0 {
		maxTokens = r.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = repomapDefaultTokens
	}

	cacheKey := fmt.Sprintf("%s|%d", r.Root, maxTokens)
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]repomapCacheEntry{}
	}
	if e, ok := r.cache[cacheKey]; ok && time.Now().Before(e.expires) {
		r.mu.Unlock()
		return Result{Content: e.rendered}, nil
	}
	r.mu.Unlock()

	syms, warns, err := repomap.WalkProject(ctx, r.Root, repomap.WalkOptions{})
	if err != nil {
		return Result{Content: "repomap: walk failed: " + err.Error(), IsError: true}, nil
	}
	if len(syms) == 0 {
		msg := "repomap: no source files found under " + r.Root
		if len(warns) > 0 {
			msg += "\n\nwarnings:\n"
			for _, w := range warns {
				msg += "- " + w + "\n"
			}
		}
		return Result{Content: msg}, nil
	}
	ranked := repomap.Rank(syms)
	rendered := repomap.Fit(ranked, maxTokens)
	if rendered == "" {
		rendered = "(no symbols fit within max_tokens budget; try a larger max_tokens)"
	}

	r.mu.Lock()
	r.cache[cacheKey] = repomapCacheEntry{
		rendered: rendered,
		expires:  time.Now().Add(repomapCacheTTL),
	}
	r.mu.Unlock()

	return Result{Content: rendered}, nil
}
