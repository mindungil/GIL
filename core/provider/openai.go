package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI is a Provider backed by the OpenAI Chat Completions HTTP API.
//
// The same wire format is implemented by a long tail of OpenAI-compatible
// endpoints — OpenRouter, vLLM, llama.cpp's server, ollama's /v1 shim, etc.
// — so this single adapter covers all three of gil's "OpenAI family"
// provider names (openai, openrouter, vllm). The factory in gild picks the
// right BaseURL and default model per provider name.
//
// Only the standard library is used (net/http + encoding/json) so we don't
// pull in another SDK for what is a small, stable JSON contract.
type OpenAI struct {
	// BaseURL is the API root, e.g. "https://api.openai.com/v1" or
	// "http://vllm.local:8000/v1". The "/chat/completions" suffix is
	// appended by Complete.
	BaseURL string
	// APIKey is the bearer token. Empty is allowed for unauthenticated local
	// endpoints (some vLLM installs and llama.cpp servers run without auth).
	APIKey string
	// HTTP is the HTTP client used for requests. nil falls back to a default
	// client with a generous (5 minute) timeout to accommodate slow local
	// models.
	HTTP *http.Client
}

// NewOpenAI returns an OpenAI provider. apiKey may be empty for local
// unauthenticated endpoints; baseURL must be the API root including the
// version segment (e.g. ".../v1").
func NewOpenAI(apiKey, baseURL string) *OpenAI {
	return &OpenAI{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Name implements Provider. Returns "openai" because this single adapter
// covers any OpenAI-compatible endpoint; the gild factory remembers which
// concrete provider name (openai/openrouter/vllm) the user chose.
func (o *OpenAI) Name() string { return "openai" }

// --- typed errors ---------------------------------------------------------
//
// The retry wrapper in retry.go currently dispatches by string-matching the
// error text. We mirror that contract — every typed error here embeds the
// well-known signal substrings ("rate limit", "5xx code", "timeout") in its
// Error() output so existing isRetryable logic does the right thing without
// needing to learn new types. Callers that want richer behaviour can use
// errors.As to peel off the typed wrapper.

// ProviderRateLimit indicates the upstream returned HTTP 429. The retry
// wrapper treats this as retryable.
type ProviderRateLimit struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *ProviderRateLimit) Error() string {
	return fmt.Sprintf("%s rate limit (HTTP %d): %s", e.Provider, e.StatusCode, truncate(e.Body, 200))
}

// ProviderTransient indicates a 5xx or otherwise retryable upstream
// failure. The retry wrapper retries with backoff.
type ProviderTransient struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *ProviderTransient) Error() string {
	return fmt.Sprintf("%s transient error (HTTP %d): %s", e.Provider, e.StatusCode, truncate(e.Body, 200))
}

// ProviderPermanent indicates a 4xx (not 429) — auth, bad request, missing
// model — that retries will not fix. The retry wrapper propagates immediately.
type ProviderPermanent struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *ProviderPermanent) Error() string {
	return fmt.Sprintf("%s permanent error (HTTP %d): %s", e.Provider, e.StatusCode, truncate(e.Body, 200))
}

// --- wire types -----------------------------------------------------------

