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
	return m.StreamComplete(ctx, req, nil)
}

// StreamComplete returns the next scripted response, optionally firing
// onText in 3 chunks so tests of streaming wiring observe progressive
// delivery. P68c.
func (m *Mock) StreamComplete(ctx context.Context, req Request, onText func(string)) (Response, error) {
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
	if m.idx < len(m.reasonings) {
		reasoning = m.reasonings[m.idx]
	}
	m.idx++
	if onText != nil && resp != "" {
		for _, chunk := range splitForStreaming(resp, 3) {
			onText(chunk)
		}
	}
	return Response{
		Text:         resp,
		Reasoning:    reasoning,
		InputTokens:  int64(len(req.Messages) * 10),
		OutputTokens: int64(len(resp)),
		StopReason:   "end_turn",
	}, nil
}

// splitForStreaming chops s into roughly n equal pieces by rune count.
// Returns at most n non-empty chunks; concatenating them recovers s
// exactly. Used by mock providers to exercise the streaming callback
// path without requiring a real upstream.
func splitForStreaming(s string, n int) []string {
	if n <= 1 || len(s) == 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= n {
		return []string{s}
	}
	size := len(runes) / n
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		start := i * size
		end := start + size
		if i == n-1 {
			end = len(runes)
		}
		out = append(out, string(runes[start:end]))
	}
	return out
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
	return m.StreamComplete(ctx, req, nil)
}

// StreamComplete returns the next scripted turn. When onText is set
// AND the turn carries text (i.e. not a pure tool_use turn), the text
// is split into 3 chunks and delivered via onText before the final
// Response. Tool calls are NOT streamed — they appear only in the
// returned Response. P68c.
func (m *MockToolProvider) StreamComplete(ctx context.Context, req Request, onText func(string)) (Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.turns) {
		return Response{}, errors.New("mock-tool provider turns exhausted")
	}
	turn := m.turns[m.idx]
	m.idx++
	if onText != nil && turn.Text != "" {
		for _, chunk := range splitForStreaming(turn.Text, 3) {
			onText(chunk)
		}
	}
	return Response{
		Text:         turn.Text,
		ToolCalls:    turn.ToolCalls,
		StopReason:   turn.StopReason,
		InputTokens:  int64(len(req.Messages) * 10),
		OutputTokens: int64(len(turn.Text)),
	}, nil
}
