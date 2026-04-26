package permission

import (
	"testing"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
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
	// Everything else → Ask (which becomes Deny in non-interactive Phase 7)
	require.Equal(t, DecisionAsk, e.Evaluate("bash", "ls"))
	require.Equal(t, DecisionAsk, e.Evaluate("write_file", "x"))
	require.Equal(t, DecisionAsk, e.Evaluate("memory_update", "progress"))
}

func TestFromAutonomy_PlanOnly_DeniesAll(t *testing.T) {
	e := FromAutonomy(gilv1.AutonomyDial_PLAN_ONLY)
	require.Equal(t, DecisionDeny, e.Evaluate("bash", "ls"))
	require.Equal(t, DecisionDeny, e.Evaluate("read_file", "x"))
	require.Equal(t, DecisionDeny, e.Evaluate("write_file", "x"))
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
