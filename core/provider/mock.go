package provider

import (
	"context"
	"errors"
	"sync"
)

// Mock returns scripted responses in order. Useful for tests where you want
// deterministic behavior without hitting a real LLM API.
type Mock struct {
	mu         sync.Mutex
	responses  []string
	reasonings []string // optional, parallel to responses; "" means none
	idx        int
	// loop, when true, wraps idx around the responses slice instead
	// of returning an exhaustion error. Use for dogfood scenarios
	// (gil chat --provider mock) where the conversation length is
	// open-ended; tests that pin exhaustion behaviour leave it false.
	loop bool
}

// NewMock returns a Mock pre-loaded with the given response strings. Each
// Complete call consumes one response in order. Once exhausted, Complete
// returns an error. Use NewMockLoop for the cycling variant.
func NewMock(responses []string) *Mock {
	return &Mock{responses: responses}
}

// NewMockLoop returns a Mock that cycles its response list forever
// instead of erroring on exhaustion. Used by gild's default mock
// branch so an open-ended chat session ("gil chat --provider mock")
// doesn't crash on turn 3 — previously the daemon shipped a 2-entry
// list and the user got a stream error the moment they typed a reply.
func NewMockLoop(responses []string) *Mock {
	return &Mock{responses: responses, loop: true}
}

// SetReasonings attaches per-response Reasoning values for tests that
// need to exercise the upstream-separated-reasoning path. The slice
// runs parallel to responses; positions beyond its length receive an
// empty Reasoning. Safe to call before Complete; not safe to call
// concurrently with Complete.
func (m *Mock) SetReasonings(rs []string) { m.reasonings = rs }

// Name implements Provider.
func (m *Mock) Name() string { return "mock" }

// Complete returns the next scripted response.
func (m *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.responses) == 0 {
		return Response{}, errors.New("mock provider has no responses")
	}
	if m.idx >= len(m.responses) {
		if !m.loop {
			return Response{}, errors.New("mock provider responses exhausted")
		}
		m.idx = 0
	}
	resp := m.responses[m.idx]
	var reasoning string
	// Reasoning slice is parallel-indexed against responses by the
	// concrete `idx`, so wrapping above means the same reasoning value
	// recycles alongside its response — preserving the test contract
	// when SetReasonings was used with a looping mock.
	if m.idx < len(m.reasonings) {
		reasoning = m.reasonings[m.idx]
	}
	m.idx++
	return Response{
		Text:         resp,
		Reasoning:    reasoning,
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
