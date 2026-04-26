package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
)

func TestAdversary_FindsBlockers(t *testing.T) {
	mock := provider.NewMock([]string{
		`[{"severity":"blocker","category":"missing_verification","finding":"no integration test","question_to_user":"How do you verify integration?","proposed_addition":"add e2e check"}]`,
	})
	a := NewAdversary(mock, "claude-haiku-4-5")
	st := NewState()

	findings, err := a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "blocker", findings[0].Severity)
	require.Equal(t, "missing_verification", findings[0].Category)
	require.Equal(t, 1, st.AdversaryRounds)
	require.Equal(t, 1, st.LastAdversaryFindings)
}

func TestAdversary_NoFindings(t *testing.T) {
	mock := provider.NewMock([]string{`[]`})
	a := NewAdversary(mock, "x")
	st := NewState()
	findings, err := a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Empty(t, findings)
	require.Equal(t, 1, st.AdversaryRounds)
	require.Equal(t, 0, st.LastAdversaryFindings)
}

func TestAdversary_BadJSON_ReturnsError(t *testing.T) {
	mock := provider.NewMock([]string{`not an array`})
	a := NewAdversary(mock, "x")
	st := NewState()
	_, err := a.Critique(context.Background(), st)
	require.Error(t, err)
	// Should NOT increment counters on parse failure
	require.Equal(t, 0, st.AdversaryRounds)
}

func TestAdversary_MultipleRounds(t *testing.T) {
	mock := provider.NewMock([]string{`[{"severity":"high","category":"x","finding":"y"}]`, `[]`})
	a := NewAdversary(mock, "x")
	st := NewState()

	_, err := a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Equal(t, 1, st.AdversaryRounds)
	require.Equal(t, 1, st.LastAdversaryFindings)

	_, err = a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Equal(t, 2, st.AdversaryRounds)
	require.Equal(t, 0, st.LastAdversaryFindings)
}
