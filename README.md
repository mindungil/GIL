# gil

자율 코딩 하네스. 길고 철저한 인터뷰로 모든 요구사항을 추출한 뒤, 며칠이 걸리더라도 사용자에게 다시 묻지 않고 끝까지 작업을 수행하는 CLI 에이전트.

**상태**: Phase 10 완료. e2e 12 단계 전부 green (1-9 + modal + daytona + oidc). 4 binary (`gil` / `gild` / `giltui` / `gilmcp`) build + install 가능. VS Code 확장 + Python Atropos 어댑터 scaffold. GoReleaser로 16-binary release matrix 빌드 가능 (linux/darwin × amd64/arm64 × 4 binary + deb + rpm + brew formula).

## 빠른 시작

```bash
# 1. Build
git clone https://github.com/<user>/gil.git
cd gil
make build           # produces bin/{gil,gild,giltui,gilmcp}
# (or: make install — installs to /usr/local/bin)

# 2. Set Anthropic API key
export ANTHROPIC_API_KEY=sk-ant-...

# 3. Start the daemon (foreground)
./bin/gild --foreground &

# 4. Create a session in your project
SESSION=$(./bin/gil new --working-dir $(pwd) | awk '{print $3}')

# 5. Interview — agent asks until it has enough to work autonomously
./bin/gil interview $SESSION

# 6. Run — autonomous; check progress in another terminal
./bin/gil run $SESSION
# or:
./bin/gil events $SESSION --tail
# or:
./bin/giltui  # interactive TUI
```

## 무엇이 다른가

기존 코딩 CLI들 (Claude Code, opencode, codex 등)은 작업 도중 사용자에게 묻거나, 미완성으로 끝납니다. **gil은 시작 전에 모든 것을 묻고, 시작 후에는 끝까지 자율로 갑니다.**

핵심 패턴:
- **인터뷰는 길고 철저하게** — saturation까지 모든 슬롯을 채움
- **에이전트가 결정, 시스템은 안전망** — 도구 순서/임계값 등은 LLM이 정함; 시스템은 스키마/budget/객관 종료/영속성만
- **단일 stop 조건** — verifier 통과 + stuck 회복 시도 끝 + budget exhausted = 작업 종료
- **캐시 보존 압축** — 며칠짜리 작업의 prompt cache prefix가 깨지지 않도록 Hermes 패턴 사용

## 아키텍처 (간략)

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
        │  provider        │  Anthropic + Mock + Retry
        │  runner          │  AgentLoop (the autonomous engine)
        │  tool            │  bash, write_file, read_file, edit, apply_patch,
        │                  │  memory_*, repomap, exec, compact_now
        │  verify          │  shell-assertion runner
        │  stuck           │  5-pattern Detector + 4 Recovery strategies
        │  checkpoint      │  Shadow Git (separate .git outside workspace)
        │  compact         │  Hermes-pattern cache-preserving compression
        │  memory          │  6-file markdown bank
        │  repomap         │  PageRank-ranked symbol overview
        │  edit            │  4-tier SEARCH/REPLACE
        │  patch           │  apply_patch DSL
        │  permission      │  last-wins glob evaluator
        │  exec            │  multi-step Recipe runner
        │  mcp             │  MCP client (consume external servers)
        └───────────────────┘
                  │
        ┌─────────┴─────────┐
        │    runtime/       │  OS / cloud sandbox adapters
        │ ─────────────────│
        │  local           │  bwrap (Linux) + Seatbelt (macOS) + stubs
        │  docker          │  per-command docker exec
        │  ssh             │  per-command ssh + rsync sync
        │  cloud           │  shared Provider interface
        │  modal           │  Modal driver (stub; Phase 10 real)
        │  daytona         │  Daytona driver (stub; Phase 10 real)
        └───────────────────┘

