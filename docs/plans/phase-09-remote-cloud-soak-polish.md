# Phase 9 — Remote sync, Cloud backends, Soak, Polish

> SSH workspace gets remote file sync (closes Phase 8 limitation). Cloud VM backends (Modal/Daytona) get scaffolded. Multi-day soak simulation proves stability under realistic patterns. Observability adds Prometheus metrics + multi-user data isolation. README + install guide polish for first-time users.

**Goal**: Lift the "Phase 8 file ops stay local" SSH limitation. Set up cloud-backend interfaces so external teams can build adapters. Prove the system survives 200+ iterations without leaks or panics. Make the project newcomer-approachable.

**What we DON'T do here**: actual Anthropic-key-driven dogfood (deferred to user-driven verification per docs/dogfood/), real cloud deployments (Modal/Daytona require account credentials), multi-user OAuth (Phase 10), VS Code extension (separate Node.js project).

---

## Track A — SSH remote file sync (closes Phase 8 limitation)

### T1: runtime/ssh.Sync — rsync helpers

**Files**: `runtime/ssh/sync.go`, `runtime/ssh/sync_test.go`

```go
type Syncer struct {
    Wrapper *Wrapper  // shares Host/Port/KeyPath
    LocalDir string   // local workspace mirror
    RemoteDir string  // remote workspace path
    RsyncBin string   // defaults to "rsync"
    ExtraArgs []string  // e.g., ["--exclude=.git/"]
}

// Push copies LocalDir → host:RemoteDir.
func (s *Syncer) Push(ctx context.Context) error
// Pull copies host:RemoteDir → LocalDir.
func (s *Syncer) Pull(ctx context.Context) error
// Available reports whether rsync is in PATH.
func Available() bool
```

Use `rsync -az --delete -e "ssh -p PORT -i KEY" <src> <dst>`. Test with mock by overriding RsyncBin to a script that captures argv.

Commit: `feat(runtime/ssh): rsync-based workspace sync (Push/Pull)`

### T2: SSH backend uses Sync

**Files**: modify `server/internal/service/run.go`

When backend == SSH:
1. Before run: Push local workspace to remote
2. After run (in defer): Pull remote workspace back to local

Add config knobs to spec.workspace if needed (or use sensible defaults).

Commit: `feat(server): SSH backend Pushes before / Pulls after run`

---

## Track B — Cloud VM backend scaffolding

### T3: runtime/cloud package — shared interface

**Files**: `runtime/cloud/cloud.go`, `runtime/cloud/cloud_test.go`

```go
// Provider is the abstraction over cloud VM backends (Modal, Daytona, etc.).
type Provider interface {
    Name() string
    // Provision creates a fresh sandbox VM with the workspace mounted.
    // Returns a CommandWrapper that routes commands into the VM, plus a
    // Teardown closure to call when the run ends.
    Provision(ctx context.Context, opts ProvisionOptions) (Sandbox, error)
}

type ProvisionOptions struct {
    Image       string  // language-stack image, e.g., "python:3.12-slim"
    WorkspaceDir string // local dir to mount/sync
    Memory      string  // e.g., "4Gi"
    CPU         string  // e.g., "2"
}

type Sandbox struct {
    Wrapper  CommandWrapper  // satisfies core/tool.CommandWrapper
    Teardown func(context.Context) error
    Info     map[string]string  // provider-specific info (vm_id, region, etc.)
}
```

Tests: pure interface compliance check + a test stub Provider.

Commit: `feat(runtime/cloud): Provider interface for cloud VM backends`

### T4: runtime/modal — Modal driver stub

**Files**: `runtime/modal/modal.go`, `runtime/modal/modal_test.go`

