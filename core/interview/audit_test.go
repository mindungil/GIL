package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/provider"
)

func TestSelfAuditGate_PassesWhenAgentApproves(t *testing.T) {
	mock := provider.NewMock([]string{`{"ready":true,"reason":"all required slots filled, adversary clean"}`})
	g := NewSelfAuditGate(mock, "x")
	st := NewState()

	pass, reason, err := g.AuditConversationToConfirm(context.Background(), st)
	require.NoError(t, err)
	require.True(t, pass)
	require.NotEmpty(t, reason)
	require.Contains(t, reason, "slots filled")
}

func TestSelfAuditGate_BlocksWhenNotReady(t *testing.T) {
	mock := provider.NewMock([]string{`{"ready":false,"reason":"goal still vague"}`})
	g := NewSelfAuditGate(mock, "x")
	st := NewState()

	pass, reason, err := g.AuditConversationToConfirm(context.Background(), st)
	require.NoError(t, err)
	require.False(t, pass)
	require.Contains(t, reason, "vague")
}

func TestSelfAuditGate_BadJSON_ReturnsError(t *testing.T) {
	mock := provider.NewMock([]string{`not json`})
	g := NewSelfAuditGate(mock, "x")
	st := NewState()
	_, _, err := g.AuditConversationToConfirm(context.Background(), st)
	require.Error(t, err)
}

func TestSelfAuditGate_ProviderError(t *testing.T) {
	mock := provider.NewMock(nil) // empty -> exhausted on first call
	g := NewSelfAuditGate(mock, "x")
	st := NewState()
	_, _, err := g.AuditConversationToConfirm(context.Background(), st)
	require.Error(t, err)
}
