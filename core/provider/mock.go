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
