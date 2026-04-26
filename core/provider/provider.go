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
}

// Request contains everything needed for an LLM completion.
type Request struct {
	Model       string
	Messages    []Message
	System      string
	MaxTokens   int
	Temperature float64
	Tools       []ToolDef // tool defs sent to LLM
}

// Response carries the LLM output and usage metrics.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	ToolCalls    []ToolCall // populated when LLM wants to call tools
	StopReason   string      // "end_turn" | "tool_use" | ...
}

// Provider is the LLM abstraction. Concrete implementations live in this
// package: Mock (for tests), Anthropic (real API).
type Provider interface {
	// Name returns a short identifier for logs (e.g., "anthropic", "mock").
	Name() string
	// Complete sends a request and returns the model's response.
	Complete(ctx context.Context, req Request) (Response, error)
}
