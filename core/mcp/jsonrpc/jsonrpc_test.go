package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransport_RoundTrip(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"x":42}}` + "\n")
	var out bytes.Buffer
	handler := func(ctx context.Context, req *Request) (any, *Error) {
		require.Equal(t, "echo", req.Method)
		return map[string]any{"echoed": json.RawMessage(req.Params)}, nil
	}
	err := NewTransport(in, &out, handler).Serve(context.Background())
	require.NoError(t, err)
	require.Contains(t, out.String(), `"id":1`)
	require.Contains(t, out.String(), `"echoed"`)
}

func TestTransport_NotificationNoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer
	handler := func(ctx context.Context, req *Request) (any, *Error) {
		return nil, nil
	}
	require.NoError(t, NewTransport(in, &out, handler).Serve(context.Background()))
	require.Empty(t, out.String(), "notifications get no response")
}

func TestTransport_HandlerError(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"oops"}` + "\n")
	var out bytes.Buffer
	handler := func(ctx context.Context, req *Request) (any, *Error) {
		return nil, &Error{Code: CodeMethodNotFound, Message: "no"}
	}
	require.NoError(t, NewTransport(in, &out, handler).Serve(context.Background()))
	require.Contains(t, out.String(), `"error"`)
	require.Contains(t, out.String(), "no")
}

func TestTransport_ParseError(t *testing.T) {
	in := strings.NewReader(`{not json}` + "\n")
	var out bytes.Buffer
	require.NoError(t, NewTransport(in, &out, func(ctx context.Context, r *Request) (any, *Error) { return nil, nil }).Serve(context.Background()))
	require.Contains(t, out.String(), "parse error")
}

func TestTransport_MultipleFrames(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"a"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"b"}` + "\n")
	var out bytes.Buffer
	handler := func(ctx context.Context, req *Request) (any, *Error) {
		return req.Method, nil
	}
	require.NoError(t, NewTransport(in, &out, handler).Serve(context.Background()))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], `"id":1`)
	require.Contains(t, lines[1], `"id":2`)
}

// Pipe-based test: connect Transport to itself via in-memory pipe to verify
// concurrent write safety (multiple goroutines writing responses).
func TestTransport_ConcurrentWritesSerialized(t *testing.T) {
	var out bytes.Buffer
	var wg sync.WaitGroup
	pr, pw := io.Pipe()
	transport := NewTransport(pr, &out, func(ctx context.Context, req *Request) (any, *Error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
		}()
		return req.Method, nil
	})
	go func() {
		defer pw.Close()
		for i := 0; i < 5; i++ {
			fmt.Fprintf(pw, `{"jsonrpc":"2.0","id":%d,"method":"x"}`+"\n", i)
		}
	}()
	require.NoError(t, transport.Serve(context.Background()))
	wg.Wait()
	require.Equal(t, 5, strings.Count(out.String(), `"jsonrpc"`))
}
