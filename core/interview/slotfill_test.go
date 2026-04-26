package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestSlotFiller_ExtractsGoalAndConstraints(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[
		  {"field":"goal.one_liner","value":"Build a CLI todo manager"},
		  {"field":"constraints.tech_stack","value":["Go","SQLite"]}
		]}`,
	})
	f := NewSlotFiller(mock, "claude-haiku-4-5")
	st := NewState()
	st.Stage = StageConversation

	require.NoError(t, f.Apply(context.Background(), st, "I want a Go CLI for managing todos, using SQLite"))
	require.NotNil(t, st.Spec.Goal)
	require.Equal(t, "Build a CLI todo manager", st.Spec.Goal.OneLiner)
	require.NotNil(t, st.Spec.Constraints)
	require.Equal(t, []string{"Go", "SQLite"}, st.Spec.Constraints.TechStack)
}

func TestSlotFiller_NoUpdates_OK(t *testing.T) {
	mock := provider.NewMock([]string{`{"updates":[]}`})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.NoError(t, f.Apply(context.Background(), st, "small talk"))
	require.Nil(t, st.Spec.Goal)
}

func TestSlotFiller_BadJSON_ReturnsError(t *testing.T) {
	mock := provider.NewMock([]string{`not json`})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.Error(t, f.Apply(context.Background(), st, "x"))
}

func TestSlotFiller_AppliesEnumFields(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[
		  {"field":"workspace.backend","value":"DOCKER"},
		  {"field":"risk.autonomy","value":"FULL"}
		]}`,
	})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.NoError(t, f.Apply(context.Background(), st, "use docker, full autonomy"))
	require.NotNil(t, st.Spec.Workspace)
	require.Equal(t, gilv1.WorkspaceBackend_DOCKER, st.Spec.Workspace.Backend)
	require.NotNil(t, st.Spec.Risk)
	require.Equal(t, gilv1.AutonomyDial_FULL, st.Spec.Risk.Autonomy)
}

func TestSlotFiller_AppliesVerificationChecks(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[
		  {"field":"verification.checks","value":[
		    {"name":"build","kind":"SHELL","command":"go build","expected_exit_code":0},
		    {"name":"test","kind":"SHELL","command":"go test ./...","expected_exit_code":0}
		  ]}
		]}`,
	})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.NoError(t, f.Apply(context.Background(), st, "build and test must pass"))
	require.NotNil(t, st.Spec.Verification)
	require.Len(t, st.Spec.Verification.Checks, 2)
	require.Equal(t, "build", st.Spec.Verification.Checks[0].Name)
	require.Equal(t, gilv1.CheckKind_SHELL, st.Spec.Verification.Checks[0].Kind)
}

func TestSlotFiller_AppliesModels(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[
		  {"field":"models.main","value":{"provider":"anthropic","modelId":"claude-opus-4-7"}}
		]}`,
	})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.NoError(t, f.Apply(context.Background(), st, "use claude opus"))
	require.NotNil(t, st.Spec.Models)
	require.NotNil(t, st.Spec.Models.Main)
	require.Equal(t, "anthropic", st.Spec.Models.Main.Provider)
	require.Equal(t, "claude-opus-4-7", st.Spec.Models.Main.ModelId)
}

func TestSlotFiller_UnknownFieldSkipped(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[{"field":"some.unknown.path","value":"x"}]}`,
	})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	// Should not error; just silently skip
	require.NoError(t, f.Apply(context.Background(), st, "x"))
}
