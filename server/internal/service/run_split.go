// Helpers for the architect/coder per-role provider split (Phase 19
// Track C). Lives next to run.go so the construction logic is close to
// its only caller (RunService.executeRun) but isolated for testability.
package service

import (
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/runner"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// roleSpec captures the (provider name, model id) pair the spec
// requested for a role. Empty fields mean "fall back to main".
type roleSpec struct {
	Provider string
	Model    string
}

// roleSelections extracts the user-requested provider/model for each
// classifyTurn role from the frozen spec. Returns nil entries for roles
// the spec leaves unset; the caller layers those onto a default.
func roleSelections(spec *gilv1.FrozenSpec) map[string]roleSpec {
	out := map[string]roleSpec{}
	if spec == nil || spec.Models == nil {
		return out
	}
	if m := spec.Models.GetMain(); m != nil {
		out[runner.RoleMain] = roleSpec{Provider: m.Provider, Model: m.ModelId}
	}
	if m := spec.Models.GetPlanner(); m != nil {
		out[runner.RolePlanner] = roleSpec{Provider: m.Provider, Model: m.ModelId}
	}
	if m := spec.Models.GetEditor(); m != nil {
		out[runner.RoleEditor] = roleSpec{Provider: m.Provider, Model: m.ModelId}
	}
	return out
}

// buildRoleProviders constructs the per-role Provider+Model maps the
// runner consumes. It keys off the unique (factory-key, model) tuple so
// a spec that points multiple roles at the same backend reuses one
// Provider instance — important because each Provider may carry a
// connection pool / API client we don't want to duplicate.
//
// Inputs:
//   - spec: the frozen spec (Models field drives selection).
//   - factory: the daemon's ProviderFactory so per-role provider names
//     resolve through the same auth/credential path the main provider
//     uses.
//   - defaultProv / defaultModel / defaultName: the already-constructed
//     main provider, resolved model id, and the FACTORY KEY that built
//     it (the request's req.Provider, NOT defaultProv.Name() — the
//     latter may carry suffixes from wrappers like "+retry" and would
//     break cache hits when the spec specifies the same provider by its
//     bare name).
//
// Outputs (returned even on partial failure — see error semantics):
//   - providers: map[role]Provider for runner.AgentLoop.Providers.
//   - models:    map[role]string  for runner.AgentLoop.Models.
//   - err:       non-nil ONLY when an explicitly-requested role's
//     provider name failed to resolve. A role left blank in the spec is
//     not an error — the runner falls back to main automatically. We
//     fail the whole run on a typo'd provider name rather than silently
//     downgrading because the user clearly intended a specific override.
//
// The caller should ALWAYS register the returned maps on the AgentLoop
// even when only a single role is wired — the runner gracefully handles
// missing entries by falling back to .Provider/.Model. Wiring the map
// even when "empty after main" is harmless and keeps the code path
// uniform across single- and multi-provider configurations.
func buildRoleProviders(
	spec *gilv1.FrozenSpec,
	factory ProviderFactory,
	defaultProv provider.Provider,
	defaultModel string,
	defaultName string,
) (map[string]provider.Provider, map[string]string, error) {
	providers := map[string]provider.Provider{
		runner.RoleMain: defaultProv,
	}
	models := map[string]string{
		runner.RoleMain: defaultModel,
	}

	selections := roleSelections(spec)

	// Cache factoryKey → Provider so we don't construct (or re-wrap
	// with NewRetry) the same backend twice. The key uses the FACTORY
	// name (e.g., "mock", "anthropic") rather than the constructed
	// Provider's .Name() because wrappers like NewRetry add suffixes
	// ("+retry") that would otherwise miss the cache when the spec
	// spells the same provider in its bare form. The model id is NOT
	// part of the cache key — Providers are connection pools that
	// happily multiplex calls for different models, and we want two
	// roles pointed at the same backend to share one pool.
	cache := map[string]provider.Provider{
		defaultName: defaultProv,
	}

	for _, role := range []string{runner.RolePlanner, runner.RoleEditor, runner.RoleMain} {
		sel, ok := selections[role]
		if !ok {
			// Spec didn't pin this role → fall back to main.
			continue
		}

		// Resolve the model id: when sel.Model is empty, inherit from main.
		modelID := sel.Model
		if modelID == "" {
			modelID = defaultModel
		}

		// Resolve the provider: when sel.Provider is empty, reuse the
		// default. Otherwise call the factory through the cache so two
		// roles with the same provider name share one instance.
		provName := sel.Provider
		if provName == "" {
			provName = defaultName
		}

		if cached, ok := cache[provName]; ok {
			providers[role] = cached
			models[role] = modelID
			continue
		}

		// Different provider name → construct via factory + retry wrap.
		newProv, _, err := factory(provName)
		if err != nil {
			return providers, models, err
		}
		newProv = provider.NewRetry(newProv)
		cache[provName] = newProv
		providers[role] = newProv
		models[role] = modelID
	}

	return providers, models, nil
}
