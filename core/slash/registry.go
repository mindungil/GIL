package slash

import (
	"sort"
	"strings"
	"sync"
)

// Registry maps command names (and aliases) to their Spec. It is safe for
// concurrent use — handlers may be registered from one goroutine while the
// TUI Update loop dispatches from another.
//
// Lookup is case-insensitive: `/HELP` and `/Help` resolve to `/help`.
type Registry struct {
	mu      sync.RWMutex
	byName  map[string]*Spec // includes aliases pointing at the same *Spec
	canon   map[string]*Spec // canonical names only (used by List)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]*Spec),
		canon:  make(map[string]*Spec),
	}
}

// Register inserts a Spec. Names are stored lower-cased. If the canonical
// name or any alias collides with an already-registered command the new
// Spec overwrites the prior one — the TUI registers commands once at
// startup so collision is a programming error, not a runtime concern.
func (r *Registry) Register(s Spec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := s
	canon := strings.ToLower(strings.TrimSpace(cp.Name))
	if canon == "" {
		return
	}
	cp.Name = canon
	r.canon[canon] = &cp
	r.byName[canon] = &cp
	for _, a := range cp.Aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || a == canon {
			continue
		}
		r.byName[a] = &cp
	}
}

// Lookup resolves a name (canonical or alias) case-insensitively.
func (r *Registry) Lookup(name string) (*Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

// List returns canonical specs sorted by Name. Aliases are not duplicated
// in the result — `/help` lists each command once, matching how codex-rs
// renders its popup.
func (r *Registry) List() []*Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Spec, 0, len(r.canon))
	for _, s := range r.canon {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
