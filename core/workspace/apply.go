package workspace

import (
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// ApplyDefaults fills empty fields on a FrozenSpec from a resolved
// Config. It NEVER overrides values that are already set: the spec the
// interview / freeze stage produced is the source of truth, and this
// helper only paints over fields the interview chose to leave blank.
//
// Concretely:
//
//   - spec.Models.Main.Provider / ModelId : populated only when both
//     the field on the spec and the corresponding entry on Config are
//     non-empty.
//   - spec.Workspace.Backend               : populated only when the
//     spec is BACKEND_UNSPECIFIED.
//   - spec.Risk.Autonomy                   : populated only when the
//     spec is AUTONOMY_UNSPECIFIED.
//
// IgnoreGlobs is intentionally not applied to spec — the spec proto
// has no field for it today. The repomap tool reads its own filter
// list separately, and that integration is left for a follow-up
// (Track A / instructions package or a dedicated repomap config
// hookup), so the helper here has standalone value either way.
//
// The method returns the spec it was given so callers can compose:
//
//	cfg, _ := workspace.Resolve(globalPath, projectPath)
//	spec   = workspace.ApplyDefaults(spec, cfg)
//
// Passing a nil spec returns nil unchanged.
func ApplyDefaults(spec *gilv1.FrozenSpec, cfg Config) *gilv1.FrozenSpec {
	if spec == nil {
		return nil
	}

	// Models.Main provider / model.
	if cfg.Provider != "" || cfg.Model != "" {
		if spec.Models == nil {
			spec.Models = &gilv1.ModelConfig{}
		}
		if spec.Models.Main == nil {
			spec.Models.Main = &gilv1.ModelChoice{}
		}
		if spec.Models.Main.Provider == "" && cfg.Provider != "" {
			spec.Models.Main.Provider = cfg.Provider
		}
		if spec.Models.Main.ModelId == "" && cfg.Model != "" {
			spec.Models.Main.ModelId = cfg.Model
		}
	}

	// Workspace backend.
	if cfg.WorkspaceBackend != "" {
		if spec.Workspace == nil {
			spec.Workspace = &gilv1.Workspace{}
		}
		if spec.Workspace.Backend == gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED {
			if v, ok := gilv1.WorkspaceBackend_value[cfg.WorkspaceBackend]; ok {
				spec.Workspace.Backend = gilv1.WorkspaceBackend(v)
			}
		}
	}

	// Risk autonomy.
	if cfg.Autonomy != "" {
		if spec.Risk == nil {
			spec.Risk = &gilv1.RiskProfile{}
		}
		if spec.Risk.Autonomy == gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED {
			if v, ok := gilv1.AutonomyDial_value[cfg.Autonomy]; ok {
				spec.Risk.Autonomy = gilv1.AutonomyDial(v)
			}
		}
	}

	return spec
}
