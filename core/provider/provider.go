// Package provider defines the LLM Provider abstraction used by the interview
// engine, run engine, and other gil components that need text completions.
package provider

import (
	"context"
	"encoding/json"
)

// Role of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ToolDef is a tool definition sent to the LLM (Anthropic native tool use format).
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema for tool's input
}

// ToolCall is a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          // unique id (Anthropic provides; needed to correlate tool_result)
	Name  string
	Input json.RawMessage
}

// ToolResult is a prior tool execution result, fed back to the LLM.
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Message is a single conversation turn.
type Message struct {
	Role        Role
	Content     string
	ToolCalls   []ToolCall   // if this assistant message contained tool_use blocks
	ToolResults []ToolResult // if this user message is feeding back tool_use results
	// CacheControl, when true, signals the provider adapter to attach an
	// Anthropic ephemeral cache_control marker to this message's last
	// content block. Used by core/compact.MarkCacheBreakpoints.
	CacheControl bool
}

// Request contains everything needed for an LLM completion.
type Request struct {
	Model              string
	Messages           []Message
	System             string
	SystemCacheControl bool    // when true, attach ephemeral cache_control to the system block
	MaxTokens          int
	Temperature        float64
	Tools              []ToolDef // tool defs sent to LLM
}

// Response carries the LLM output and usage metrics.
//
// Reasoning is populated when the upstream cleanly separates the
// chain-of-thought from the final answer (vLLM's `reasoning` field,
// DeepSeek-R1's `reasoning_content`, Anthropic extended-thinking
// blocks). When it is non-empty, callers can trust Text as the actual
// answer and skip the defensive preamble-stripping heuristics in
// core/interview/jsonextract.go. When empty, the upstream either
// inlined reasoning into Text or didn't reason at all — the caller
// must apply its own normalisation.
type Response struct {
	Text         string
	Reasoning    string
	InputTokens  int64
	OutputTokens int64
	ToolCalls    []ToolCall // populated when LLM wants to call tools
	StopReason   string     // "end_turn" | "tool_use" | ...
}

// Provider is the LLM abstraction. Concrete implementations live in this
// package: Mock (for tests), Anthropic (real API).
type Provider interface {
	// Name returns a short identifier for logs (e.g., "anthropic", "mock").
	Name() string
	// Complete sends a request and returns the model's response. The
	// returned Response is fully populated when the model finishes.
	Complete(ctx context.Context, req Request) (Response, error)
	// StreamComplete is like Complete, but fires onText for each
	// incremental text delta as it arrives from the upstream. The
	// final Response is returned the same way as Complete (with the
	// accumulated text in Response.Text). Reasoning and tool calls
	// are NOT streamed in this contract — they come back in the
	// returned Response only — keeping the wire shape simple for
	// session_prompt.go's chat path. Providers that don't natively
	// stream MAY fall back to calling Complete and emitting the
	// final text as a single onText call. onText may be nil; in that
	// case the implementation MUST behave identically to Complete.
	// P68c (2026-05-20).
	StreamComplete(ctx context.Context, req Request, onText func(string)) (Response, error)
}
