package service

import (
	"github.com/mindungil/gil/core/mcpregistry"
)

// mergeMCPServers returns a single map combining spec-pinned servers and
// registry-resolved servers. Spec entries always win on name collision —
// they are scoped to this run (came in via the frozen spec) so they are
// strictly more specific than the user-wide registry.
//
// Both inputs may be nil; the result is always non-nil so call sites can
// iterate without a nil-guard. Returned values are copies of the inputs;
// mutating the result does not affect either source map.
//
// The function deliberately lives in its own file (and is a pure function
// over maps) so run.go can stay focused on lifecycle wiring while the
// merge logic gets exhaustive unit coverage in run_mcp_test.go.
func mergeMCPServers(spec, registry map[string]mcpregistry.Server) map[string]mcpregistry.Server {
	out := make(map[string]mcpregistry.Server, len(spec)+len(registry))
	// Registry first; spec overwrites on collision.
	for name, s := range registry {
		s.Name = name
		out[name] = s
	}
	for name, s := range spec {
		s.Name = name
		out[name] = s
	}
	return out
}

// shadowedRegistryNames returns the sorted list of registry names that the
// spec shadowed in a merge. Used by run.go to emit a single observability
// event so the user sees which registry entries were overridden by the
// frozen spec on a given run.
//
// Returned slice is empty (never nil) when there is no shadow; ordering is
// stable so the event payload diffs cleanly across reruns.
func shadowedRegistryNames(spec, registry map[string]mcpregistry.Server) []string {
	if len(spec) == 0 || len(registry) == 0 {
		return []string{}
	}
	out := make([]string, 0)
	for name := range registry {
		if _, ok := spec[name]; ok {
			out = append(out, name)
		}
	}
	// Sort in-place for stability without pulling in sort here — the
	// caller already imports it for other purposes, but keep this helper
	// dependency-free by doing a tiny insertion sort. The expected size
	// is small (registry rarely exceeds a handful of entries).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
