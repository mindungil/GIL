package service

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/mcp"
	"github.com/mindungil/gil/core/mcpregistry"
	"github.com/mindungil/gil/core/tool"
)

// mergeMCPServers returns a single map combining spec-pinned servers and
// registry-resolved servers. Spec entries always win on name collision —
// they are scoped to this run (came in via the frozen spec) so they are
// strictly more specific than the user-wide registry.
//
// Both inputs may be nil; the result is always non-nil so call sites can
// iterate without a nil-guard. Returned values are copies of the inputs;
// mutating the result does not affect either source map.
//
// The function deliberately lives in its own file (and is a pure function
// over maps) so run.go can stay focused on lifecycle wiring while the
// merge logic gets exhaustive unit coverage in run_mcp_test.go.
func mergeMCPServers(spec, registry map[string]mcpregistry.Server) map[string]mcpregistry.Server {
	out := make(map[string]mcpregistry.Server, len(spec)+len(registry))
	// Registry first; spec overwrites on collision.
	for name, s := range registry {
		s.Name = name
		out[name] = s
	}
	for name, s := range spec {
		s.Name = name
		out[name] = s
	}
	return out
}

// shadowedRegistryNames returns the sorted list of registry names that the
// spec shadowed in a merge. Used by run.go to emit a single observability
// event so the user sees which registry entries were overridden by the
// frozen spec on a given run.
//
// Returned slice is empty (never nil) when there is no shadow; ordering is
// stable so the event payload diffs cleanly across reruns.
func shadowedRegistryNames(spec, registry map[string]mcpregistry.Server) []string {
	if len(spec) == 0 || len(registry) == 0 {
		return []string{}
	}
	out := make([]string, 0)
	for name := range registry {
		if _, ok := spec[name]; ok {
			out = append(out, name)
		}
	}
	// Sort in-place for stability without pulling in sort here — the
	// caller already imports it for other purposes, but keep this helper
	// dependency-free by doing a tiny insertion sort. The expected size
	// is small (registry rarely exceeds a handful of entries).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// mcpLaunchResult captures the per-server outcome of launchMCPServers so
// run.go can both append the new tools to the agent loop and emit a
// single summary event. The Clients slice owns the subprocess lifetime;
// the caller MUST defer Close() on each entry when the run unwinds.
type mcpLaunchResult struct {
	Clients []*mcp.Client
	Tools   []tool.Tool
	// Launched lists the names whose subprocess + initialize + tools/list
	// all succeeded. Used by the summary event payload.
	Launched []string
	// Failed maps name → error string for servers that resolved but did
	// not complete the handshake. Surfaced per-server via mcpServerLaunchFailed
	// events and re-summarised in mcpToolsRegistered for one-shot view.
	Failed map[string]string
}

// mcpLauncher matches mcp.LoadAllTools's shape so tests can inject a fake
// without spawning subprocesses. Production code passes mcp.LoadAllTools
// directly; tests construct a stub that returns canned clients and tools.
type mcpLauncher func(ctx context.Context, opts mcp.LaunchOptions) (*mcp.Client, []tool.Tool, error)

// launchMCPServers walks the spec allowlist against the merged registry
// and launches one MCP subprocess per allowed entry. Each launched
// server's tools are exposed through the returned slice so the agent
// loop can call them like any other tool.Tool.
//
// Semantics:
//   - allowlist is spec.Tools.McpServers (opt-in). When empty, no
//     launches occur — the loop expects callers to skip emitting the
//     summary event entirely in that case.
//   - Unknown names (in allowlist but not in merged) emit
//     `mcp_server_not_in_registry` so typos are visible immediately.
//   - http transport is not yet implemented by core/mcp; entries are
//     skipped with `mcp_server_http_unsupported` rather than failing
//     the whole run.
//   - Stdio launches that fail at any step (spawn, initialize,
//     tools/list) emit `mcp_server_launch_failed` and are then skipped.
//     Other servers continue — one bad server should not down the run.
//
// The stream argument may be nil during unit tests; events are simply
// dropped in that case so the caller can assert purely against the
// returned struct.
func launchMCPServers(
	ctx context.Context,
	merged map[string]mcpregistry.Server,
	allowlist []string,
	workspaceDir string,
	stream *event.Stream,
	launcher mcpLauncher,
) mcpLaunchResult {
	res := mcpLaunchResult{
		Launched: []string{},
		Failed:   map[string]string{},
	}
	if len(allowlist) == 0 {
		return res
	}
	if launcher == nil {
		launcher = mcp.LoadAllTools
	}

	emit := func(typ string, payload map[string]any) {
		if stream == nil {
			return
		}
		data, _ := json.Marshal(payload)
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now().UTC(),
			Source:    event.SourceSystem,
			Kind:      event.KindNote,
			Type:      typ,
			Data:      data,
		})
	}

	for _, name := range allowlist {
		srv, ok := merged[name]
		if !ok {
			emit("mcp_server_not_in_registry", map[string]any{"name": name})
			continue
		}
		switch srv.Type {
		case "stdio":
			env := make([]string, 0, len(srv.Env))
			for k, v := range srv.Env {
				env = append(env, k+"="+v)
			}
			// Stable env order keeps the launched subprocess deterministic
			// across reruns — helps reproducibility when the MCP server
			// itself reads ordering-sensitive env vars.
			sort.Strings(env)
			cli, ts, err := launcher(ctx, mcp.LaunchOptions{
				Command: srv.Command,
				Args:    srv.Args,
				Env:     env,
				Dir:     workspaceDir,
			})
			if err != nil {
				res.Failed[name] = err.Error()
				emit("mcp_server_launch_failed", map[string]any{
					"name": name,
					"err":  err.Error(),
				})
				continue
			}
			res.Clients = append(res.Clients, cli)
			res.Tools = append(res.Tools, ts...)
			res.Launched = append(res.Launched, name)
			toolNames := make([]string, 0, len(ts))
			for _, t := range ts {
				toolNames = append(toolNames, t.Name())
			}
			emit("mcp_server_launched", map[string]any{
				"name":       name,
				"tool_count": len(ts),
				"tools":      toolNames,
			})
		case "http":
			emit("mcp_server_http_unsupported", map[string]any{"name": name})
		default:
			emit("mcp_server_launch_failed", map[string]any{
				"name": name,
				"err":  "unknown transport: " + srv.Type,
			})
			res.Failed[name] = "unknown transport: " + srv.Type
		}
	}

	sort.Strings(res.Launched)
	if len(res.Launched) > 0 || len(res.Failed) > 0 {
		failedNames := make([]string, 0, len(res.Failed))
		for n := range res.Failed {
			failedNames = append(failedNames, n)
		}
		sort.Strings(failedNames)
		emit("mcp_tools_registered", map[string]any{
			"launched":         res.Launched,
			"failed":           failedNames,
			"total_tool_count": len(res.Tools),
		})
	}
	return res
}
