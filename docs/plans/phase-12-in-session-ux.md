# Phase 12 — In-session UX (AGENTS.md, MCP add, 슬래시, 프로젝트로컬, cost, export)

> Phase 11에서 fresh-install path 마련. Phase 12는 **세션 안에서의 UX 갭**을 메운다 — 다른 harness가 다 가진, 세션 진행 중에 사용자가 만나는 표면들.

**Goal**: AGENTS.md 자동 인식, MCP server 등록 subcommand, TUI/run 슬래시 명령, project-local `.gil/`, permission 영속화, cost/stats 가시성, JSON 출력, session export.

**Skip (Phase 13)**: `gil update`, 텔레메트리 stance 문서화.

---

## Track A — AGENTS.md / CLAUDE.md 디스커버리

### T1: core/instructions 패키지

**Files**: `core/instructions/discover.go`, `core/instructions/discover_test.go`

워크스페이스 → home 트리워크:
1. `<workspace>/AGENTS.md`
2. `<workspace>/CLAUDE.md` (옵션, `instructions.disable_claude_md = true` 시 skip)
3. `<workspace>/.cursor/rules/*.mdc` (cursor 규칙)
4. parent 디렉토리들 같은 패턴 (git root까지)
5. `$XDG_CONFIG_HOME/gil/AGENTS.md` (글로벌)
6. `$HOME/AGENTS.md` (옵션)

Concat + dedup + 토큰 budget (default 8KB) — over면 가장 가까운 (워크스페이스 root) 우선.

Reference: `/home/ubuntu/research/codex/codex-rs/core/src/agents_md_tests.rs` (트리워크), `/home/ubuntu/research/opencode/packages/opencode/src/session/instruction.ts` (AGENTS+CLAUDE merge).

Commit: `feat(core/instructions): AGENTS.md/CLAUDE.md/cursorrules tree-walk discovery`

### T2: AgentLoop가 system prompt에 주입

**Files**: `core/runner/runner.go`

AgentLoop 시작 시 `instructions.Discover(workspace)` → System block의 cache_control 다음에 prepend. Memory bank 다음, before 사용자 메시지.

순서: Base system → AGENTS.md/CLAUDE.md → Memory bank (small or progress only) → User.

Commit: `feat(core/runner): inject AGENTS.md context into system prompt`

---

## Track B — gil mcp subcommand

### T3: core/mcpregistry 패키지

**Files**: `core/mcpregistry/registry.go`, `core/mcpregistry/registry_test.go`

`$XDG_CONFIG_HOME/gil/mcp.toml`:
```toml
[servers.fs]
type = "stdio"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]

[servers.search]
type = "http"
url = "https://example.com/mcp"
auth = "bearer:sk-..."
```

Project-local `<workspace>/.gil/mcp.toml`이 있으면 merge (project가 global override).

Commit: `feat(core/mcpregistry): TOML-backed MCP server registry (global + project)`

### T4: gil mcp subcommand

**Files**: `cli/cmd/gil/mcp.go`

```
gil mcp list                              # 등록된 서버
gil mcp add <name> [-- COMMAND ARGS...]   # stdio 추가
gil mcp add <name> --url URL [--bearer KEY]  # http 추가
gil mcp remove <name>
gil mcp login <name>                      # OAuth flow (Phase 13+ 또는 stub)
gil mcp logout <name>
```

Reference: `/home/ubuntu/research/codex/codex-rs/cli/src/mcp_cmd.rs` (subcommand 구조 + add 형식).

Commit: `feat(cli): gil mcp add/list/remove (registry-backed)`

### T5: gild RunService에 mcpregistry 통합

**Files**: `server/internal/service/run.go`

기존 spec.MCP 외에 mcpregistry 서버들도 launch. Per-session 레벨에서 동적 로드 — spec freeze 후라도 mcp.toml 수정하면 다음 run에 반영.

Commit: `feat(server): RunService consumes mcpregistry alongside spec.MCP`

---

## Track C — 슬래시 commands (TUI + run)

### T6: core/slash 패서

**Files**: `core/slash/parser.go`, `core/slash/registry.go`, `core/slash/parser_test.go`

```go
type Command struct {
    Name string
    Args []string
    Raw  string
}

type Handler func(ctx, Command) (output string, err error)

type Registry struct{ /* name → Handler */ }

func ParseLine(line string) (Command, bool)  // /<name> args... 인식, false면 일반 텍스트
```

Reference: `/home/ubuntu/research/codex/codex-rs/tui/src/slash_command.rs` (enum), `/home/ubuntu/research/cline/src/services/slash-commands/index.ts` (parser).

Commit: `feat(core/slash): line parser + handler registry`

### T7: TUI 슬래시 commands (9개)

**Files**: `tui/internal/app/slash.go`, `tui/internal/app/update.go`

기본 9개:
- `/help` — 사용 가능한 명령 목록
- `/status` — 현재 세션 + iter + tokens
- `/cost` — 누적 cost (per-stage + total)
- `/clear` — 화면 clear (이벤트 ring buffer reset, RPC 호출 없음)
- `/compact` — 강제 compact (RunService.RequestCompact RPC)
- `/model <name>` — 다음 turn부터 모델 변경 (RunService.SetModel RPC)
- `/agents` — `<workspace>/AGENTS.md` 열기 (외부 $EDITOR)
- `/diff` — 마지막 checkpoint 대비 변경 (ShadowGit.Diff)
- `/quit` — TUI 종료

