package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestEngine_Sensing_ExtractsDomain(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"domain":"web-saas","domain_confidence":0.85,"tech_hints":["go"],"scale_hint":"medium","ambiguity":"none"}`,
	})
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()

	require.NoError(t, eng.RunSensing(context.Background(), st, "I want to build a REST API for task management"))
	require.Equal(t, "web-saas", st.Domain)
	require.InDelta(t, 0.85, st.DomainConfidence, 0.001)
	require.Equal(t, StageConversation, st.Stage)
	// User input recorded
	require.Len(t, st.History, 1)
	require.Equal(t, "I want to build a REST API for task management", st.History[0].Content)
}

func TestEngine_Sensing_BadJSON_ReturnsError(t *testing.T) {
	mock := provider.NewMock([]string{`not json`})
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()

	err := eng.RunSensing(context.Background(), st, "x")
	require.Error(t, err)
	require.Equal(t, StageSensing, st.Stage) // didn't advance
}

func TestEngine_NextQuestion_ReturnsAgentText(t *testing.T) {
	mock := provider.NewMock([]string{`What technologies do you want to use?`})
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()
	st.Stage = StageConversation
	st.Domain = "web-saas"
	st.AppendUser("REST API")

	q, err := eng.NextQuestion(context.Background(), st)
	require.NoError(t, err)
	require.Equal(t, "What technologies do you want to use?", q)
}

func TestEngine_NextQuestion_PropagatesProviderError(t *testing.T) {
	mock := provider.NewMock(nil) // empty → exhausted on first call
	eng := NewEngine(mock, "claude-haiku-4-5")
	st := NewState()
	st.Stage = StageConversation

	_, err := eng.NextQuestion(context.Background(), st)
	require.Error(t, err)
}

func TestEngine_RunReplyTurn_FillsSlotAndAsksNext(t *testing.T) {
	// Mock provides 2 responses:
	// 1. SlotFiller — adds goal.one_liner
	// 2. Engine.NextQuestion — returns next question
	// (no adversary call yet because slots not all filled)
	mock := provider.NewMock([]string{
		`{"updates":[{"field":"goal.one_liner","value":"Build CLI"}]}`,
		`Tell me more about the user.`,
	})
	eng := NewEngineWithSubmodels(mock, "main", "main", "main")
	st := NewState()
	st.Stage = StageConversation
	st.Domain = "web-saas"

	turn, err := eng.RunReplyTurn(context.Background(), st, "I want a CLI")
	require.NoError(t, err)
	require.Equal(t, ReplyOutcomeNextQuestion, turn.Outcome)
	require.Equal(t, "Tell me more about the user.", turn.Content)
	require.Equal(t, "Build CLI", st.Spec.Goal.OneLiner)
	require.Len(t, st.History, 2) // user + assistant
}

func TestEngine_RunReplyTurn_FullSlotsRunsAdversaryAndAudit(t *testing.T) {
	// Pre-fill spec to be one-update-away from saturation
	st := NewState()
	st.Stage = StageConversation
	st.Domain = "web-saas"
	st.Spec.Goal = &gilv1.Goal{
		OneLiner:               "Build CLI",
		SuccessCriteriaNatural: []string{"a", "b", "c"},
	}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "x"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	// Risk missing — slotfill will add it

	mock := provider.NewMock([]string{
		`{"updates":[{"field":"risk.autonomy","value":"FULL"}]}`,    // 1. SlotFiller
		`[]`,                                                         // 2. Adversary (clean)
		`{"ready":true,"reason":"all good"}`,                         // 3. SelfAuditGate
	})
	eng := NewEngineWithSubmodels(mock, "main", "main", "main")

	turn, err := eng.RunReplyTurn(context.Background(), st, "full autonomy please")
	require.NoError(t, err)
	require.Equal(t, ReplyOutcomeReadyToConfirm, turn.Outcome)
	require.Equal(t, StageConfirm, st.Stage)
	require.Contains(t, turn.Content, "all good")
	require.Equal(t, gilv1.AutonomyDial_FULL, st.Spec.Risk.Autonomy)
	require.Equal(t, 1, st.AdversaryRounds)
	require.Equal(t, 0, st.LastAdversaryFindings)
}

func TestEngine_RunReplyTurn_AuditBlocksAdvancesAndAsksNext(t *testing.T) {
	// Pre-fill to be saturatable
	st := NewState()
	st.Stage = StageConversation
	st.Domain = "web-saas"
	st.Spec.Goal = &gilv1.Goal{OneLiner: "x", SuccessCriteriaNatural: []string{"a", "b", "c"}}
	st.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	st.Spec.Verification = &gilv1.Verification{Checks: []*gilv1.Check{{Name: "n"}}}
	st.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	st.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "p", ModelId: "m"}}
	st.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}

	mock := provider.NewMock([]string{
		`{"updates":[]}`,                              // 1. SlotFiller no-op
		`[]`,                                          // 2. Adversary clean
		`{"ready":false,"reason":"goal too vague"}`,    // 3. Audit blocks
		`Can you elaborate on what 'CLI' means?`,       // 4. NextQuestion
	})
	eng := NewEngineWithSubmodels(mock, "main", "main", "main")

	turn, err := eng.RunReplyTurn(context.Background(), st, "yes")
	require.NoError(t, err)
	require.Equal(t, ReplyOutcomeNextQuestion, turn.Outcome)
	require.Equal(t, StageConversation, st.Stage) // didn't advance
	require.Contains(t, turn.Content, "elaborate")
}
