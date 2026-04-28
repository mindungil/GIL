package runner

import (
	"fmt"

	"github.com/mindungil/gil/core/compact"
	"github.com/mindungil/gil/core/provider"
	specpb "github.com/mindungil/gil/proto/gen/gil/v1"
)

// NewCompactorFromSpec builds a production-ready Compactor from the
// frozen spec's ModelConfig and the provider registry available to the
// run loop.
//
// Model selection: prefer Weak (cost-efficient summarizer); fall back
// to Main if Weak is unset. Returns an error when no usable model or
// the chosen model's provider is missing from the registry.
func NewCompactorFromSpec(models *specpb.ModelConfig, providers map[string]provider.Provider) (*compact.Compactor, error) {
	if models == nil {
		return nil, fmt.Errorf("NewCompactorFromSpec: spec has no Models")
	}
	choice := models.GetWeak()
	if choice == nil || choice.GetModelId() == "" {
		choice = models.GetMain()
	}
	if choice == nil || choice.GetModelId() == "" {
		return nil, fmt.Errorf("NewCompactorFromSpec: no model configured (neither Weak nor Main)")
	}
	// providerID may be "" when the workspace config.toml set a model string
	// but no provider; in that case the caller should register "" as a
	// catch-all key in the providers map so the lookup below succeeds.
	providerID := choice.GetProvider()
	p, ok := providers[providerID]
	if !ok {
		return nil, fmt.Errorf("NewCompactorFromSpec: provider %q not in registry", providerID)
	}
	return &compact.Compactor{
		Provider:  p,
		Model:     choice.GetModelId(),
		HeadKeep:  2,
		TailKeep:  6,
		MinMiddle: 8,
		History:   &compact.History{},
	}, nil
}
