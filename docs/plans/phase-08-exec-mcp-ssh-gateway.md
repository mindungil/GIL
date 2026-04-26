# Phase 8 — Exec / MCP / SSH / HTTP Gateway

> Multi-step tool compression (`core/exec`, Hermes pattern), built-in MCP server adapter (`mcp/`, Goose pattern), SSH workspace backend, HTTP/JSON gateway for browser clients (grpc-gateway), and the first dogfood.

**Goal**: Eliminate context bloat from chains of small tool calls (exec compresses N intermediate results into one), let gil consume external MCP servers (Goose's 6 backends pattern), enable remote workspaces over SSH, expose the gRPC API as REST for browser/curl, and run gil on its own codebase.

**Architecture**: Each track is independent. The dogfood at the end is the integration test that proves Phases 1-8 work together on a real task.

---

## Track A — core/exec (Hermes-style multi-step compression)

### T1: core/exec.Recipe — declarative multi-step DSL

**Files**: `core/exec/recipe.go`, `core/exec/recipe_test.go`

**Reference to lift**: `/home/ubuntu/research/hermes-agent/tools/code_execution_tool.py` lines 1-200 (the script-execution skeleton). Hermes's pattern: agent writes a Python script that calls a small set of allowed RPCs (read_file/write_file/bash/...). The script runs in a subprocess; intermediate stdout doesn't enter the LLM's context — only the final result + a compact summary do.

For gil we adapt this without Python: a Recipe is a JSON-described sequence of tool calls + a final summary template. The recipe runs in the same process; intermediate tool_result events are emitted but NOT appended to messages — only one summary message is appended at the end.

```go
type Recipe struct {
    Steps    []RecipeStep
    Summary  string  // template using {{step_N_output}} / {{step_N_status}}
}
type RecipeStep struct {
    Tool string
    Args json.RawMessage
}
```

Define `Run(ctx, recipe, tools, eventEmitter) (summary string, err error)`.

Tests cover: linear sequence, summary template substitution, step error handling.

Commit: `feat(core/exec): Recipe + Runner (Hermes execute_code lift)`

### T2: exec tool — agent-callable wrapper

**Files**: `core/tool/exec.go`, `core/tool/exec_test.go`

```go
type Exec struct {
    Tools []Tool
    Emit  func(name string, kind string, data map[string]any)
}
// Tool name: "exec"
// Args: { "recipe": {...} }
// Returns: the recipe's summary as the tool result.
```

The agent emits a Recipe; gil runs it; the LLM sees only the summary back. Replaces 5-10 individual tool calls with 1.

RunService wires this with the full tool list (excluding exec itself, to prevent recursion) plus the Events stream.

Commit: `feat(core/tool): exec tool — multi-step compression`

---

## Track B — mcp/ built-in MCP backend

### T3: mcp/server — gil's gRPC services exposed as MCP

**Files**: `mcp/cmd/gilmcp/main.go`, `mcp/internal/adapter/handler.go`

**Reference**: `/home/ubuntu/research/goose/crates/goose-mcp/src/lib.rs` (64 lines — small) for the MCP server skeleton. MCP is a JSON-RPC over stdio (or HTTP) protocol; servers expose `tools/list` and `tools/call`. We map gil's tools to MCP tools so external MCP clients (Claude Desktop, etc.) can use gil as a backend.

Phase 8 scope: stdio transport only (newline-delimited JSON-RPC); HTTP transport is Phase 9.

```go
// Tools exposed: those that don't require workspace state — start_session,
// list_sessions, get_session, get_events. Run/edit/etc. require a session
// context which MCP doesn't have a clean way to pass.
```

Use `github.com/modelcontextprotocol/go-sdk` if available; otherwise hand-roll JSON-RPC handler.

Commit: `feat(mcp): MCP server adapter exposing gil sessions`

### T4: mcp/client — gil consumes external MCP servers

**Files**: `core/mcp/client.go`, `core/mcp/client_test.go`

**Reference**: Goose's `goose-mcp/src/subprocess.rs` (113 lines) for the spawn-and-communicate pattern.

MCPClient launches an MCP server subprocess (stdio), discovers its tools via `tools/list`, exposes them as `tool.Tool` instances. RunService can then expose external tools (e.g., a postgres MCP, slack MCP) to the agent.

Commit: `feat(core/mcp): client launches stdio MCP servers + exposes tools`

---

## Track C — SSH workspace backend

### T5: runtime/ssh.Wrapper — ssh exec wrapper

**Files**: `runtime/ssh/wrap.go`, `runtime/ssh/wrap_test.go`

Mirrors `runtime/docker.Wrapper` shape:
```go
type Wrapper struct {
    Host    string  // user@host
    SSHBin  string  // defaults to "ssh"
    KeyPath string  // optional -i <path>
    Port    int     // optional -p <port>
}
func (w *Wrapper) Wrap(cmd string, args ...string) []string
```

Layout: `["ssh", "-i", key, "-p", port, "user@host", cmd, args...]`.

For file ops (write_file/read_file), Phase 8 keeps them local (assumes SSH is for a remote shell only; remote file ops are Phase 9 with rsync sync). Document this limitation.

RunService SSH branch wires this when `spec.workspace.backend == SSH` and `spec.workspace.path` parsed as `user@host[:port][/keypath]`.

Commit: `feat(runtime/ssh): ssh workspace wrapper`

---

## Track D — HTTP/JSON gateway

### T6: grpc-gateway integration

**Files**: `proto/gil/v1/*.proto` (add HTTP annotations), `server/cmd/gild/main.go` (mux gateway)

Add `option (google.api.http)` annotations to RPCs:
```proto
rpc List(ListRequest) returns (ListResponse) {
    option (google.api.http) = {
        get: "/v1/sessions"
    };
}
```

Run protoc-gen-grpc-gateway during `buf generate`. Mount the gateway mux at `--http :8080` flag on gild. Browser clients can now `curl http://localhost:8080/v1/sessions`.

Commit: `feat(proto+server): HTTP/JSON gateway via grpc-gateway`

---

## Track E — First dogfood + integration

### T7: gil 자체 기능 추가를 gil로

The first real autonomous task: have gil add a small feature to itself (e.g., `gil sessions delete <id>` — a missing CLI command). Steps:
1. `gil new --working-dir /home/ubuntu/gil`
2. `gil interview <id>` answering questions about the goal
3. `gil run <id>` autonomous run
4. Inspect the generated commit for quality

This isn't a test script — it's a manual demo. Document the procedure + capture the output in `docs/dogfood/2026-04-26-gil-sessions-delete.md`.

Commit: `docs(dogfood): first autonomous gil-on-gil run`

### T8: e2e8 — exec + mcp + ssh sanity

**Files**: `tests/e2e/phase08_test.sh`, Makefile

Mock provider scripts: call `exec` with a 3-step recipe (write_file → read_file → bash echo), assert summary contains expected substrings. SSH and MCP paths skip when not available locally.

Commit: `test(e2e): phase 8 — exec + mcp + ssh sanity`

### T9: progress.md Phase 8 update

Mark complete + outcomes summary.

Commit: `docs(progress): Phase 8 complete`

---

## Phase 8 완료 체크리스트

- [ ] `make e2e-all` 8 phase 통과
- [ ] core/exec recipe + tool 작동
- [ ] mcp 서버 stdio 작동 (외부 MCP 클라이언트가 gil 사용 가능)
- [ ] mcp 클라이언트 (gil이 외부 MCP 사용 가능)
- [ ] SSH 백엔드 작동
- [ ] HTTP gateway curl 가능
- [ ] 첫 dogfood 1회 완료 + 문서화

## Phase 9 미루는 항목

- 클라우드 VM 백엔드 (Modal/Daytona)
- VS Code 확장
- 다중 사용자
- Atropos RL 통합
- 며칠 무인 시뮬레이션
