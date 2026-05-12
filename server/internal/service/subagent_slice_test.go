package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// subagent_slice_test.go — S4 invariants: inherit-all default,
// per-slot inheritance gates, override precedence, budget ⅓ rule.

func TestSliceSpec_NilPolicyInheritsAll(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: "/work"},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}},
		Tools:     &gilv1.Tools{Bash: true, FileOps: true},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "test", Kind: gilv1.CheckKind_SHELL, Command: "go test ./..."}},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"go"}},
		Budget:      &gilv1.Budget{MaxIterations: 30, MaxTotalTokens: 300_000, MaxTotalCostUsd: 3.0},
	}
	child := sliceSpec(subagentSliceInput{
		childSessionID: "child-1",
		parent:         parent,
		policy:         nil,
	})

	require.NotNil(t, child.Workspace)
	require.Equal(t, "/work", child.Workspace.Path)
	require.NotNil(t, child.Models)
	require.Equal(t, "claude-opus-4-7", child.Models.Main.ModelId)
	require.True(t, child.Tools.Bash)
	require.Len(t, child.Verification.Checks, 1)
	require.Equal(t, []string{"go"}, child.Constraints.TechStack)
	// Budget defaults to parent/3 — ints round down, must be ≥ 1.
	require.Equal(t, int32(10), child.Budget.MaxIterations)
	require.InDelta(t, 1.0, child.Budget.MaxTotalCostUsd, 1e-9)
}

func TestSliceSpec_PerSlotGates(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Workspace: &gilv1.Workspace{Path: "/work"},
		Tools:     &gilv1.Tools{Bash: true},
	}
	// Non-empty policy with inherit_workspace=true ONLY.
	policy := &gilv1.SubagentPolicy{InheritWorkspace: true}
	child := sliceSpec(subagentSliceInput{parent: parent, policy: policy})

	require.NotNil(t, child.Workspace, "workspace inherited per gate")
	require.Nil(t, child.Tools, "tools not inherited when gate is false")
}

func TestSliceSpec_OverrideWorkspaceWinsOverInherit(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Workspace: &gilv1.Workspace{Path: "/parent", Backend: gilv1.WorkspaceBackend_DOCKER},
	}
	child := sliceSpec(subagentSliceInput{
		parent:                parent,
		policy:                nil, // inherit-all
		overrideWorkspacePath: "/child/.subagent/explore",
	})
	require.Equal(t, "/child/.subagent/explore", child.Workspace.Path)
	require.Equal(t, gilv1.WorkspaceBackend_DOCKER, child.Workspace.Backend,
		"backend still inherits when only path overridden")
}

func TestSliceSpec_ToolsAllowlistOverridesInherited(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Tools: &gilv1.Tools{Bash: true, FileOps: true, WebSearch: true},
	}
	child := sliceSpec(subagentSliceInput{
		parent:             parent,
		policy:             nil,
		overrideToolsAllow: []string{"file_ops", "grep_mcp"},
	})
	require.True(t, child.Tools.FileOps)
	require.False(t, child.Tools.Bash)
	require.False(t, child.Tools.WebSearch)
	require.Contains(t, child.Tools.McpServers, "grep_mcp", "unknown name routes to MCP allowlist")
}

func TestSliceSpec_OverrideMaxIterationsWins(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Budget: &gilv1.Budget{MaxIterations: 30},
	}
	child := sliceSpec(subagentSliceInput{
		parent:                parent,
		overrideMaxIterations: 5,
	})
	require.Equal(t, int32(5), child.Budget.MaxIterations)
}

func TestSliceSpec_BudgetThirdRuleHasFloor(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Budget: &gilv1.Budget{MaxIterations: 2}, // 2/3 = 0 → must round up to 1
	}
	child := sliceSpec(subagentSliceInput{parent: parent})
	require.Equal(t, int32(1), child.Budget.MaxIterations, "tiny parent budget rounds up to 1 iter")
}

func TestSliceSpec_PolicyValueOverridesThirdRule(t *testing.T) {
	parent := &gilv1.FrozenSpec{
		Budget: &gilv1.Budget{MaxIterations: 30},
	}
	policy := &gilv1.SubagentPolicy{MaxIterationsPerSubagent: 7}
	child := sliceSpec(subagentSliceInput{parent: parent, policy: policy})
	require.Equal(t, int32(7), child.Budget.MaxIterations)
}

func TestSliceSpec_NoParentBudgetNoChildBudget(t *testing.T) {
	parent := &gilv1.FrozenSpec{}
	child := sliceSpec(subagentSliceInput{parent: parent})
	require.Nil(t, child.Budget, "no parent budget + no policy → leave RunService to apply defaults")
}
