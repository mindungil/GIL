package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// mockServer drives an LSP-like JSON-RPC peer over an io pipe. Tests wire
// the mock's stdin/stdout to a Client, register handler funcs per-method,
// and assert on the result the Client surfaces.
//
// The mock is intentionally simple: one method handler at a time, a
// shared mutex, no concurrency tricks. It mirrors what gopls / pyright do
// at the wire level (Content-Length-framed JSON-RPC) but says nothing
// useful unless a handler is registered.
type mockServer struct {
	t *testing.T

	stdinR  *io.PipeReader // server reads requests here
	stdinW  *io.PipeWriter // client writes requests here (this becomes Client stdin)
	stdoutR *io.PipeReader // client reads responses here (this becomes Client stdout)
	stdoutW *io.PipeWriter // server writes responses here

	mu       sync.Mutex
	handlers map[string]func(params json.RawMessage) (any, error)
	stopped  bool
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	m := &mockServer{
		t:        t,
		stdinR:   stdinR,
		stdinW:   stdinW,
		stdoutR:  stdoutR,
		stdoutW:  stdoutW,
		handlers: make(map[string]func(params json.RawMessage) (any, error)),
	}
	go m.serve()
	return m
}

// handle registers a response builder for a specific request method.
// The handler runs synchronously inside the read loop, so don't block.
func (m *mockServer) handle(method string, fn func(params json.RawMessage) (any, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[method] = fn
}

// notify pushes an unsolicited notification (e.g. publishDiagnostics) at
// the client. Tests use this to verify the client buffers diagnostics.
func (m *mockServer) notify(method string, params any) {
	m.write(rawMessage{JSONRPC: "2.0", Method: method, Params: rawJSON(params)})
}

func (m *mockServer) close() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	m.mu.Unlock()
	_ = m.stdinR.Close()
	_ = m.stdoutW.Close()
}

func (m *mockServer) serve() {
	defer m.stdoutW.Close()
	r := bufio.NewReader(m.stdinR)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		// Notification (no id): no response.
		if len(msg.ID) == 0 {
			continue
		}
		// Look up handler.
		m.mu.Lock()
		fn, ok := m.handlers[msg.Method]
		m.mu.Unlock()
		if !ok {
			// Default: reply null (server "saw" the request but had nothing to say).
			m.write(rawMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage("null")})
			continue
		}
		result, herr := fn(msg.Params)
		if herr != nil {
			m.write(rawMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32603, Message: herr.Error()}})
			continue
		}
		m.write(rawMessage{JSONRPC: "2.0", ID: msg.ID, Result: rawJSON(result)})
	}
}

func (m *mockServer) write(msg rawMessage) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_, _ = io.WriteString(m.stdoutW, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	_, _ = m.stdoutW.Write(body)
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// nopWriteCloser wraps an io.Writer so io.WriteCloser tests can pass a
// simple in-process pipe.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// stringSink helps tests verify the mock saw what the client sent without
// depending on order-of-bytes.
type stringSink struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *stringSink) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(b)
}

func (s *stringSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
