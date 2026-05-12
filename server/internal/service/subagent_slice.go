package service

import (
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// subagent_slice.go — S4 of docs/design/subagent.md. Given a parent
// FrozenSpec + SubagentPolicy + agent-provided overrides, derive the
// child FrozenSpec the new subagent runs against.
//
// Rules (per design §3.1):
//   - Empty SubagentPolicy = inherit everything from parent. We treat
//     a fully-zero policy struct as "inherit all" so the agent doesn't
//     need to fill in true/true/true/... explicitly.
//   - inherit_* booleans gate slot copy. When false the child's
//     corresponding slot stays nil so RunService.Start falls back to
//     the layered workspace-config default.
//   - max_*_per_subagent overrides become the child's Budget.
//   - Agent-provided overrides (spec_override.workspace_path,
//     tools_allowlist, max_iterations) win over both inherit-rules and
//     per-subagent caps.

// subagentSliceInput is what spawn_agent assembles before calling.
type subagentSliceInput struct {
	childSessionID string
	parent         *gilv1.FrozenSpec
	policy         *gilv1.SubagentPolicy

	// Overrides — agent passed these in spawn_agent's spec_override
	// object. Empty values are ignored (inherit-rules apply).
	overrideWorkspacePath string
	overrideToolsAllow    []string
	overrideMaxIterations int32
}

// sliceSpec produces the child's FrozenSpec. parent must be non-nil
// (the spawn_agent tool gates on the parent being frozen before
// reaching here). policy may be nil — treated as "inherit-all".
func sliceSpec(in subagentSliceInput) *gilv1.FrozenSpec {
	child := &gilv1.FrozenSpec{
		SpecId:    ulid.Make().String(),
		SessionId: in.childSessionID,
		FrozenAt:  timestamppb.New(time.Now().UTC()),
	}

	// Goal is always sliced — the subagent has its own per-spawn task
	// message that becomes the seed. spawn_agent fills child.Goal after
	// sliceSpec returns (we don't have the task text here).

	pol := in.policy
	inheritAll := pol == nil || isPolicyEmpty(pol)

	if (inheritAll || pol.InheritWorkspace) && in.parent.Workspace != nil {
		ws := *in.parent.Workspace
		if in.overrideWorkspacePath != "" {
			ws.Path = in.overrideWorkspacePath
		}
		child.Workspace = &ws
	} else if in.overrideWorkspacePath != "" {
		child.Workspace = &gilv1.Workspace{Path: in.overrideWorkspacePath}
	}

	if (inheritAll || pol.InheritModels) && in.parent.Models != nil {
		// shallow copy is enough — ModelChoice is a flat message.
		m := *in.parent.Models
		child.Models = &m
	}

	if (inheritAll || pol.InheritTools) && in.parent.Tools != nil {
		t := *in.parent.Tools
		child.Tools = &t
	}
	if len(in.overrideToolsAllow) > 0 {
		// V1: tools_allowlist trims the MCP servers list to the named
		// subset. Boolean tool gates (bash/file_ops/etc.) require
		// explicit name match — for V1 we treat unknown names as a
		// signal to disable everything not listed.
		child.Tools = applyToolsAllowlist(child.Tools, in.overrideToolsAllow)
	}

	if (inheritAll || pol.InheritVerification) && in.parent.Verification != nil {
		v := *in.parent.Verification
		child.Verification = &v
	}
	if (inheritAll || pol.InheritConstraints) && in.parent.Constraints != nil {
		c := *in.parent.Constraints
		child.Constraints = &c
	}

	// Risk profile: subagents inherit autonomy from parent. Memory:
	// "사람 안 부르는 설계" — child shouldn't be more cautious than
	// parent, since the goal is autonomous execution.
	if in.parent.Risk != nil {
		r := *in.parent.Risk
		child.Risk = &r
	}

	// Budget: per-subagent caps go in. If policy has zero values,
	// default to parent.Budget / 3 so a runaway child can't burn the
	// entire root budget. Override (overrideMaxIterations) wins.
	child.Budget = childBudget(in.parent.Budget, pol, in.overrideMaxIterations)

	return child
}

// isPolicyEmpty reports whether every field is zero — the "inherit all"
// sentinel. We can't just check pol == nil because the agent may pass
// an empty {} struct.
func isPolicyEmpty(p *gilv1.SubagentPolicy) bool {
	return !p.InheritWorkspace &&
		!p.InheritModels &&
		!p.InheritTools &&
		!p.InheritVerification &&
		!p.InheritConstraints &&
		p.MaxIterationsPerSubagent == 0 &&
		p.MaxTokensPerSubagent == 0 &&
		p.MaxCostPerSubagentUsd == 0
}

// applyToolsAllowlist returns a Tools message keyed to names present in
// allow. Recognised names map to boolean gates; unknown names go into
// mcp_servers verbatim so the subagent registry can re-filter at run
// time.
func applyToolsAllowlist(base *gilv1.Tools, allow []string) *gilv1.Tools {
	allowSet := make(map[string]struct{}, len(allow))
	for _, n := range allow {
		allowSet[n] = struct{}{}
	}
	t := &gilv1.Tools{}
	_, t.Bash = allowSet["bash"]
	_, t.FileOps = allowSet["file_ops"]
	_, t.WebSearch = allowSet["web_search"]
	_, t.WebFetch = allowSet["web_fetch"]
	_, t.Repomap = allowSet["repomap"]
	_, t.ExecCode = allowSet["exec_code"]
	// Preserve mcp_servers from the base (allowlist is for tools, not
	// MCP) and add any unrecognised names that look like MCP server
	// references.
	if base != nil {
		t.McpServers = base.McpServers
	}
	known := map[string]struct{}{
		"bash": {}, "file_ops": {}, "web_search": {}, "web_fetch": {},
		"repomap": {}, "exec_code": {},
	}
	for _, n := range allow {
		if _, isKnown := known[n]; !isKnown {
			t.McpServers = append(t.McpServers, n)
		}
	}
	return t
}

// childBudget computes the subagent's per-run budget.
//
//   - override wins if non-zero.
//   - policy fields win over the default ⅓-of-parent rule.
//   - missing parent.Budget → child Budget stays nil (RunService.Start
//     applies its own defaults / unbounded).
func childBudget(parent *gilv1.Budget, pol *gilv1.SubagentPolicy, overrideMaxIters int32) *gilv1.Budget {
	if parent == nil && pol == nil && overrideMaxIters == 0 {
		return nil
	}
	b := &gilv1.Budget{}

	// max_iterations
	switch {
	case overrideMaxIters > 0:
		b.MaxIterations = overrideMaxIters
	case pol != nil && pol.MaxIterationsPerSubagent > 0:
		b.MaxIterations = int32(pol.MaxIterationsPerSubagent)
	case parent != nil && parent.MaxIterations > 0:
		b.MaxIterations = parent.MaxIterations / 3
		if b.MaxIterations < 1 {
			b.MaxIterations = 1
		}
	}

	// max_tokens
	switch {
	case pol != nil && pol.MaxTokensPerSubagent > 0:
		b.MaxTotalTokens = pol.MaxTokensPerSubagent
	case parent != nil && parent.MaxTotalTokens > 0:
		b.MaxTotalTokens = parent.MaxTotalTokens / 3
	}

	// max_cost_usd
	switch {
	case pol != nil && pol.MaxCostPerSubagentUsd > 0:
		b.MaxTotalCostUsd = pol.MaxCostPerSubagentUsd
	case parent != nil && parent.MaxTotalCostUsd > 0:
		b.MaxTotalCostUsd = parent.MaxTotalCostUsd / 3
	}

	return b
}
