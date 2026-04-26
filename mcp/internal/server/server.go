// Package server implements the MCP method dispatcher for the gilmcp adapter.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jedutools/gil/mcp/internal/jsonrpc"
	"github.com/jedutools/gil/sdk"
)

// Server is a single-server MCP adapter backed by a gil SDK client.
type Server struct {
	Client *sdk.Client
}

// Handle is the top-level JSON-RPC dispatcher.
func (s *Server) Handle(ctx context.Context, req *jsonrpc.Request) (any, *jsonrpc.Error) {
	switch req.Method {
	case "initialize":
		return s.initialize(ctx, req)
	case "notifications/initialized":
		return nil, nil // no response
	case "tools/list":
		return s.toolsList(ctx, req)
	case "tools/call":
		return s.toolsCall(ctx, req)
	default:
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unknown method: " + req.Method}
	}
}

func (s *Server) initialize(_ context.Context, _ *jsonrpc.Request) (any, *jsonrpc.Error) {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "gil-mcp",
			"version": "0.1.0",
		},
	}, nil
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) toolsList(_ context.Context, _ *jsonrpc.Request) (any, *jsonrpc.Error) {
	return map[string]any{
		"tools": []toolDef{
			{
				Name:        "list_sessions",
				Description: "List recent gil sessions.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"type": "integer", "default": 20},
					},
				},
			},
			{
				Name:        "get_session",
				Description: "Get details for one gil session by ID.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"session_id": map[string]any{"type": "string"},
					},
					"required": []string{"session_id"},
				},
			},
			{
				Name:        "create_session",
				Description: "Create a new gil session in the given working directory.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"working_dir": map[string]any{"type": "string"},
						"goal_hint":   map[string]any{"type": "string"},
					},
					"required": []string{"working_dir"},
				},
			},
		},
	}, nil
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) toolsCall(ctx context.Context, req *jsonrpc.Request) (any, *jsonrpc.Error) {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	switch p.Name {
	case "list_sessions":
		return s.callListSessions(ctx, p.Arguments)
	case "get_session":
		return s.callGetSession(ctx, p.Arguments)
	case "create_session":
		return s.callCreateSession(ctx, p.Arguments)
	default:
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unknown tool: " + p.Name}
	}
}

func (s *Server) callListSessions(ctx context.Context, args json.RawMessage) (any, *jsonrpc.Error) {
	var a struct {
		Limit int `json:"limit"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	if a.Limit <= 0 {
		a.Limit = 20
	}
	sessions, err := s.Client.ListSessions(ctx, a.Limit)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return mcpToolResult(sessionsText(sessions)), nil
}

func (s *Server) callGetSession(ctx context.Context, args json.RawMessage) (any, *jsonrpc.Error) {
	var a struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	if a.SessionID == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "session_id required"}
	}
	sess, err := s.Client.GetSession(ctx, a.SessionID)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return mcpToolResult(fmt.Sprintf("ID: %s\nStatus: %s\nWorking dir: %s\nGoal: %s", sess.ID, sess.Status, sess.WorkingDir, sess.GoalHint)), nil
}

func (s *Server) callCreateSession(ctx context.Context, args json.RawMessage) (any, *jsonrpc.Error) {
	var a struct {
		WorkingDir string `json:"working_dir"`
		GoalHint   string `json:"goal_hint"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	if a.WorkingDir == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "working_dir required"}
	}
	sess, err := s.Client.CreateSession(ctx, sdk.CreateOptions{WorkingDir: a.WorkingDir, GoalHint: a.GoalHint})
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	return mcpToolResult("Created session " + sess.ID), nil
}

// mcpToolResult wraps a string in the MCP tool-call response shape.
func mcpToolResult(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": text,
		}},
	}
}

func sessionsText(sessions []*sdk.Session) string {
	if len(sessions) == 0 {
		return "(no sessions)"
	}
	var buf strings.Builder
	for _, s := range sessions {
		fmt.Fprintf(&buf, "%s  %s  %s\n", s.ID, s.Status, s.GoalHint)
	}
	return buf.String()
}
