// Package mcp implements an MCP client that launches a stdio MCP server
// subprocess, discovers its tools, and exposes them as core/tool.Tool
// instances.
//
// Lifted from Goose's goose-mcp/src/subprocess.rs (subprocess management
// pattern). Simplified: no Windows CREATE_NO_WINDOW (POSIX-first), no
// login-shell PATH resolution (caller's PATH is used as-is).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mindungil/gil/core/mcp/jsonrpc"
)

// Client speaks JSON-RPC 2.0 to a stdio MCP server subprocess.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader

	nextID  atomic.Int64
	pending sync.Map // id -> chan *jsonrpc.Response

	writeMu sync.Mutex
	closed  atomic.Bool
	done    chan struct{}
}

// LaunchOptions configures Launch.
type LaunchOptions struct {
	Command string   // executable
	Args    []string // args (no shell expansion)
	Env     []string // additional env vars (KEY=VALUE); inherited from parent if nil
	Dir     string   // working dir; inherited if empty
}

// Launch spawns the MCP server subprocess and starts the read pump.
// The caller MUST call Close to terminate the subprocess.
func Launch(ctx context.Context, opts LaunchOptions) (*Client, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("mcp.Launch: Command required")
	}
	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	cmd.Env = append(cmd.Environ(), opts.Env...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp.Launch stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp.Launch stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp.Launch start: %w", err)
	}

	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		done:   make(chan struct{}),
	}
	go c.readPump()
	return c, nil
}

// readPump scans the subprocess stdout for newline-delimited JSON responses.
// Each response with an ID is delivered to the matching pending channel.
func (c *Client) readPump() {
	defer close(c.done)
	decoder := json.NewDecoder(c.stdout)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			// EOF or bad data — exit pump
			return
		}
		var resp jsonrpc.Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue // malformed; skip
		}
		if len(resp.ID) == 0 {
			continue // notifications from server (ignore)
		}
		if ch, ok := c.pending.LoadAndDelete(string(resp.ID)); ok {
			ch.(chan *jsonrpc.Response) <- &resp
		}
	}
}

// Call sends a JSON-RPC request and waits up to timeout for the response.
// Returns the result JSON or an error (transport, RPC, or timeout).
func (c *Client) Call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("mcp.Client: closed")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	id := c.nextID.Add(1)
	idJSON, _ := json.Marshal(id)
	paramsJSON, _ := json.Marshal(params)

	req := jsonrpc.Request{
		JSONRPC: "2.0", ID: idJSON, Method: method, Params: paramsJSON,
	}
	payload, _ := json.Marshal(req)
	payload = append(payload, '\n')

	ch := make(chan *jsonrpc.Response, 1)
	c.pending.Store(string(idJSON), ch)
	defer c.pending.Delete(string(idJSON))

	c.writeMu.Lock()
	_, werr := c.stdin.Write(payload)
	c.writeMu.Unlock()
	if werr != nil {
		return nil, fmt.Errorf("mcp.Call write: %w", werr)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp.Call %s: %w", method, resp.Error)
		}
		return json.Marshal(resp.Result)
	case <-time.After(timeout):
		return nil, fmt.Errorf("mcp.Call %s: timeout after %s", method, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	if c.closed.Load() {
		return fmt.Errorf("mcp.Client: closed")
	}
	paramsJSON, _ := json.Marshal(params)
	req := jsonrpc.Request{JSONRPC: "2.0", Method: method, Params: paramsJSON}
	payload, _ := json.Marshal(req)
	payload = append(payload, '\n')
	c.writeMu.Lock()
	_, err := c.stdin.Write(payload)
	c.writeMu.Unlock()
	return err
}

// Initialize performs the MCP initialize handshake.
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gil", "version": "0.1.0"},
	}, 10*time.Second)
	if err != nil {
		return err
	}
	return c.Notify("notifications/initialized", map[string]any{})
}

// RemoteToolInfo holds the metadata for one tool advertised by the MCP server.
type RemoteToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListTools returns the tools advertised by the server.
func (c *Client) ListTools(ctx context.Context) ([]RemoteToolInfo, error) {
	raw, err := c.Call(ctx, "tools/list", map[string]any{}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []RemoteToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp.ListTools unmarshal: %w", err)
	}
	return resp.Tools, nil
}

// CallTool invokes one remote tool. Returns the text content concatenated
// from the response (MCP responses are typically `content: [{type:"text",text:"..."}]`).
func (c *Client) CallTool(ctx context.Context, name string, args any) (string, bool, error) {
	raw, err := c.Call(ctx, "tools/call", map[string]any{
		"name": name, "arguments": args,
	}, 60*time.Second)
	if err != nil {
		return "", false, err
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", false, fmt.Errorf("mcp.CallTool unmarshal: %w", err)
	}
	var text string
	for _, item := range resp.Content {
		if item.Type == "text" {
			text += item.Text
		}
	}
	return text, resp.IsError, nil
}

// Close terminates the subprocess and waits for the read pump to finish.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.done
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
	return nil
}
