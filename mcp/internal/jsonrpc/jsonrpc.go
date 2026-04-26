// Package jsonrpc implements a minimal JSON-RPC 2.0 transport over an
// io.Reader/io.Writer pair (stdio for MCP). Newline-delimited frames:
// each request is one line of JSON; each response is one line of JSON.
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Request is a JSON-RPC 2.0 request frame.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response frame.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Handler processes one Request and returns either a Result (any JSON-marshalable)
// or a non-nil Error. A nil ID indicates a notification — handlers may return
// (nil, nil) which means "no response sent". Transport.Serve drops responses
// for notifications regardless.
type Handler func(ctx context.Context, req *Request) (any, *Error)

// Transport reads requests line by line and dispatches them to handler.
// Writes responses line by line. Single-threaded reader; one in-flight at a
// time (sufficient for stdio MCP).
type Transport struct {
	in      io.Reader
	out     io.Writer
	handler Handler
	writeMu sync.Mutex
}

// NewTransport creates a Transport that reads from in, writes to out, and
// dispatches each request to handler.
func NewTransport(in io.Reader, out io.Writer, handler Handler) *Transport {
	return &Transport{in: in, out: out, handler: handler}
}

// Serve runs the read loop until the input EOFs or ctx is canceled.
func (t *Transport) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(t.in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024) // up to 16MB per frame
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			t.writeError(nil, &Error{Code: CodeParseError, Message: "parse error: " + err.Error()})
			continue
		}
		result, rpcErr := t.handler(ctx, &req)
		// Notifications: id is empty/null → skip response
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		if rpcErr != nil {
			t.writeError(req.ID, rpcErr)
			continue
		}
		t.writeResult(req.ID, result)
	}
	return scanner.Err()
}

func (t *Transport) writeResult(id json.RawMessage, result any) {
	resp := Response{JSONRPC: "2.0", ID: id, Result: result}
	t.writeJSON(resp)
}

func (t *Transport) writeError(id json.RawMessage, err *Error) {
	resp := Response{JSONRPC: "2.0", ID: id, Error: err}
	t.writeJSON(resp)
}

func (t *Transport) writeJSON(v any) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	_, _ = t.out.Write(b)
}