// chatRequest is the Chat Completions request body. We only marshal the
// fields the adapter actually uses; unknown fields are intentionally left
// out rather than serialised as zero values, so tests can assert exact body
// shape with omitempty semantics in mind.
type chatRequest struct {
	Model       string         `json:"model"`
	Messages    []chatMessage  `json:"messages"`
	Tools       []chatTool     `json:"tools,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
}

// chatMessage is one OpenAI message. Content is *string (not string) so we
// can faithfully emit `null` for assistant messages that contain only
// tool_calls — the API rejects an empty-string content paired with
// tool_calls in some compliance modes.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function chatToolCallFunctionDef `json:"function"`
}

// chatToolCallFunctionDef carries the function name and the arguments
// payload. arguments is a JSON STRING in OpenAI format — not an object.
// We marshal the inner JSON and store the resulting bytes verbatim so the
// upstream parses round-trip-safely.
type chatToolCallFunctionDef struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatResponse is the relevant subset of the response body. Fields we don't
// consume (model, created, id, system_fingerprint, …) are simply not declared.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatRespMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// chatRespMsg is the assistant message inside a choice. content can be a
// JSON string OR null when the model only emitted tool_calls; we declare
// it as *string so the unmarshal preserves the null/empty distinction.
type chatRespMsg struct {
	Role      string             `json:"role"`
	Content   *string            `json:"content"`
	ToolCalls []chatRespToolCall `json:"tool_calls"`
}

type chatRespToolCall struct {
	ID       string                       `json:"id"`
	Type     string                       `json:"type"`
	Function chatRespToolCallFunctionCall `json:"function"`
}

type chatRespToolCallFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// --- Complete -------------------------------------------------------------

// Complete sends req to the upstream and returns the parsed response. It
// translates between gil's provider.Request/Response shape and the OpenAI
// chat.completions wire format.
//
// Mapping notes:
//   - req.System is prepended as a {"role":"system"} message before the
//     conversation; OpenAI does not have a separate system field.
//   - req.Messages with ToolResults expand into one role:"tool" message per
//     result (one per tool_call_id), per OpenAI's spec.
//   - req.CacheControl / req.SystemCacheControl are Anthropic-only and are
//     silently ignored here so callers can pass the same Request object to
//     either provider without conditional logic.
//   - req.Temperature == 0 is treated as "unset" (omitted) so the upstream
//     uses its own default; this matches what the Anthropic adapter does.
func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	if req.Model == "" {
		return Response{}, errors.New("openai.Complete: model required")
	}
	if o.BaseURL == "" {
		return Response{}, errors.New("openai.Complete: base URL required")
	}

	body, err := o.buildRequestBody(req)
	if err != nil {
		return Response{}, err
	}

	url := o.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("openai.Complete: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("openai.Complete: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("openai.Complete: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Response{}, classifyHTTPError(o.Name(), resp.StatusCode, respBody)
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("openai.Complete: parse response: %w (body excerpt: %s)", err, truncate(string(respBody), 200))
	}

	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("openai.Complete: response had no choices (body excerpt: %s)", truncate(string(respBody), 200))
	}

	choice := parsed.Choices[0]
	out := Response{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		StopReason:   mapFinishReason(choice.FinishReason),
	}
	if choice.Message.Content != nil {
		out.Text = *choice.Message.Content
	}
	for _, tc := range choice.Message.ToolCalls {
		// arguments is a JSON string per the OpenAI spec. Wrap it as
		// json.RawMessage by parsing-then-remarshalling to validate the
		// inner JSON; if the model emitted invalid JSON we keep the raw
		// bytes so the caller still sees what was returned and can decide
		// how to recover.
		raw := json.RawMessage(tc.Function.Arguments)
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		} else {
			var probe interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &probe); err == nil {
				raw = json.RawMessage(tc.Function.Arguments)
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: raw,
		})
	}
	return out, nil
}

// buildRequestBody marshals req into the OpenAI chat.completions JSON body.
// Split out from Complete so tests can exercise the request-shape mapping
// without spinning up an HTTP server.
func (o *OpenAI) buildRequestBody(req Request) ([]byte, error) {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		s := req.System
		msgs = append(msgs, chatMessage{Role: "system", Content: &s})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			// Tool results expand into N role:"tool" messages — one per
			// tool_call_id — rather than a single user message carrying an
			// array. This matches OpenAI's documented shape; collapsing
			// them into one user message confuses the model when there are
			// multiple parallel tool calls in flight.
			if len(m.ToolResults) > 0 {
				for _, tr := range m.ToolResults {
					content := tr.Content
					msgs = append(msgs, chatMessage{
						Role:       "tool",
						Content:    &content,
						ToolCallID: tr.ToolUseID,
					})
				}
				continue
			}
			if m.Content != "" {
				c := m.Content
				msgs = append(msgs, chatMessage{Role: "user", Content: &c})
			}
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				calls := make([]chatToolCall, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					argStr := string(tc.Input)
					if argStr == "" {
						argStr = "{}"
					}
					calls = append(calls, chatToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: chatToolCallFunctionDef{
							Name:      tc.Name,
							Arguments: argStr,
						},
					})
				}
				// Content is null when there is no narrative text alongside
				// the tool calls — the API rejects "" + tool_calls in some
				// strict modes, so we explicitly emit null.
				msg := chatMessage{Role: "assistant", ToolCalls: calls}
				if m.Content != "" {
					c := m.Content
					msg.Content = &c
				}
				msgs = append(msgs, msg)
			} else if m.Content != "" {
				c := m.Content
				msgs = append(msgs, chatMessage{Role: "assistant", Content: &c})
			}
		case RoleSystem:
			// Allow stray RoleSystem messages even though the canonical
			// way to pass system text is the Request.System field.
			if m.Content != "" {
				c := m.Content
				msgs = append(msgs, chatMessage{Role: "system", Content: &c})
			}
		}
	}

	body := chatRequest{
		Model:     req.Model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
	}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	if len(req.Tools) > 0 {
		tools := make([]chatTool, 0, len(req.Tools))
		for _, td := range req.Tools {
			schema := td.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, chatTool{
				Type: "function",
				Function: chatToolFunction{
					Name:        td.Name,
					Description: td.Description,
					Parameters:  schema,
				},
			})
		}
		body.Tools = tools
	}

	return json.Marshal(body)
}

// mapFinishReason maps OpenAI's finish_reason vocabulary into gil's
// provider-agnostic StopReason set. Unknown values pass through verbatim
// so callers can debug the upstream's behaviour without a code change.
func mapFinishReason(r string) string {
	switch r {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "":
		return ""
	default:
		return r
	}
}

// classifyHTTPError translates an HTTP status into the typed error the
// retry wrapper recognises. The body is included (truncated) so log output
// surfaces the upstream's error message — auth failures in particular are
// often "invalid_api_key" or "invalid model X" which the user needs to see.
func classifyHTTPError(provider string, status int, body []byte) error {
	switch {
	case status == http.StatusTooManyRequests:
		return &ProviderRateLimit{Provider: provider, StatusCode: status, Body: string(body)}
	case status >= 500:
		return &ProviderTransient{Provider: provider, StatusCode: status, Body: string(body)}
	default:
		return &ProviderPermanent{Provider: provider, StatusCode: status, Body: string(body)}
	}
}

// truncate returns s shortened to at most n bytes with an ellipsis when
// truncation occurred. Used for HTTP body excerpts in error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
