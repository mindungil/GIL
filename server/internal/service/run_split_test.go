package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/runner"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// stubProvider is a no-op Provider whose Name() returns the configured
// string. Used purely to identify which provider instance ended up in
// the per-role map.
type stubProvider struct{ name string }

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{}, nil
}

func TestBuildRoleProviders_OnlyMain(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "anthropic"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"},
		},
	}
	factory := func(name string) (provider.Provider, string, error) {
		t.Fatalf("factory should not be called when spec only pins main; got %q", name)
		return nil, "", nil
	}

	provs, models, err := buildRoleProviders(spec, factory, defaultProv, "claude-opus-4-7", "anthropic")
	require.NoError(t, err)

	// Only main is wired (planner/editor fall back to .Provider/.Model on the runner side).
	require.Equal(t, defaultProv, provs[runner.RoleMain])
	require.Equal(t, "claude-opus-4-7", models[runner.RoleMain])
	require.NotContains(t, provs, runner.RolePlanner)
	require.NotContains(t, provs, runner.RoleEditor)
}

func TestBuildRoleProviders_ArchitectCoderSplit(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "openai"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			Main:    &gilv1.ModelChoice{Provider: "openai", ModelId: "gpt-4o"},
			Planner: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"},
			Editor:  &gilv1.ModelChoice{Provider: "vllm", ModelId: "qwen-27b"},
		},
	}
	calls := map[string]int{}
	factory := func(name string) (provider.Provider, string, error) {
		calls[name]++
		switch name {
		case "anthropic":
			return &stubProvider{name: "anthropic"}, "claude-opus-4-7", nil
		case "vllm":
			return &stubProvider{name: "vllm"}, "qwen-27b", nil
		}
		return nil, "", errors.New("unexpected provider " + name)
	}

	provs, models, err := buildRoleProviders(spec, factory, defaultProv, "gpt-4o", "openai")
	require.NoError(t, err)

	require.Equal(t, defaultProv, provs[runner.RoleMain], "main should reuse the default")
	require.Equal(t, "gpt-4o", models[runner.RoleMain])

	require.NotEqual(t, defaultProv, provs[runner.RolePlanner], "planner should be a different instance")
	// NewRetry wraps the factory's return value, so .Name() carries the
	// "+retry" suffix.
	require.Equal(t, "anthropic+retry", provs[runner.RolePlanner].Name())
	require.Equal(t, "claude-opus-4-7", models[runner.RolePlanner])

	require.NotEqual(t, defaultProv, provs[runner.RoleEditor], "editor should be a different instance")
	require.Equal(t, "vllm+retry", provs[runner.RoleEditor].Name())
	require.Equal(t, "qwen-27b", models[runner.RoleEditor])

	require.Equal(t, 1, calls["anthropic"], "anthropic factory should be called once")
	require.Equal(t, 1, calls["vllm"], "vllm factory should be called once")
}

func TestBuildRoleProviders_SharesProviderForSameBackend(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "openai"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			// Planner + editor BOTH point at anthropic but with
			// different model ids. The factory should be called only
			// once for "anthropic" — the second hit should reuse the
			// first instance because the backend is the same; only the
			// per-request model id differs.
			Planner: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"},
			Editor:  &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-haiku-4-5"},
		},
	}
	calls := 0
	factory := func(name string) (provider.Provider, string, error) {
		calls++
		require.Equal(t, "anthropic", name)
		return &stubProvider{name: "anthropic"}, "claude-default", nil
	}

	provs, models, err := buildRoleProviders(spec, factory, defaultProv, "gpt-4o", "openai")
	require.NoError(t, err)

	// Different model ids but same Provider instance — connection pool
	// is shared.
	require.Equal(t, provs[runner.RolePlanner], provs[runner.RoleEditor],
		"planner + editor should share one Provider when same backend")
	require.Equal(t, "claude-opus-4-7", models[runner.RolePlanner])
	require.Equal(t, "claude-haiku-4-5", models[runner.RoleEditor])
	require.Equal(t, 1, calls, "factory should be called once per unique backend")
}

func TestBuildRoleProviders_PlannerReusesDefaultWhenSameBackend(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "anthropic"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			// Planner pins anthropic (same as default) with a different
			// model. Should reuse the default Provider instance — no
			// factory call required.
			Planner: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"},
		},
	}
	factory := func(name string) (provider.Provider, string, error) {
		t.Fatalf("factory should not be called when role reuses the default backend; got %q", name)
		return nil, "", nil
	}

	provs, models, err := buildRoleProviders(spec, factory, defaultProv, "claude-haiku-4-5", "anthropic")
	require.NoError(t, err)

	require.Equal(t, defaultProv, provs[runner.RolePlanner])
	require.Equal(t, "claude-opus-4-7", models[runner.RolePlanner])
}

func TestBuildRoleProviders_FactoryError_PropagatesUp(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "openai"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			Planner: &gilv1.ModelChoice{Provider: "typo-provider", ModelId: "x"},
		},
	}
	factory := func(name string) (provider.Provider, string, error) {
		return nil, "", errors.New("unknown provider: " + name)
	}

	_, _, err := buildRoleProviders(spec, factory, defaultProv, "gpt-4o", "openai")
	require.Error(t, err)
	require.Contains(t, err.Error(), "typo-provider")
}

func TestBuildRoleProviders_NilSpec(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "anthropic"}
	provs, models, err := buildRoleProviders(nil, nil, defaultProv, "claude", "anthropic")
	require.NoError(t, err)
	require.Equal(t, defaultProv, provs[runner.RoleMain])
	require.Equal(t, "claude", models[runner.RoleMain])
}

func TestBuildRoleProviders_EmptyProviderFieldFallsBackToDefault(t *testing.T) {
	t.Parallel()

	defaultProv := &stubProvider{name: "anthropic"}
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			// Provider field empty → reuse default Provider but with
			// a different model id.
			Planner: &gilv1.ModelChoice{ModelId: "claude-opus-4-7"},
		},
	}
	factory := func(name string) (provider.Provider, string, error) {
		t.Fatalf("factory should not be called when provider field is empty; got %q", name)
		return nil, "", nil
	}

	provs, models, err := buildRoleProviders(spec, factory, defaultProv, "claude-haiku-4-5", "anthropic")
	require.NoError(t, err)

	require.Equal(t, defaultProv, provs[runner.RolePlanner])
	require.Equal(t, "claude-opus-4-7", models[runner.RolePlanner])
}
