package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/mcp"
	"github.com/mindungil/gil/core/mcpregistry"
	"github.com/mindungil/gil/core/tool"
)

func TestMergeMCPServers_RegistryOnly(t *testing.T) {
	registry := map[string]mcpregistry.Server{
		"fs":     {Type: "stdio", Command: "echo", Args: []string{"hi"}},
		"issues": {Type: "http", URL: "https://x.example.com/mcp"},
	}
	got := mergeMCPServers(nil, registry)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(got), got)
	}
	if got["fs"].Command != "echo" {
		t.Errorf("fs.Command = %q, want echo", got["fs"].Command)
	}
	if got["issues"].URL != "https://x.example.com/mcp" {
		t.Errorf("issues.URL = %q, want https://x.example.com/mcp", got["issues"].URL)
	}
	// Name field is stamped from the map key.
	if got["fs"].Name != "fs" {
		t.Errorf("expected Name to be stamped from key, got %q", got["fs"].Name)
	}
}

func TestMergeMCPServers_SpecOnly(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"override": {Type: "stdio", Command: "spec-cmd"},
	}
	got := mergeMCPServers(spec, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got["override"].Command != "spec-cmd" {
		t.Errorf("Command = %q, want spec-cmd", got["override"].Command)
	}
}

func TestMergeMCPServers_SpecWinsOnCollision(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	registry := map[string]mcpregistry.Server{
		"fs":     {Type: "stdio", Command: "registry-fs"},
		"issues": {Type: "http", URL: "https://x.example.com/mcp"},
	}
	got := mergeMCPServers(spec, registry)
	if got["fs"].Command != "spec-fs" {
		t.Errorf("expected spec to win, got Command = %q", got["fs"].Command)
	}
	if got["issues"].URL == "" {
		t.Errorf("non-colliding registry entry should remain, got: %+v", got["issues"])
	}
}

func TestMergeMCPServers_BothNil(t *testing.T) {
	got := mergeMCPServers(nil, nil)
	if got == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestMergeMCPServers_DoesNotMutateInputs(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	specCopy := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "spec-fs"},
	}
	registry := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "registry-fs"},
	}
	registryCopy := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "registry-fs"},
	}
	_ = mergeMCPServers(spec, registry)
	if !reflect.DeepEqual(spec, specCopy) {
		t.Errorf("spec mutated: %v vs %v", spec, specCopy)
	}
	if !reflect.DeepEqual(registry, registryCopy) {
		t.Errorf("registry mutated: %v vs %v", registry, registryCopy)
	}
}

