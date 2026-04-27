package permission

import (
	"testing"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/stretchr/testify/require"
)

func TestFromAutonomy_FullReturnsNil(t *testing.T) {
	require.Nil(t, FromAutonomy(gilv1.AutonomyDial_FULL))
	require.Nil(t, FromAutonomy(gilv1.AutonomyDial_AUTONOMY_UNSPECIFIED))
}

func TestFromAutonomy_DestructiveOnly_DeniesRm(t *testing.T) {
	e := FromAutonomy(gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY)
	require.NotNil(t, e)
	require.Equal(t, DecisionDeny, e.Evaluate("bash", "rm -rf /"))
	require.Equal(t, DecisionDeny, e.Evaluate("bash", "sudo apt install git"))
	require.Equal(t, DecisionAllow, e.Evaluate("bash", "ls -la"))
	require.Equal(t, DecisionAllow, e.Evaluate("bash", "echo hi"))
}

func TestFromAutonomy_DestructiveOnly_AllowsMostThings(t *testing.T) {
	e := FromAutonomy(gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY)
	require.Equal(t, DecisionAllow, e.Evaluate("write_file", "x.txt"))
	require.Equal(t, DecisionAllow, e.Evaluate("read_file", "x.txt"))
	require.Equal(t, DecisionAllow, e.Evaluate("memory_update", "progress"))
	require.Equal(t, DecisionAllow, e.Evaluate("apply_patch", ""))
}

func TestFromAutonomy_AskPerAction_AllowsReadOnly(t *testing.T) {
	e := FromAutonomy(gilv1.AutonomyDial_ASK_PER_ACTION)
	require.Equal(t, DecisionAllow, e.Evaluate("read_file", "x"))
	require.Equal(t, DecisionAllow, e.Evaluate("memory_load", "progress"))
	require.Equal(t, DecisionAllow, e.Evaluate("repomap", ""))
	require.Equal(t, DecisionAllow, e.Evaluate("compact_now", ""))
	// lsp is read-only at the operation layer (rename returns edits but
	// doesn't apply them, so the actual write is gated separately).
	require.Equal(t, DecisionAllow, e.Evaluate("lsp", ""))
	// Everything else → Ask (which becomes Deny in non-interactive Phase 7)
	require.Equal(t, DecisionAsk, e.Evaluate("bash", "ls"))
	require.Equal(t, DecisionAsk, e.Evaluate("write_file", "x"))
	require.Equal(t, DecisionAsk, e.Evaluate("memory_update", "progress"))
}

func TestFromAutonomy_PlanOnly_DeniesWrites_AllowsReadAndPlan(t *testing.T) {
	// PLAN_ONLY blocks writes (bash/edit/write_file/apply_patch/exec)
	// but allows the read-only investigation tools and the plan tool
	// itself so the agent can deliver "read the codebase + write a
	// plan + exit". lsp + web_fetch are also allowed (both read-only).
	// web_search stays denied (paid backend, not authorized).
	e := FromAutonomy(gilv1.AutonomyDial_PLAN_ONLY)
	require.Equal(t, DecisionDeny, e.Evaluate("bash", "ls"))
	require.Equal(t, DecisionDeny, e.Evaluate("write_file", "x"))
	require.Equal(t, DecisionDeny, e.Evaluate("edit", ""))
	require.Equal(t, DecisionDeny, e.Evaluate("apply_patch", ""))
	// Read-only / planning tools allowed.
	require.Equal(t, DecisionAllow, e.Evaluate("plan", ""))
	require.Equal(t, DecisionAllow, e.Evaluate("read_file", "main.go"))
	require.Equal(t, DecisionAllow, e.Evaluate("memory_load", "progress"))
	require.Equal(t, DecisionAllow, e.Evaluate("repomap", ""))
	require.Equal(t, DecisionAllow, e.Evaluate("lsp", ""))
	// web_fetch is research-grade read-only and useful for planning;
	// web_search is denied (paid backend, not authorized for plan-only).
	require.Equal(t, DecisionAllow, e.Evaluate("web_fetch", "https://x"))
	require.Equal(t, DecisionDeny, e.Evaluate("web_search", "anything"))
}

func TestFromAutonomy_DestructiveOnly_AllowsWebTools(t *testing.T) {
	// At ASK_DESTRUCTIVE_ONLY web_* are read-only verbs, allowed.
	e := FromAutonomy(gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY)
	require.Equal(t, DecisionAllow, e.Evaluate("web_fetch", "https://x"))
	require.Equal(t, DecisionAllow, e.Evaluate("web_search", "go testing"))
}

func TestFromAutonomy_AskPerAction_AsksWebTools(t *testing.T) {
	// At ASK_PER_ACTION the user wants to confirm every outgoing
	// network request, so web_* fall through to default Ask.
	e := FromAutonomy(gilv1.AutonomyDial_ASK_PER_ACTION)
	require.Equal(t, DecisionAsk, e.Evaluate("web_fetch", "https://x"))
	require.Equal(t, DecisionAsk, e.Evaluate("web_search", "x"))
}

func TestFromAutonomy_DestructiveBashPatterns(t *testing.T) {
	e := FromAutonomy(gilv1.AutonomyDial_ASK_DESTRUCTIVE_ONLY)
	cases := map[string]Decision{
		"rm hello.txt":         DecisionDeny,
		"rm -rf /":             DecisionDeny,
		"mv a b":               DecisionDeny,
		"chmod 777 file":       DecisionDeny,
		"chown root file":      DecisionDeny,
		"dd if=/dev/zero of=x": DecisionDeny,
		"mkfs.ext4 /dev/sda":   DecisionDeny,
		"sudo rm hi":           DecisionDeny,
		"ls":                   DecisionAllow,
		"echo hi":              DecisionAllow,
		"git status":           DecisionAllow,
		"go test ./...":        DecisionAllow,
	}
	for cmd, want := range cases {
		got := e.Evaluate("bash", cmd)
		require.Equal(t, want, got, "command=%q", cmd)
	}
}
