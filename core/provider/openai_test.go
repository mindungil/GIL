package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scriptedHandler is a small httptest handler that captures the inbound
// body, exposes it for assertions, and writes a fixed response. Splitting
// this out keeps each test focused on the request/response shape rather
// than HTTP plumbing.
type scriptedHandler struct {
	wantAuth string
	status   int
	respBody string
	gotBody  []byte
	gotAuth  string
	gotPath  string
}

func (h *scriptedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.gotBody = body
	h.gotAuth = r.Header.Get("Authorization")
	h.gotPath = r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	if h.status == 0 {
		h.status = http.StatusOK
	}
	w.WriteHeader(h.status)
	_, _ = w.Write([]byte(h.respBody))
}

func TestOpenAI_Name(t *testing.T) {
	o := NewOpenAI("k", "https://api.openai.com/v1")
	require.Equal(t, "openai", o.Name())
}

func TestOpenAI_Complete_TextOnly(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{
            "choices": [{"index":0, "message":{"role":"assistant","content":"hello back"}, "finish_reason":"stop"}],
            "usage": {"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}
        }`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("test-key", srv.URL)
	resp, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
		System:   "You are concise.",
		Messages: []Message{{Role: RoleUser, Content: "say hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, "hello back", resp.Text)
	require.Equal(t, "end_turn", resp.StopReason)
	require.Equal(t, int64(12), resp.InputTokens)
	require.Equal(t, int64(7), resp.OutputTokens)

	require.Equal(t, "Bearer test-key", h.gotAuth)
	require.Equal(t, "/chat/completions", h.gotPath)

	var sent chatRequest
	require.NoError(t, json.Unmarshal(h.gotBody, &sent))
	require.Equal(t, "gpt-4o-mini", sent.Model)
	require.Len(t, sent.Messages, 2) // system + user
	require.Equal(t, "system", sent.Messages[0].Role)
	require.Equal(t, "You are concise.", *sent.Messages[0].Content)
	require.Equal(t, "user", sent.Messages[1].Role)
	require.Equal(t, "say hi", *sent.Messages[1].Content)
}

func TestOpenAI_Complete_NoAPIKey_OmitsAuthHeader(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:    "qwen3.6-27b",
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	require.NoError(t, err)
	require.Empty(t, h.gotAuth, "no API key should mean no Authorization header")
}

func TestOpenAI_Complete_ToolUseRoundTrip(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{
            "choices":[{
                "index":0,
                "message":{
                    "role":"assistant",
                    "content":null,
                    "tool_calls":[{
                        "id":"call_abc",
                        "type":"function",
                        "function":{"name":"calculate","arguments":"{\"expr\":\"2+2\"}"}
                    }]
                },
                "finish_reason":"tool_calls"
            }],
            "usage":{"prompt_tokens":40,"completion_tokens":12}
        }`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	schema := json.RawMessage(`{"type":"object","properties":{"expr":{"type":"string"}}}`)
	o := NewOpenAI("k", srv.URL)
	resp, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "what is 2+2?"}},
		Tools: []ToolDef{{
			Name:        "calculate",
			Description: "evaluate an expression",
			Schema:      schema,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "", resp.Text, "content was null in the upstream response")
	require.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	require.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	require.Equal(t, "calculate", resp.ToolCalls[0].Name)

	// Arguments must round-trip back to a parseable JSON object.
	var args struct {
		Expr string `json:"expr"`
	}
	require.NoError(t, json.Unmarshal(resp.ToolCalls[0].Input, &args))
	require.Equal(t, "2+2", args.Expr)

	// Tool definition must hit the wire as `{"type":"function","function":{...}}`.
	var sent chatRequest
	require.NoError(t, json.Unmarshal(h.gotBody, &sent))
	require.Len(t, sent.Tools, 1)
	require.Equal(t, "function", sent.Tools[0].Type)
	require.Equal(t, "calculate", sent.Tools[0].Function.Name)
	require.JSONEq(t, string(schema), string(sent.Tools[0].Function.Parameters))
}

func TestOpenAI_Complete_ToolResultsBecomeRoleToolMessages(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: RoleUser, Content: "calc"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "call_1", Name: "calculate", Input: json.RawMessage(`{"expr":"1+1"}`)},
				{ID: "call_2", Name: "calculate", Input: json.RawMessage(`{"expr":"2+2"}`)},
			}},
			{Role: RoleUser, ToolResults: []ToolResult{
				{ToolUseID: "call_1", Content: "2"},
				{ToolUseID: "call_2", Content: "4"},
			}},
		},
	})
	require.NoError(t, err)

	var sent chatRequest
	require.NoError(t, json.Unmarshal(h.gotBody, &sent))

	// Expect: user, assistant(tool_calls), tool(call_1), tool(call_2)
	require.Len(t, sent.Messages, 4)
	require.Equal(t, "user", sent.Messages[0].Role)
	require.Equal(t, "assistant", sent.Messages[1].Role)
	require.Nil(t, sent.Messages[1].Content, "assistant tool_calls msg should have null content")
	require.Len(t, sent.Messages[1].ToolCalls, 2)
	// Arguments must be JSON-encoded as a STRING per OpenAI spec.
	require.Equal(t, `{"expr":"1+1"}`, sent.Messages[1].ToolCalls[0].Function.Arguments)
	require.Equal(t, `{"expr":"2+2"}`, sent.Messages[1].ToolCalls[1].Function.Arguments)

	require.Equal(t, "tool", sent.Messages[2].Role)
	require.Equal(t, "call_1", sent.Messages[2].ToolCallID)
	require.Equal(t, "2", *sent.Messages[2].Content)
	require.Equal(t, "tool", sent.Messages[3].Role)
	require.Equal(t, "call_2", sent.Messages[3].ToolCallID)
	require.Equal(t, "4", *sent.Messages[3].Content)
}

