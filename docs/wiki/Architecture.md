# Architecture

```
           ┌─────────────┐
   gil ────┤  cli/       │  cobra root
           └──────┬──────┘
                  │ gRPC over UDS
           ┌──────┴──────┐
  giltui ──┤  gild       │  daemon (server/)
           │  + SQLite   │  - SessionService / InterviewService / RunService
           │  + JSONL    │  - 4 service interfaces, all event-driven
           └──────┬──────┘
                  │
        ┌─────────┴─────────┐
        │     core/         │  pure logic, no I/O outside dedicated pkgs
        │ ─────────────────│
        │  event           │  append-only stream + pub/sub
        │  spec            │  frozen spec + SHA-256 lock
        │  interview       │  Sensing → Conversation → Confirm
        │  provider        │  Anthropic + OpenAI (vllm/openrouter compat) + Mock + Retry
        │  runner          │  AgentLoop (the autonomous engine)
        │  tool            │  bash / write_file / read_file / edit / apply_patch /
        │                  │  memory_* / repomap / exec / compact_now / plan /
        │                  │  web_fetch / web_search / lsp / subagent / clarify
        │  verify          │  shell-assertion runner
        │  stuck           │  6-pattern Detector + 4 Recovery strategies
        │  checkpoint      │  Shadow Git (separate .git outside workspace)
        │  compact         │  Hermes-pattern cache-preserving compression
        │  memory          │  6-file markdown bank (Cline)
        │  repomap         │  PageRank-ranked symbol overview (Aider)
        │  edit            │  4-tier SEARCH/REPLACE (Aider)
        │  patch           │  apply_patch DSL (Codex)
        │  permission      │  last-wins glob + persistent always_allow/deny + bash chain split
        │  exec            │  multi-step Recipe runner (Hermes)
        │  mcp             │  MCP client (consume external) + jsonrpc framing
        │  mcpregistry     │  TOML-backed MCP server registry (global + project)
        │  plan            │  per-session plan store (TODO + status)
        │  web             │  fetch + html→markdown (stdlib + x/net/html)
        │  lsp             │  JSON-RPC client + multi-language server manager
        │  notify          │  desktop / webhook / multi notifier (clarify backend)
        │  cost            │  model price catalog + USD calculator
        │  workspace       │  project root discovery + .gil/ layered config
        │  paths           │  XDG layout (Config/Data/State/Cache)
        │  credstore       │  file-based credential store (auth.json 0600)
        │  cliutil         │  typed UserError with Hint
        │  instructions    │  AGENTS.md / CLAUDE.md / .cursor/rules tree-walk
        │  slash           │  parser + handler registry (TUI + run --interactive)
        │  version         │  build-time version constants
        └───────────────────┘
                  │
        ┌─────────┴─────────┐
        │    runtime/       │  OS / cloud sandbox adapters
        │ ─────────────────│
        │  local           │  bwrap (Linux) + Seatbelt (macOS) + stubs
        │  docker          │  per-command docker exec
        │  ssh             │  per-command ssh + rsync sync
        │  cloud           │  shared Provider interface
        │  modal           │  Modal CLI shell-out + manifest gen
        │  daytona         │  Daytona REST API client
        └───────────────────┘

External binaries:
  gilmcp (mcp/) — MCP server adapter (stdio JSON-RPC) so external clients
                  (Claude Desktop, Cline, etc.) use gil as backend.

Auth (optional):
  gild --auth-issuer <url> — OIDC bearer-token middleware (UDS bypass default)

Distribution:
  GoReleaser — 16 binary matrix + deb + rpm + brew formula

Python adapters:
  python/gil_atropos     — Hermes Atropos RL environment adapter
  python/gil_swebench    — SWE-bench harness (Phase 23.C)

VS Code:
  vscode/                — TypeScript extension scaffold (Cline pattern lift)
```

## 핵심 설계 결정

1. **인터뷰-자율 분리** — 모든 다른 harness 는 chat-driven (사용자 매 turn 개입). gil 만 "exhaust interview → freeze spec → autonomous run". 이게 "며칠 안 묻기" 의 구조적 보장.

2. **Spec lock + verifier 통과 = 객관적 종료** — agent 가 자기 자신을 "끝났다" 선언 못 함. 외부 spec.verification.checks (SHELL kind) 통과 + budget exhausted = 종료.

3. **Stuck recovery → Single stop condition** — 5+1 detector + 4 recovery + 3-strike abort. 자율 실행이 끝까지 가게 하는 안전망 (Phase 21.A NoProgress 6번째 패턴 + 22.B verify-independent fallback).

4. **Daemon + gRPC + 4 binary** — 다른 harness 는 single-process. gil 의 daemon 모델 덕분에 disconnect 후 reconnect 자연스러움.

5. **자체 server이면서 자체 client + MCP server adapter (gilmcp)** — 외부 Claude Desktop 등이 gil을 backend로 쓸 수 있음.

## 모듈 의존성

```
core (no upward deps)
  ↑
runtime (uses core/tool interface)
  ↑
proto (gRPC schema)
  ↑
sdk (gRPC client wrapper)
  ↑
cli, server, tui, mcp (binaries)
```

`go.work` 으로 8 module 묶임. replace directives 로 local 작동.

## 자세한 narrative

`docs/design.md` 참조 — 전체 설계 narrative, 모든 결정 사항, 트레이드오프.
