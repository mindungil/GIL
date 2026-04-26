// Package cost computes USD cost estimates for LLM token usage given a model
// price catalog.
//
// The default catalog is embedded at build time and reflects the current
// public list prices for the small set of models gil ships with first-class
// support. Prices are best-effort and can be stale; users may override the
// embedded set by writing a JSON file with the same shape to
// Layout.Cache/models.json (see LoadCatalog).
//
// All rates are quoted "per million tokens" in USD, matching the form
// providers publish (e.g. "$15 / 1M input tokens"). The Calculator divides
// by 1_000_000 internally so callers pass raw token counts.
//
// Reference: see /home/ubuntu/research/aider/aider/commands.py cmd_tokens
// for the inspiration. Aider stores per-token rates from litellm's catalog
// and multiplies straight through; gil's twist is the cached-read rate
// which Anthropic and OpenAI both bill at a discount.
package cost

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

// ModelPrice holds the per-million-token USD rates for a single model.
//
// CachedReadPerM and CacheWritePerM are optional. A zero value means
// "fall back to InputPerM" — providers that don't offer cache-aware billing
// effectively charge full input rates for every token.
type ModelPrice struct {
	InputPerM      float64 `json:"input_per_m"`
	OutputPerM     float64 `json:"output_per_m"`
	CachedReadPerM float64 `json:"cached_read_per_m,omitempty"`
	CacheWritePerM float64 `json:"cache_write_per_m,omitempty"`
}

// Catalog maps a model name (lowercase, provider-agnostic) to its price.
type Catalog map[string]ModelPrice

//go:embed default_catalog.json
var defaultCatalogJSON []byte

// DefaultCatalog returns the embedded default catalog. The returned map is
// a fresh copy each call so callers may mutate it without affecting future
// invocations.
//
// Panics on parse failure — the catalog is hard-coded at build time and a
// malformed JSON would be a programming error caught by tests.
func DefaultCatalog() Catalog {
	var c Catalog
	if err := json.Unmarshal(defaultCatalogJSON, &c); err != nil {
		panic(fmt.Sprintf("cost: parse embedded catalog: %v", err))
	}
	return c
}

// LoadCatalog reads a catalog from the given JSON file and merges it on top
// of the embedded defaults. The file's entries take precedence; unknown
// models are added; defaults remain for models the file omits.
//
// Returns the embedded defaults if path does not exist (so callers may pass
// a probable cache path unconditionally). Other I/O or parse errors are
// surfaced.
func LoadCatalog(path string) (Catalog, error) {
	merged := DefaultCatalog()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return merged, nil
		}
		return nil, fmt.Errorf("cost: read catalog %s: %w", path, err)
	}
	var override Catalog
	if err := json.Unmarshal(body, &override); err != nil {
		return nil, fmt.Errorf("cost: parse catalog %s: %w", path, err)
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged, nil
}