func TestOpenAI_Complete_RateLimit(t *testing.T) {
	srv := httptest.NewServer(&scriptedHandler{
		status:   http.StatusTooManyRequests,
		respBody: `{"error":{"message":"Rate limit reached"}}`,
	})
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var rl *ProviderRateLimit
	require.True(t, errors.As(err, &rl), "want ProviderRateLimit, got %T: %v", err, err)
	require.Equal(t, 429, rl.StatusCode)

	// retry.go does substring matching; make sure our error text triggers it.
	require.True(t, isRetryable(err), "ProviderRateLimit must be classified retryable")
}

func TestOpenAI_Complete_Transient5xx(t *testing.T) {
	srv := httptest.NewServer(&scriptedHandler{
		status:   http.StatusBadGateway,
		respBody: `<html><body>upstream gone</body></html>`,
	})
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var tr *ProviderTransient
	require.True(t, errors.As(err, &tr), "want ProviderTransient, got %T: %v", err, err)
	require.Equal(t, 502, tr.StatusCode)
	require.True(t, isRetryable(err), "ProviderTransient must be classified retryable")
}

func TestOpenAI_Complete_Permanent4xx(t *testing.T) {
	srv := httptest.NewServer(&scriptedHandler{
		status:   http.StatusUnauthorized,
		respBody: `{"error":{"message":"invalid api key"}}`,
	})
	defer srv.Close()

	o := NewOpenAI("bad-key", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	var pe *ProviderPermanent
	require.True(t, errors.As(err, &pe), "want ProviderPermanent, got %T: %v", err, err)
	require.Equal(t, 401, pe.StatusCode)
	require.False(t, isRetryable(err), "ProviderPermanent must NOT be classified retryable")
	require.Contains(t, pe.Error(), "invalid api key")
}

func TestOpenAI_Complete_EmptyContentWithToolCalls(t *testing.T) {
	// The wire-level shape OpenAI emits when the model only calls tools:
	// content is JSON null, finish_reason is "tool_calls". Verify Complete
	// reads it without confusing "" with "absent text".
	h := &scriptedHandler{
		respBody: `{
            "choices":[{
                "message":{"role":"assistant","content":null,"tool_calls":[
                    {"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}
                ]},
                "finish_reason":"tool_calls"
            }],
            "usage":{"prompt_tokens":2,"completion_tokens":2}
        }`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	resp, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "go"}},
	})
	require.NoError(t, err)
	require.Equal(t, "", resp.Text)
	require.Len(t, resp.ToolCalls, 1)
	require.JSONEq(t, "{}", string(resp.ToolCalls[0].Input))
}

func TestOpenAI_Complete_RequiresModel(t *testing.T) {
	o := NewOpenAI("k", "https://example.com/v1")
	_, err := o.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "model required")
}

func TestOpenAI_Complete_RequiresBaseURL(t *testing.T) {
	o := NewOpenAI("k", "")
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "base URL required")
}

func TestOpenAI_Complete_TemperatureOmittedWhenZero(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:       "gpt-4o",
		Messages:    []Message{{Role: RoleUser, Content: "x"}},
		Temperature: 0,
	})
	require.NoError(t, err)
	require.NotContains(t, string(h.gotBody), `"temperature"`,
		"temperature should be omitted when zero so upstream defaults apply")
}

func TestOpenAI_Complete_CacheControlIgnored(t *testing.T) {
	// Anthropic-only cache_control hints must not affect the OpenAI wire body.
	h := &scriptedHandler{
		respBody: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:              "gpt-4o",
		System:             "sys",
		SystemCacheControl: true,
		Messages: []Message{
			{Role: RoleUser, Content: "x", CacheControl: true},
		},
	})
	require.NoError(t, err)
	require.NotContains(t, string(h.gotBody), "cache_control",
		"cache_control hints must not be serialised on the OpenAI path")
}

func TestOpenAI_Complete_BaseURLTrailingSlashIgnored(t *testing.T) {
	h := &scriptedHandler{
		respBody: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	o := NewOpenAI("k", srv.URL+"/")
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.NoError(t, err)
	require.Equal(t, "/chat/completions", h.gotPath, "trailing slash on BaseURL must not double-up the path")
}

func TestOpenAI_MapFinishReason(t *testing.T) {
	cases := map[string]string{
		"stop":          "end_turn",
		"tool_calls":    "tool_use",
		"length":        "max_tokens",
		"content_filter": "content_filter", // pass-through for unknown values
		"":              "",
	}
	for in, want := range cases {
		require.Equal(t, want, mapFinishReason(in), "input %q", in)
	}
}

func TestOpenAI_TruncateBodyInError(t *testing.T) {
	// Long bodies should be truncated in the error message so logs don't
	// accidentally swallow an HTML stack trace from a misconfigured upstream.
	long := strings.Repeat("x", 1000)
	srv := httptest.NewServer(&scriptedHandler{
		status:   http.StatusInternalServerError,
		respBody: long,
	})
	defer srv.Close()

	o := NewOpenAI("k", srv.URL)
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	require.Error(t, err)
	require.Less(t, len(err.Error()), 500, "error message should be truncated, was %d bytes", len(err.Error()))
}
