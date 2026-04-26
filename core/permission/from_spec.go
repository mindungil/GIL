package permission

import (
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// FromAutonomy returns the Evaluator that corresponds to a frozen spec's
// RiskProfile.Autonomy level. Returns nil for FULL (and UNSPECIFIED, which
// preserves Phase 5/6 behavior of unrestricted runs).
//
// Levels:
//
//	FULL / UNSPECIFIED   → nil (no gating; all allowed)
//	ASK_DESTRUCTIVE_ONLY → deny destructive bash patterns; allow rest
//	ASK_PER_ACTION       → allow only read-only ops + memory_load + repomap;
//	                       everything else falls through to default Ask (which
//	                       becomes Deny in Phase 7 non-interactive mode)
//	PLAN_ONLY            → deny all tools (effectively halts execution)
func FromAutonomy(autonomy gilv1.AutonomyDial) *Evaluator {
	switch autonomy {
	case gilv1.AutonomyDial_FULL, gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED:
		return nil

	case gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY:
		// Order matters: catch-all allow first, destructive denies LAST so they win.
		return &Evaluator{Rules: append(
			// Allow everything by default
			[]Rule{{Tool: "*", Key: "*", Action: DecisionAllow}},
			// Destructive denies (last-wins)
			destructiveBashRules()...,
		)}

	case gilv1.AutonomyDial_ASK_PER_ACTION:
		// Allow only safe read-only ops; everything else falls through to default Ask.
		return &Evaluator{Rules: []Rule{
			{Tool: "read_file", Key: "*", Action: DecisionAllow},
			{Tool: "memory_load", Key: "*", Action: DecisionAllow},
			{Tool: "repomap", Key: "*", Action: DecisionAllow},
			{Tool: "compact_now", Key: "*", Action: DecisionAllow},
		}}

	case gilv1.AutonomyDial_PLAN_ONLY:
		return &Evaluator{Rules: []Rule{
			{Tool: "*", Key: "*", Action: DecisionDeny},
		}}
	}
	// Unknown autonomy values default to FULL (backwards-compat for newly
	// added enum values that older builds don't recognize).
	return nil
}

// destructiveBashRules denies bash invocations matching dangerous patterns.
// Glob patterns use the wildcard semantics from wildcard.go.
func destructiveBashRules() []Rule {
	patterns := []string{
		"rm *",
		"rm",
		"mv *",
		"chmod *",
		"chown *",
		"dd *",
		"mkfs*",
		"sudo *",
		"*>* /*",
		"*> /*",
	}
	out := make([]Rule, len(patterns))
	for i, p := range patterns {
		out[i] = Rule{Tool: "bash", Key: p, Action: DecisionDeny}
	}
	return out
}