External: gilmcp (mcp/) — MCP server adapter so external clients (Claude
Desktop, Cline, etc.) can use gil as a backend over JSON-RPC stdio.
```

## 명령어 요약

| Command | What |
|---|---|
| `gil new --working-dir <dir>` | Create session |
| `gil status` | List sessions |
| `gil interview <id>` | Run interview to gather requirements |
| `gil resume <id>` | Resume an in-progress interview |
| `gil spec <id>` | Show current (or frozen) spec |
| `gil spec freeze <id>` | Freeze spec to lock in requirements |
| `gil run <id> [--detach]` | Run autonomously; --detach returns immediately |
| `gil events <id> --tail` | Stream live events |
| `gil restore <id> <step>` | Roll back via Shadow Git checkpoint |
| `gild --foreground [--user X] [--http :8080] [--metrics :9090]` | Start daemon |
| `giltui` | Interactive TUI |
| `gilmcp --socket <path>` | MCP server adapter (stdio) |

## 워크스페이스 백엔드

`spec.workspace.backend` (interview에서 결정):

| Backend | 무엇 | 요구사항 |
|---|---|---|
| `LOCAL_NATIVE` | 직접 실행 (default) | — |
| `LOCAL_SANDBOX` | bwrap (Linux) | bwrap installed |
| `DOCKER` | per-command `docker exec` | docker daemon |
| `SSH` | ssh + rsync | ssh + rsync |
| `MODAL` | Modal cloud sandbox | `MODAL_TOKEN_*` (Phase 10) |
| `DAYTONA` | Daytona workspace | `DAYTONA_API_KEY` (Phase 10) |
| `VM` | (planned) | — |

## 자율성 (autonomy) 다이얼

`spec.risk.autonomy`:

- `FULL` — no permission gate
- `ASK_DESTRUCTIVE_ONLY` — deny `rm/mv/chmod/chown/dd/mkfs/sudo`; allow rest
- `ASK_PER_ACTION` — allow only read-only ops + memory_load + repomap; everything else needs approval
- `PLAN_ONLY` — deny all tool execution

In Phase 9 the "Ask" path uses TUI dialog if connected; non-interactive runs fall back to deny.

## 더 읽을거리

- [docs/design.md](docs/design.md) — 전체 설계 narrative
- [docs/install.md](docs/install.md) — 자세한 설치 가이드
- [docs/progress.md](docs/progress.md) — Phase별 산출물 + 결정 이력
- [docs/research/2026-04-25-reference-harnesses-deep-dive.md](docs/research/2026-04-25-reference-harnesses-deep-dive.md) — 7개 참조 도구 라인 레벨 분석
- [docs/dogfood/](docs/dogfood/) — 자율 실행 사례
- [docs/plans/](docs/plans/) — Phase별 구현 계획 (history)

## 참조 (lift 출처)

각 컴포넌트의 commit message에 정확한 source 파일/라인을 명기했지만, 큰 그림:

| 컴포넌트 | 출처 |
|---|---|
| Stuck detection / recovery | OpenHands, Cline, Goose |
| Cache-preserving compression | Hermes Agent (Nous Research) |
| Memory bank | Cline |
| Repomap (PageRank) | Aider |
| SEARCH/REPLACE 4-tier | Aider |
| apply_patch DSL | Codex (OpenAI) |
| Permission glob | OpenCode (sst) |
| Shadow Git checkpoint | Cline + OpenCode |
| bwrap sandbox | Codex |
| Seatbelt sandbox | Codex |
| Recipe / multi-step compression | Hermes Agent |
| MCP server/client | Goose (Block) |
| HTTP gateway | gRPC ecosystem (grpc-gateway) |
| VS Code extension scaffold | Cline (saoudrizwan) |
| OIDC JWT verifier | Hermes Agent (auth.py decode pattern) |
| Atropos environment adapter | Hermes Agent (HermesAgentBaseEnv) |

## 외부 자원이 필요한 잔여 항목

코드 경로는 모두 존재하되 실제 검증은 사용자 자원이 필요:

- **실제 Anthropic dogfood**: `ANTHROPIC_API_KEY` 필요. 절차는 [docs/dogfood/](docs/dogfood/).
- **Modal / Daytona 실제 deployment**: 각각 Modal / Daytona 계정 + 토큰 필요. driver 코드 + e2e (fake CLI / httptest) 모두 green.
- **OIDC 실제 IdP**: Google / Auth0 등 실제 issuer 설정. middleware + mock IdP e2e 모두 green.
- **VS Code Marketplace 게시**: `vsce package` → publisher 등록 → publish.
- **Homebrew tap**: `jedutools/homebrew-tap` 리포 생성 + GoReleaser brews 블록의 token 시크릿 등록.
- **Atropos 실제 training run**: hermes-agent 설치 + `OPENROUTER_API_KEY` + Atropos server.

## License

MIT.
