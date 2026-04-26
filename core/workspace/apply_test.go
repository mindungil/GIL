package workspace

import (
	"testing"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestApplyDefaults_FillsEmptySpec(t *testing.T) {
	spec := &gilv1.FrozenSpec{}
	cfg := Config{
		Provider:         "anthropic",
		Model:            "claude-sonnet-4-6",
		WorkspaceBackend: "DOCKER",
		Autonomy:         "ASK_DESTRUCTIVE_ONLY",
	}
	out := ApplyDefaults(spec, cfg)
	require.Same(t, spec, out)

	require.Equal(t, "anthropic", spec.Models.Main.Provider)
	require.Equal(t, "claude-sonnet-4-6", spec.Models.Main.ModelId)
	require.Equal(t, gilv1.WorkspaceBackend_DOCKER, spec.Workspace.Backend)
	require.Equal(t, gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY, spec.Risk.Autonomy)
}

func TestApplyDefaults_PreservesExistingValues(t *testing.T) {
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			Main: &gilv1.ModelChoice{Provider: "openai", ModelId: "gpt-5"},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_SSH},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
	}
	cfg := Config{
		Provider:         "anthropic",
		Model:            "claude-sonnet-4-6",
		WorkspaceBackend: "DOCKER",
		Autonomy:         "PLAN_ONLY",
	}
	ApplyDefaults(spec, cfg)

	require.Equal(t, "openai", spec.Models.Main.Provider, "interview-set provider must win")
	require.Equal(t, "gpt-5", spec.Models.Main.ModelId)
	require.Equal(t, gilv1.WorkspaceBackend_SSH, spec.Workspace.Backend)
	require.Equal(t, gilv1.AutonomyDial_FULL, spec.Risk.Autonomy)
}

func TestApplyDefaults_PartialOverlap(t *testing.T) {
	// Spec has provider but no model — config should fill in only the model.
	spec := &gilv1.FrozenSpec{
		Models: &gilv1.ModelConfig{
			Main: &gilv1.ModelChoice{Provider: "anthropic"},
		},
	}
	cfg := Config{Provider: "openai", Model: "claude-sonnet-4-6"}
	ApplyDefaults(spec, cfg)
	require.Equal(t, "anthropic", spec.Models.Main.Provider)
	require.Equal(t, "claude-sonnet-4-6", spec.Models.Main.ModelId)
}

func TestApplyDefaults_NilSpec(t *testing.T) {
	require.Nil(t, ApplyDefaults(nil, Defaults()))
}

func TestApplyDefaults_UnknownEnumIgnored(t *testing.T) {
	// A typo in the TOML must not panic or set garbage; it just leaves
	// the field at its zero value.
	spec := &gilv1.FrozenSpec{}
	cfg := Config{
		WorkspaceBackend: "BANANA",
		Autonomy:         "MAYBE",
	}
	ApplyDefaults(spec, cfg)
	// Workspace was created (cfg.WorkspaceBackend non-empty triggers
	// the lazy init) but Backend stays UNSPECIFIED because BANANA is
	// not a known enum value.
	require.NotNil(t, spec.Workspace)
	require.Equal(t, gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED, spec.Workspace.Backend)
	require.NotNil(t, spec.Risk)
	require.Equal(t, gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED, spec.Risk.Autonomy)
}

func TestApplyDefaults_DefaultsAlone(t *testing.T) {
	spec := &gilv1.FrozenSpec{}
	ApplyDefaults(spec, Defaults())
	// Defaults set backend = LOCAL_NATIVE, autonomy = FULL.
	require.Equal(t, gilv1.WorkspaceBackend_LOCAL_NATIVE, spec.Workspace.Backend)
	require.Equal(t, gilv1.AutonomyDial_FULL, spec.Risk.Autonomy)
	// No provider/model in defaults.
	require.Nil(t, spec.Models)
}
