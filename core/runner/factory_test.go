package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
	specpb "github.com/mindungil/gil/proto/gen/gil/v1"
)

// fakeProvider implements provider.Provider just enough for the factory.
// The factory only needs the registry to contain the matching provider id;
// no Complete() call is exercised by these tests.
type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake" }
func (fakeProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	return provider.Response{}, nil
}

func (v fakeProvider) StreamComplete(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	return v.Complete(ctx, req)
}

func TestNewCompactorFromSpec_PrefersWeakModel(t *testing.T) {
	models := &specpb.ModelConfig{
		Weak: &specpb.ModelChoice{Provider: "anthropic", ModelId: "haiku"},
		Main: &specpb.ModelChoice{Provider: "anthropic", ModelId: "opus"},
	}
	providers := map[string]provider.Provider{"anthropic": fakeProvider{}}
	c, err := NewCompactorFromSpec(models, providers)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, "haiku", c.Model)
}

func TestNewCompactorFromSpec_FallsBackToMainWhenNoWeak(t *testing.T) {
	models := &specpb.ModelConfig{
		Main: &specpb.ModelChoice{Provider: "anthropic", ModelId: "opus"},
	}
	providers := map[string]provider.Provider{"anthropic": fakeProvider{}}
	c, err := NewCompactorFromSpec(models, providers)
	require.NoError(t, err)
	require.Equal(t, "opus", c.Model)
}

func TestNewCompactorFromSpec_ErrorsWhenProviderMissing(t *testing.T) {
	models := &specpb.ModelConfig{
		Main: &specpb.ModelChoice{Provider: "anthropic", ModelId: "opus"},
	}
	providers := map[string]provider.Provider{} // empty
	_, err := NewCompactorFromSpec(models, providers)
	require.Error(t, err)
}

func TestNewCompactorFromSpec_ErrorsWhenNoModel(t *testing.T) {
	_, err := NewCompactorFromSpec(&specpb.ModelConfig{}, map[string]provider.Provider{})
	require.Error(t, err)
	_, err = NewCompactorFromSpec(nil, map[string]provider.Provider{})
	require.Error(t, err)
}

func TestNewCompactorFromSpec_EmptyProviderUsedAsCatchAll(t *testing.T) {
	// workspace.ApplyDefaults can leave Provider blank when config.toml
	// sets only a model string. The factory accepts this if the caller
	// has registered a "" catch-all in the providers map.
	models := &specpb.ModelConfig{
		Main: &specpb.ModelChoice{Provider: "", ModelId: "haiku"},
	}
	providers := map[string]provider.Provider{"": fakeProvider{}}
	c, err := NewCompactorFromSpec(models, providers)
	require.NoError(t, err)
	require.Equal(t, "haiku", c.Model)
}

func TestNewCompactorFromSpec_EmptyProviderRejectsWithoutCatchAll(t *testing.T) {
	// Same setup but no "" key registered → must error.
	models := &specpb.ModelConfig{
		Main: &specpb.ModelChoice{Provider: "", ModelId: "haiku"},
	}
	providers := map[string]provider.Provider{"anthropic": fakeProvider{}}
	_, err := NewCompactorFromSpec(models, providers)
	require.Error(t, err)
}
