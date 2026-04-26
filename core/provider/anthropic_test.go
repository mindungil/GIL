package provider

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropic_Name(t *testing.T) {
	p := NewAnthropic("dummy-key")
	require.Equal(t, "anthropic", p.Name())
}

func TestAnthropic_Complete_LiveSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live smoke")
	}

	p := NewAnthropic(key)
	resp, err := p.Complete(context.Background(), Request{
		Model:     "claude-haiku-4-5",
		Messages:  []Message{{Role: RoleUser, Content: "Reply with just the word 'pong' and nothing else."}},
		MaxTokens: 10,
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(strings.ToLower(resp.Text), "pong"), "got %q", resp.Text)
	require.Greater(t, resp.OutputTokens, int64(0))
	require.Greater(t, resp.InputTokens, int64(0))
}

func TestAnthropic_Complete_RequiresModel(t *testing.T) {
	p := NewAnthropic("dummy-key")
	_, err := p.Complete(context.Background(), Request{
		Model:    "",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "model required")
}
