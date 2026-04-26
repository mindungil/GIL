package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
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