Modal (https://modal.com) Python SDK doesn't have a Go client; we'd shell out to the `modal` CLI or call the REST API. For Phase 9 scaffold:
- Construct a Sandbox by shelling out to `modal run` (placeholder; not actually executed without credentials)
- Document the API + provide a stubbed implementation that returns ErrNotConfigured when `MODAL_TOKEN_ID`/`MODAL_TOKEN_SECRET` env vars aren't set
- Test: verify the stub gates correctly + produces the expected `modal run` argv

Commit: `feat(runtime/modal): Modal cloud VM driver scaffold`

### T5: runtime/daytona — Daytona driver stub

Same shape; Daytona uses a REST API. Stub returns ErrNotConfigured when `DAYTONA_API_KEY` not set. Document the planned API + provide a reference HTTP call sequence.

Commit: `feat(runtime/daytona): Daytona cloud VM driver scaffold`

### T6: WorkspaceBackend proto values + RunService routing

Add to `proto/gil/v1/spec.proto`:
```proto
enum WorkspaceBackend {
  ...
  MODAL = 6;
  DAYTONA = 7;
}
```

RunService recognizes them; if env vars not set, returns FailedPrecondition.

Commit: `feat(proto+server): MODAL + DAYTONA workspace backend routing`

---

## Track C — Multi-day soak simulation

### T7: Long-run mock provider

**Files**: modify `server/cmd/gild/main.go` — new mode `run-soak`

The mock provider scripts a 200-turn run that:
- Mostly does write_file and bash commands
- Periodically (every ~30 turns) loops the same tool call to trigger stuck detection
- Periodically calls compact_now to exercise the compactor
- Calls memory_update every ~10 turns

This validates that the system survives long runs without leaking goroutines, resources, or memory.

Commit: `feat(gild): run-soak mock mode for long-run sanity`

### T8: e2e9 soak test

**Files**: `tests/e2e/phase09_test.sh`, Makefile

Script:
- Run gild with `GIL_MOCK_MODE=run-soak`
- Inject frozen spec with MaxIterations=200, autonomy=FULL
- Run synchronously, capture wall time + final iteration count
- Assert: status=done OR max_iterations; iterations >= 100; no panic; events.jsonl > 1000 lines; memory bank populated

Should complete in <60s.

Commit: `test(e2e): phase 9 — multi-iteration soak sanity`

---

## Track D — Observability + multi-user

### T9: gild --user flag for data dir isolation

**Files**: modify `server/cmd/gild/main.go`

```go
user := flag.String("user", "", "user namespace; data dir becomes <base>/users/<user>")
// ...
if *user != "" {
    base = filepath.Join(*base, "users", *user)
}
```

Lets multiple gild instances coexist on one host without stepping on each other. Phase 10 will add real auth; this is just the directory split.

Update install docs to mention the flag.

Commit: `feat(gild): --user flag for per-user data isolation`

### T10: Prometheus metrics endpoint

**Files**: `server/internal/metrics/metrics.go`, modify `server/cmd/gild/main.go`

Add `prometheus/client_golang`. Expose:
- `gil_sessions_total{status="..."}`  (gauge)
- `gil_run_iterations_total` (counter)
- `gil_compact_done_total` (counter)
- `gil_stuck_detected_total{pattern="..."}` (counter)
- `gil_tool_calls_total{tool="...",result="ok|error"}` (counter)

`gild --metrics :9090` mounts the Prometheus handler on a separate listener.

Commit: `feat(server): Prometheus metrics endpoint (--metrics :PORT)`

---

## Track E — Documentation polish

### T11: README.md quickstart

Replace the current minimal README with:
- Project status banner ("Phase 9, e2e green for 9 phases")
- 3-line install: `git clone && make build && export ANTHROPIC_API_KEY=...`
- 30-line quickstart: gild start, gil new, gil interview, gil run
- Architecture diagram (ASCII)
- Pointers to docs/design.md, docs/install.md, docs/dogfood/

Commit: `docs: README quickstart + architecture diagram`

### T12: docs/install.md

Step-by-step install for Linux / macOS:
- Build from source
- Install to /usr/local/bin via `make install`
- Anthropic key setup
- Optional: bwrap (Linux) / sandbox-exec (macOS) / docker / ssh / rsync
- TUI usage
- MCP integration with Claude Desktop

Commit: `docs: install guide`

### T13: Phase 9 progress update

Mark complete + summary.

Commit: `docs(progress): Phase 9 complete`

---

## Phase 9 완료 체크리스트

- [ ] `make e2e-all` 9 phase 통과
- [ ] SSH backend file ops 작동 (rsync)
- [ ] Cloud backends scaffolded (Modal + Daytona stubs)
- [ ] Soak test 200+ iter no panic
- [ ] Multi-user dir isolation (`--user`)
- [ ] Prometheus metrics endpoint
- [ ] README + install docs

## Phase 10 (미래) 미루는 항목

- 실제 Anthropic-driven dogfood (user-driven, ANTHROPIC_API_KEY 필요)
- Modal/Daytona 실제 deployment (계정 필요)
- VS Code 확장 (별도 Node.js 프로젝트)
- OAuth multi-user
- Atropos RL 통합
- 공식 release / packaging (homebrew, deb, rpm)
