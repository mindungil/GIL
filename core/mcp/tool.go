package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedutools/gil/core/tool"
)

// RemoteTool wraps a single MCP-server-advertised tool as a core/tool.Tool.
// All calls go through the shared Client (one client per server, many tools).
type RemoteTool struct {
	Client *Client
	Info   RemoteToolInfo
}

func (r *RemoteTool) Name() string        { return r.Info.Name }
func (r *RemoteTool) Description() string { return r.Info.Description }
func (r *RemoteTool) Schema() json.RawMessage {
	b, err := json.Marshal(r.Info.InputSchema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return b
}

func (r *RemoteTool) Run(ctx context.Context, argsJSON json.RawMessage) (tool.Result, error) {
	var args any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return tool.Result{}, fmt.Errorf("mcp.RemoteTool unmarshal: %w", err)
		}
	}
	text, isError, err := r.Client.CallTool(ctx, r.Info.Name, args)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: text, IsError: isError}, nil
}

// LoadAllTools dials the MCP server, lists its tools, and returns them
// wrapped as core/tool.Tool instances. The caller is responsible for
// calling Close() on the returned Client when done. Returns both the
// Client (for subprocess lifetime management) and the tools slice.
func LoadAllTools(ctx context.Context, opts LaunchOptions) (*Client, []tool.Tool, error) {
	cli, err := Launch(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if err := cli.Initialize(ctx); err != nil {
		_ = cli.Close()
		return nil, nil, fmt.Errorf("mcp initialize: %w", err)
	}
	infos, err := cli.ListTools(ctx)
	if err != nil {
		_ = cli.Close()
		return nil, nil, fmt.Errorf("mcp list tools: %w", err)
	}
	out := make([]tool.Tool, 0, len(infos))
	for _, info := range infos {
		out = append(out, &RemoteTool{Client: cli, Info: info})
	}
	return cli, out, nil
}
