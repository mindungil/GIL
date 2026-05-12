package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/mcp"
	"github.com/mindungil/gil/core/specstore"
	"github.com/mindungil/gil/core/tool"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// chat_mcp_test.go — gap-2 wiring (MCP surface in chat-mode).
// The contract worth pinning is "chat agent registry sees the same
// MCP-advertised tools as the run agent loop, with one shared
// subprocess set per session".

// fakeCoreTool is a tool.Tool stub used to drive coreToolAdapter
// without spawning an MCP subprocess.
type fakeCoreTool struct {
	id      string
	content string
}

func (f *fakeCoreTool) Name() string             { return f.id }
func (f *fakeCoreTool) Description() string      { return "fake remote tool" }
func (f *fakeCoreTool) Schema() json.RawMessage  { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeCoreTool) Run(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: f.content}, nil
}

func TestCoreToolAdapter_PassesThroughNameAndResult(t *testing.T) {
	a := &coreToolAdapter{t: &fakeCoreTool{id: "fs.read", content: "hi"}}
	require.Equal(t, "fs.read", a.name())
	require.Equal(t, "fake remote tool", a.description())

	res, err := a.run(context.Background(), "any-session", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "hi", res.Content)
}

func TestEnsureSessionMCPTools_EmptyAllowlistNoLaunch(t *testing.T) {
	// spec with no MCP allowlist must return nil — no subprocess
	// spawn, no cache entry.
	rs := NewRunService(newTestRepo(t), t.TempDir(), nil)
	spec := &gilv1.FrozenSpec{Tools: &gilv1.Tools{}}
	got := rs.ensureSessionMCPTools(context.Background(), "s1", spec, "", nil)
	require.Empty(t, got)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	require.NotContains(t, rs.mcpClientCache, "s1")
}

func TestEnsureSessionMCPTools_NilSpecNoLaunch(t *testing.T) {
	rs := NewRunService(newTestRepo(t), t.TempDir(), nil)
	got := rs.ensureSessionMCPTools(context.Background(), "s1", nil, "", nil)
	require.Empty(t, got)
}

func TestEnsureSessionMCPTools_CacheHitReturnsCachedTools(t *testing.T) {
	// Pre-populate the cache with a fake launch result so the lazy
	// launcher path doesn't try to spawn subprocesses. ensureSession
	// MCPTools must return the cached tools verbatim.
	rs := NewRunService(newTestRepo(t), t.TempDir(), nil)
	cached := &mcpLaunchResult{
		Tools: []tool.Tool{&fakeCoreTool{id: "cached.tool"}},
	}
	rs.mu.Lock()
	rs.mcpClientCache["s1"] = cached
	rs.mu.Unlock()

	spec := &gilv1.FrozenSpec{Tools: &gilv1.Tools{McpServers: []string{"fs"}}}
	got := rs.ensureSessionMCPTools(context.Background(), "s1", spec, "/work", nil)
	require.Len(t, got, 1)
	require.Equal(t, "cached.tool", got[0].Name())
}

func TestAppendChatMCPTools_NoRunServiceIsNoop(t *testing.T) {
	registry := &chatToolRegistry{tools: []chatTool{&toolShowStatus{}}}
	got := appendChatMCPTools(context.Background(), registry, nil, "s1", "/base")
	require.Equal(t, registry, got)
	require.Len(t, got.tools, 1)
}

func TestAppendChatMCPTools_NoSpecIsNoop(t *testing.T) {
	// Session base exists but no spec.yaml has been frozen yet.
	base := t.TempDir()
	rs := NewRunService(newTestRepo(t), base, nil)
	registry := &chatToolRegistry{tools: []chatTool{&toolShowStatus{}}}
	got := appendChatMCPTools(context.Background(), registry, rs, "s1", base)
	require.Len(t, got.tools, 1, "registry untouched when no spec exists")
}

func TestAppendChatMCPTools_SpecWithoutAllowlistIsNoop(t *testing.T) {
	// Spec is frozen but Tools.McpServers is empty — chat registry
	// stays untouched.
	base := t.TempDir()
	sid := "01ABCDEF"
	store := specstore.NewStore(filepath.Join(base, sid))
	require.NoError(t, store.Save(&gilv1.FrozenSpec{
		Goal:      &gilv1.Goal{OneLiner: "test"},
		Workspace: &gilv1.Workspace{Path: "/tmp"},
		Tools:     &gilv1.Tools{},
	}))
	require.NoError(t, store.Freeze())

	rs := NewRunService(newTestRepo(t), base, nil)
	registry := &chatToolRegistry{tools: []chatTool{&toolShowStatus{}}}
	got := appendChatMCPTools(context.Background(), registry, rs, sid, base)
	require.Len(t, got.tools, 1)
}

func TestAppendChatMCPTools_AllowlistPullsFromCache(t *testing.T) {
	// Spec names a server; ensureSessionMCPTools would launch on
	// real call, but we pre-seed the cache so the test stays
	// subprocess-free. The registry should gain one adapter entry.
	base := t.TempDir()
	sid := "01CACHED1"
	store := specstore.NewStore(filepath.Join(base, sid))
	require.NoError(t, store.Save(&gilv1.FrozenSpec{
		Goal:      &gilv1.Goal{OneLiner: "test"},
		Workspace: &gilv1.Workspace{Path: "/tmp"},
		Tools:     &gilv1.Tools{McpServers: []string{"fs"}},
	}))
	require.NoError(t, store.Freeze())

	rs := NewRunService(newTestRepo(t), base, nil)
	rs.mu.Lock()
	rs.mcpClientCache[sid] = &mcpLaunchResult{
		Tools: []tool.Tool{&fakeCoreTool{id: "fs.list"}},
	}
	rs.mu.Unlock()

	registry := &chatToolRegistry{tools: []chatTool{&toolShowStatus{}}}
	got := appendChatMCPTools(context.Background(), registry, rs, sid, base)
	require.Len(t, got.tools, 2)
	require.Equal(t, "fs.list", got.tools[1].name())
}

// Sanity: an actual MCP subprocess test is gated by the binary
// being available + the host having `go` in PATH; we leave the
// real-subprocess coverage to core/mcp/client_test.go's existing
// TestClient_LaunchRealSubprocess and pin the integration boundary
// at the adapter + cache layer here.
var _ = mcp.LoadAllTools // import sanity check
