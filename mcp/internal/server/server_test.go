package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/mcp/jsonrpc"
)

// Tests that don't need a real gild — exercise the dispatcher with a nil Client
// and only call methods that don't reach Client.

func TestServer_Initialize(t *testing.T) {
	s := &Server{}
	res, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "initialize"})
	require.Nil(t, err)
	m, ok := res.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "2024-11-05", m["protocolVersion"])
	info := m["serverInfo"].(map[string]any)
	require.Equal(t, "gil-mcp", info["name"])
}

func TestServer_NotificationsInitialized_NilResponse(t *testing.T) {
	s := &Server{}
	res, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "notifications/initialized"})
	require.Nil(t, err)
	require.Nil(t, res)
}

func TestServer_ToolsList(t *testing.T) {
	s := &Server{}
	res, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "tools/list"})
	require.Nil(t, err)
	m := res.(map[string]any)
	tools := m["tools"].([]toolDef)
	require.Len(t, tools, 3)
	names := []string{tools[0].Name, tools[1].Name, tools[2].Name}
	require.Contains(t, names, "list_sessions")
	require.Contains(t, names, "get_session")
	require.Contains(t, names, "create_session")
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	s := &Server{}
	p, _ := json.Marshal(map[string]any{"name": "nope", "arguments": map[string]any{}})
	_, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "tools/call", Params: p})
	require.NotNil(t, err)
	require.Equal(t, jsonrpc.CodeMethodNotFound, err.Code)
}

func TestServer_UnknownMethod(t *testing.T) {
	s := &Server{}
	_, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "garbage"})
	require.NotNil(t, err)
	require.Equal(t, jsonrpc.CodeMethodNotFound, err.Code)
}

func TestServer_ToolsCall_BadParams(t *testing.T) {
	s := &Server{}
	_, err := s.Handle(context.Background(), &jsonrpc.Request{Method: "tools/call", Params: json.RawMessage(`{not json`)})
	require.NotNil(t, err)
	require.Equal(t, jsonrpc.CodeInvalidParams, err.Code)
}