각 핸들러는 RunService gRPC 호출 또는 로컬 작업.

Commit: `feat(tui): 9 slash commands (help/status/cost/clear/compact/model/agents/diff/quit)`

### T8: gil run --interactive 모드

**Files**: `cli/cmd/gil/run.go`

`gil run <id> --interactive`은 TUI 안 띄우고도 슬래시 명령 사용 가능 (stdin line 읽고 슬래시면 위 핸들러, 아니면 무시).

Commit: `feat(cli): gil run --interactive — slash commands without TUI`

---

## Track D — Project-local `.gil/`

### T9: workspace.discover

**Files**: `core/workspace/discover.go`, `core/workspace/discover_test.go`

`<workspace>/.gil/` 발견 시 다음 파일들 인식:
- `config.toml` — global config 오버라이드 (model, autonomy, ignore paths)
- `mcp.toml` — global mcp.toml 오버라이드 (Track B와 통합)
- `AGENTS.md` — Track A에서 이미 트리워크로 처리

Layered config: CLI flag > env > project `.gil/config.toml` > global `~/.config/gil/config.toml` > defaults.

Commit: `feat(core/workspace): project-local .gil/ discovery + layered config`

---

## Track E — Permission "always allow" 영속화

### T10: core/permission 영속 store

**Files**: `core/permission/store.go`, modify `core/permission/evaluator.go`

`$XDG_STATE_HOME/gil/permissions.toml`:
```toml
[project."<absolute-workspace-path>"]
always_allow = ["git status", "ls *", "cat README.md"]
always_deny  = []
```

TUI permission 모달에 "Always allow this exact command" 체크박스. 체크 시 store에 추가 → 다음부터 ask 없이 통과.

Reference: `/home/ubuntu/research/codex/codex-rs/protocol/src/protocol.rs` (`ApprovedForSession`), `/home/ubuntu/research/cline/src/core/permissions/CommandPermissionController.ts`.

Commit: `feat(core/permission): persistent always-allow store + TUI checkbox`

---

## Track F — Cost / stats 가시성

### T11: core/cost 패키지

**Files**: `core/cost/catalog.go`, `core/cost/calculator.go`, `core/cost/calculator_test.go`

`$XDG_CACHE_HOME/gil/models.json`에 model 가격 카탈로그 (input/output per million tokens). 빌드 타임에 internal/embed로 default 캐시; `gil models update` (Phase 13)으로 갱신.

```go
type Calculator struct{ Catalog map[string]ModelPrice }
func (c *Calculator) Estimate(model string, in, out int64) (usd float64)
```

Commit: `feat(core/cost): model price catalog + calculator (USD estimate)`

### T12: gil cost / gil stats

**Files**: `cli/cmd/gil/cost.go`, `cli/cmd/gil/stats.go`

```
gil cost [<session-id>]    # 단일 세션 cost
gil stats [--days N]       # 모든 세션 누적 (per-model breakdown)
```

데이터 소스: 기존 event log (`token_usage` 이벤트가 있나? 없으면 추가).

Commit: `feat(cli): gil cost + gil stats (USD + per-model breakdown)`

---

## Track G — `--output json` global flag

### T13: cobra persistent flag

**Files**: `cli/cmd/gil/main.go` (root), 각 command에서 `--output json` 분기

`gil events --output json`이 가장 큰 의의 (외부 파이프라인). `gil status`, `gil session list`도 JSON 모드.

Commit: `feat(cli): --output json/text persistent flag (events, status, session list)`

---

## Track H — Session export / import

### T14: gil export <id>

**Files**: `cli/cmd/gil/export.go`

```
gil export <id> [--format markdown|json|jsonl] [--output <file>]
```

Markdown 포맷: 사람용 리포트 (system prompt 요약 → 인터뷰 Q&A → run 단계별 메시지/tool/verify).
JSON: 머신용 typed snapshot.
JSONL: 원본 이벤트 stream.

Commit: `feat(cli): gil export — markdown/json/jsonl session dump`

### T15: gil import (옵션)

별도 SessionService.Import RPC 추가 — JSONL을 새 세션으로 replay (이벤트 만 복원, 워크스페이스 상태는 워크스페이스 자체에 의존).

Commit: `feat(cli+server): gil import — replay session from JSONL`

---

## Track I — e2e + docs

### T16: phase12 e2e

각 트랙별 small e2e:
- AGENTS.md 디스커버리: workspace에 AGENTS.md 두고 mock provider가 system context에 보였는지 검증
- mcp add → list → remove
- 슬래시: mock TUI 입력으로 /status, /cost 실행
- export: markdown 출력 sanity

Commit: `test(e2e): phase 12 — in-session UX (agents/mcp/slash/export)`

### T17: docs

install.md "고급 사용" 섹션: AGENTS.md, MCP, 슬래시, project-local. progress.md row.

Commit: `docs: in-session UX (Phase 12)`

---

## Phase 12 완료 체크리스트

- [ ] `make e2e-all` Phase 11 + phase12 통과
- [ ] AGENTS.md/CLAUDE.md 트리워크 작동
- [ ] `gil mcp add/list/remove` + RunService 통합
- [ ] TUI 9 슬래시 명령
- [ ] `gil run --interactive` slash 모드
- [ ] Project-local `.gil/config.toml + mcp.toml + AGENTS.md` 인식
- [ ] Permission "always allow" 영속화
- [ ] `gil cost` + `gil stats`
- [ ] `--output json` global flag
- [ ] `gil export` markdown/json/jsonl
