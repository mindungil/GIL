// Package provider defines the LLM Provider abstraction used by the interview
// engine, run engine, and other gil components that need text completions.
package provider

import "context"

// Role of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single conversation turn.
type Message struct {
	Role    Role
	Content string
}

// Request contains everything needed for an LLM completion.
type Request struct {
	Model       string
	Messages    []Message
	System      string
	MaxTokens   int
	Temperature float64
}

// Response carries the LLM output and usage metrics.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	StopReason   string
}

// Provider is the LLM abstraction. Concrete implementations live in this
// package: Mock (for tests), Anthropic (real API).
type Provider interface {
	// Name returns a short identifier for logs (e.g., "anthropic", "mock").
	Name() string
	// Complete sends a request and returns the model's response.
	Complete(ctx context.Context, req Request) (Response, error)
}
