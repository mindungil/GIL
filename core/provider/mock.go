package provider

import (
	"context"
	"errors"
	"sync"
)

// Mock returns scripted responses in order. Useful for tests where you want
// deterministic behavior without hitting a real LLM API.
type Mock struct {
	mu        sync.Mutex
	responses []string
	idx       int
}

// NewMock returns a Mock pre-loaded with the given response strings. Each
// Complete call consumes one response in order. Once exhausted, Complete
// returns an error.
func NewMock(responses []string) *Mock {
	return &Mock{responses: responses}
}

// Name implements Provider.
func (m *Mock) Name() string { return "mock" }

// Complete returns the next scripted response.
func (m *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return Response{}, errors.New("mock provider responses exhausted")
	}
	resp := m.responses[m.idx]
	m.idx++
	return Response{
		Text:         resp,
		InputTokens:  int64(len(req.Messages) * 10),
		OutputTokens: int64(len(resp)),
		StopReason:   "end_turn",
	}, nil
}

// MockTurn is one scripted response that may include tool calls.
type MockTurn struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
}

// MockToolProvider returns scripted MockTurns, one per Complete call.
// Useful for testing AgentLoop behavior with deterministic tool call sequences.
type MockToolProvider struct {
	mu    sync.Mutex
	turns []MockTurn
	idx   int
}

// NewMockToolProvider returns a MockToolProvider pre-loaded with the given turns.
func NewMockToolProvider(turns []MockTurn) *MockToolProvider {
	return &MockToolProvider{turns: turns}
}

// Name implements Provider.
func (m *MockToolProvider) Name() string { return "mock-tool" }

// Complete returns the next scripted turn.
func (m *MockToolProvider) Complete(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.turns) {
		return Response{}, errors.New("mock-tool provider turns exhausted")
	}
	turn := m.turns[m.idx]
	m.idx++
	return Response{
		Text:         turn.Text,
		ToolCalls:    turn.ToolCalls,
		StopReason:   turn.StopReason,
		InputTokens:  int64(len(req.Messages) * 10),
		OutputTokens: int64(len(turn.Text)),
	}, nil
}