func TestShadowedRegistryNames_Empty(t *testing.T) {
	if got := shadowedRegistryNames(nil, nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	if got := shadowedRegistryNames(map[string]mcpregistry.Server{"a": {}}, nil); len(got) != 0 {
		t.Errorf("expected empty when registry empty, got %v", got)
	}
	if got := shadowedRegistryNames(nil, map[string]mcpregistry.Server{"a": {}}); len(got) != 0 {
		t.Errorf("expected empty when spec empty, got %v", got)
	}
}

// fakeMCPTool is a minimal tool.Tool stub used by launchMCPServers
// tests to assert the launched tools surface through to the agent loop.
type fakeMCPTool struct{ name string }

func (f *fakeMCPTool) Name() string                 { return f.name }
func (f *fakeMCPTool) Description() string          { return "fake mcp tool" }
func (f *fakeMCPTool) Schema() json.RawMessage      { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeMCPTool) Run(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

// collectTypes drains the subscription channel until it closes and
// returns the observed event types in order. The caller must Close the
// subscription after launchMCPServers returns so the channel drains.
func collectTypes(sub *event.Subscription) []string {
	var out []string
	for ev := range sub.Events() {
		out = append(out, ev.Type)
	}
	return out
}

func TestLaunchMCPServers_EmptyAllowlistIsNoop(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "echo"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(8)

	res := launchMCPServers(context.Background(), merged, nil, "", stream, func(_ context.Context, _ mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		t.Fatal("launcher must not be invoked with empty allowlist")
		return nil, nil, nil
	})

	require.Empty(t, res.Clients)
	require.Empty(t, res.Tools)
	require.Empty(t, res.Launched)
	require.Empty(t, res.Failed)

	sub.Close()
	require.Empty(t, collectTypes(sub), "no events should be emitted")
}

func TestLaunchMCPServers_StdioServerLaunchedAndToolsSurfaced(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "/usr/bin/fs-mcp", Args: []string{"--root", "."}},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(16)

	var capturedOpts mcp.LaunchOptions
	res := launchMCPServers(context.Background(), merged, []string{"fs"}, "/work", stream, func(_ context.Context, opts mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		capturedOpts = opts
		return &mcp.Client{}, []tool.Tool{&fakeMCPTool{name: "fs.read"}, &fakeMCPTool{name: "fs.write"}}, nil
	})

	require.Equal(t, []string{"fs"}, res.Launched)
	require.Empty(t, res.Failed)
	require.Len(t, res.Clients, 1)
	require.Len(t, res.Tools, 2)
	require.Equal(t, "fs.read", res.Tools[0].Name())
	require.Equal(t, "fs.write", res.Tools[1].Name())

	// Launcher receives the workspace dir, command, and args verbatim.
	require.Equal(t, "/usr/bin/fs-mcp", capturedOpts.Command)
	require.Equal(t, []string{"--root", "."}, capturedOpts.Args)
	require.Equal(t, "/work", capturedOpts.Dir)

	sub.Close()
	types := collectTypes(sub)
	require.Contains(t, types, "mcp_server_launched")
	require.Contains(t, types, "mcp_tools_registered")
}

func TestLaunchMCPServers_UnknownNameEmitsNotInRegistry(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"fs": {Type: "stdio", Command: "echo"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(8)

	called := false
	res := launchMCPServers(context.Background(), merged, []string{"typo"}, "", stream, func(_ context.Context, _ mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		called = true
		return nil, nil, nil
	})

	require.False(t, called, "launcher must not run for unknown names")
	require.Empty(t, res.Launched)
	require.Empty(t, res.Failed)

	sub.Close()
	types := collectTypes(sub)
	require.Equal(t, []string{"mcp_server_not_in_registry"}, types,
		"unknown names emit not_in_registry only, no summary since nothing launched/failed")
}

func TestLaunchMCPServers_LaunchFailureIsSoftAndIsolated(t *testing.T) {
	// Two servers in the allowlist. The first one fails to launch (e.g.,
	// missing binary). The second one must still launch — one bad
	// server can NEVER take down the whole run.
	merged := map[string]mcpregistry.Server{
		"bad":  {Type: "stdio", Command: "/no/such/bin"},
		"good": {Type: "stdio", Command: "/bin/echo"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(16)

	res := launchMCPServers(context.Background(), merged, []string{"bad", "good"}, "", stream, func(_ context.Context, opts mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		if opts.Command == "/no/such/bin" {
			return nil, nil, errors.New("exec: no such file")
		}
		return &mcp.Client{}, []tool.Tool{&fakeMCPTool{name: "good.ping"}}, nil
	})

	require.Equal(t, []string{"good"}, res.Launched)
	require.Contains(t, res.Failed, "bad")
	require.Len(t, res.Tools, 1)
	require.Equal(t, "good.ping", res.Tools[0].Name())

	sub.Close()
	types := collectTypes(sub)
	require.Contains(t, types, "mcp_server_launch_failed")
	require.Contains(t, types, "mcp_server_launched")
	require.Contains(t, types, "mcp_tools_registered")
}

func TestLaunchMCPServers_HTTPTransportSkippedWithEvent(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"web": {Type: "http", URL: "https://x.example.com/mcp"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(8)

	res := launchMCPServers(context.Background(), merged, []string{"web"}, "", stream, func(_ context.Context, _ mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		t.Fatal("http servers must not invoke the stdio launcher")
		return nil, nil, nil
	})

	require.Empty(t, res.Launched)
	require.Empty(t, res.Failed, "http skip is intentional, not a failure")

	sub.Close()
	types := collectTypes(sub)
	require.Equal(t, []string{"mcp_server_http_unsupported"}, types)
}

func TestLaunchMCPServers_UnknownTransportIsFailure(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"weird": {Type: "carrier-pigeon", Command: "fly"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(8)

	res := launchMCPServers(context.Background(), merged, []string{"weird"}, "", stream, nil)

	require.Empty(t, res.Launched)
	require.Contains(t, res.Failed, "weird")

	sub.Close()
	types := collectTypes(sub)
	require.Contains(t, types, "mcp_server_launch_failed")
	require.Contains(t, types, "mcp_tools_registered")
}

// Sanity: the summary event includes the launched + failed names in
// stable sorted order so observers can diff payloads across reruns.
func TestLaunchMCPServers_SummaryEventIsStable(t *testing.T) {
	merged := map[string]mcpregistry.Server{
		"zebra": {Type: "stdio", Command: "z"},
		"alpha": {Type: "stdio", Command: "a"},
	}
	stream := event.NewStream()
	sub := stream.Subscribe(16)

	_ = launchMCPServers(context.Background(), merged, []string{"zebra", "alpha"}, "", stream, func(_ context.Context, opts mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error) {
		return &mcp.Client{}, []tool.Tool{&fakeMCPTool{name: opts.Command + ".tool"}}, nil
	})

	sub.Close()
	var payload struct {
		Launched []string `json:"launched"`
	}
	for ev := range sub.Events() {
		if ev.Type == "mcp_tools_registered" {
			require.NoError(t, json.Unmarshal(ev.Data, &payload))
			break
		}
	}
	require.Equal(t, []string{"alpha", "zebra"}, payload.Launched)
}

func TestShadowedRegistryNames_SortedIntersection(t *testing.T) {
	spec := map[string]mcpregistry.Server{
		"zebra": {}, "alpha": {}, "mango": {}, "fresh-only": {},
	}
	registry := map[string]mcpregistry.Server{
		"alpha": {}, "mango": {}, "zebra": {}, "registry-only": {},
	}
	got := shadowedRegistryNames(spec, registry)
	want := []string{"alpha", "mango", "zebra"}
	if !sort.StringsAreSorted(got) {
		t.Errorf("output not sorted: %v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
