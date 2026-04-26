package interview

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestState_InitialIsSensingStage(t *testing.T) {
	st := NewState()
	require.Equal(t, StageSensing, st.Stage)
	require.Empty(t, st.History)
	require.NotNil(t, st.Spec)
}

func TestState_AppendUserAndAssistant(t *testing.T) {
	st := NewState()
	st.AppendUser("hello")
	st.AppendAssistant("hi there")
	require.Len(t, st.History, 2)
	require.Equal(t, provider.RoleUser, st.History[0].Role)
	require.Equal(t, "hello", st.History[0].Content)
	require.Equal(t, provider.RoleAssistant, st.History[1].Role)
}

func TestState_RequiredSlotProgress(t *testing.T) {
	st := NewState()
	st.Spec.Goal = &gilv1.Goal{OneLiner: "x"}
	require.False(t, st.AllRequiredSlotsFilled())

	st.Spec.Goal.SuccessCriteriaNatural = []string{"a", "b", "c"}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	st.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}
	require.True(t, st.AllRequiredSlotsFilled())
}

func TestState_SaturationRequiresAdversaryClean(t *testing.T) {
	st := NewState()
	st.Spec.Goal = &gilv1.Goal{OneLiner: "x", SuccessCriteriaNatural: []string{"a", "b", "c"}}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	st.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}

	// Without adversary clean, not saturated
	require.False(t, st.IsSaturated())

	// Adversary ran but found issues — not saturated
	st.AdversaryRounds = 1
	st.LastAdversaryFindings = 3
	require.False(t, st.IsSaturated())

	// Adversary clean — saturated
	st.LastAdversaryFindings = 0
	require.True(t, st.IsSaturated())
}

func TestStage_String(t *testing.T) {
	require.Equal(t, "sensing", StageSensing.String())
	require.Equal(t, "conversation", StageConversation.String())
	require.Equal(t, "confirm", StageConfirm.String())
	require.Equal(t, "frozen", StageFrozen.String())
	require.Equal(t, "unknown", Stage(99).String())
}
