package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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

func TestAnthropic_CacheControl_OnLastBlocks(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	client := anthropic.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL))
	a := &Anthropic{client: client}
	_, err := a.Complete(context.Background(), Request{
		Model:              "m",
		System:             "you are helpful",
		SystemCacheControl: true,
		Messages: []Message{
			{Role: RoleUser, Content: "first"},
			{Role: RoleAssistant, Content: "second", CacheControl: true},
			{Role: RoleUser, Content: "third", CacheControl: true},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, captured)
	// Verify ephemeral cache_control appears in the request body
	require.Contains(t, string(captured), `"cache_control":{"type":"ephemeral"}`)
	// System block should carry it
	require.Regexp(t, `(?s)"system":.*"cache_control":\{"type":"ephemeral"\}`, string(captured))
}
