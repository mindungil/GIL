# Harness UX 비교 감사 — 2026-04-26

> **Why this exists**: Phase 10 마무리 직후 사용자가 "왜 env var로 auth하지?" 라고 지적. 같은 종류의 deviation이 codebase 전반에 또 있을지 7개 reference harness (aider/cline/codex/goose/hermes-agent/opencode/openhands)와 line-level 비교 감사. **A=critical / B=worthwhile / C=defensible / D=already have**.

**핵심 발견**: 7개의 A-tier 갭 — first-run UX, auth/credstore, XDG, subcommand 표면, AGENTS.md, MCP add/remove, in-chat slash command, daemon-mandatory, error message.

---

## 17개 surface별 감사 결과

| # | surface | gil 상태 | 주요 reference | 평가 |
|---|---|---|---|---|
| 1 | first-run UX | `gil status` → `socket did not appear` 에러 | opencode `Global` auto-create dirs, codex onboarding TUI, goose `configure first-time setup`, aider `offer_openrouter_oauth` | **A** |
| 2 | auth/credentials | env var only (`ANTHROPIC_API_KEY`) | opencode `auth.json` 0600 + `auth login`, codex `~/.codex/.credentials.json` + `codex login`, goose keyring + fallback `secrets.yaml` | **A** |
| 3 | config layout | `~/.gil/` (non-XDG, single dir) | opencode/goose XDG split, codex `CODEX_HOME` override, aider `.aider.conf.yml` 다중경로 | **A** |
| 4 | subcommand surface | 9 verbs, session-only | codex 17 verbs, goose 17 verbs, opencode 22 verbs | **A** (init/auth/mcp/doctor/completion/update/cost/export 누락) |
| 5 | project-local config | 없음 | opencode `.opencode/`, codex 트러스트, aider `.aider.conf.yml`, cline `.clinerules/` | **B** |
| 6 | memory/AGENTS.md | 없음 (per-session memory bank만 존재) | codex `AGENTS.md` 트리워크, opencode AGENTS+CLAUDE merge, cline `.clinerules` | **A** |
| 7 | MCP add/remove | 없음 (gilmcp는 외부 노출용) | codex `mcp {list,add,remove,login,logout}`, opencode `mcp`, goose `mcp + configure` | **A** |
| 8 | slash commands in run/TUI | 없음 | codex 36개, opencode TUI registry, cline 7개, aider ~50개 | **A** (run/TUI), **B** (interview) |
| 9 | permission persistence | glob만, "always allow" 없음 | codex `ApprovedForSession`, opencode persisted across sessions, cline `CommandPermissionController` | **B** |
| 10 | cost / usage 가시성 | 없음 | codex `/status`, opencode `stats`, aider `/tokens` | **B** |
| 11 | daemon vs single-process | daemon 강제 | opencode/codex/goose/aider 모두 single-process default, daemon은 opt-in | **A** (재평가 필요) |
| 12 | update mechanism | 없음 | codex `update_action`, goose `update`, opencode `upgrade`, aider `--upgrade` | **B** (배포 결정 후) |
| 13 | output formatting | text only | codex `exec --json`, goose `--output text\|json\|stream-json` | **B** |
| 14 | error messages | 내부 어휘 노출 ("socket did not appear") | codex `CHATGPT_LOGIN_DISABLED_MESSAGE`, opencode typed errors, goose `doctor`, aider hints | **A** |
| 15 | shell completions | 없음 (cobra 한 줄로 가능) | codex `clap_complete`, goose 동일, aider `shtab` | **B** |
| 16 | session export/import | 없음 | opencode `export`/`import`/`share`, codex `/copy`, aider `/save`/`/load` | **B** |
| 17 | telemetry | 없음 — 의도적 | goose posthog opt-in, aider analytics opt-in, codex/opencode 없음 | **D** (방어 가능) |

## A-tier 7개를 어떻게 묶을지

**Phase 11 (필수 — fresh-install dogfood가 가능하려면)**:
1. XDG 레이아웃 마이그레이션 (`~/.gil/` → `$XDG_*/gil/`, 자동 1회 마이그레이션)
2. `gil auth login/logout/list` + credstore (opencode 패턴 — JSON 0600)
3. `gil init` first-run scaffolding
4. `gil doctor` 진단
5. Daemon 자동 spawn (read-only 명령은 daemon 불필요로 분리, 나머지는 fork+exec gild)
6. Error 메시지 overhaul (`Hint` 필드, 사용자 어휘로 표현)
7. `gil completion <shell>` (cobra 한 줄)

**Phase 12 (in-session UX)**:
- AGENTS.md/CLAUDE.md 트리워크 + system context 주입
- `gil mcp add/remove/list/login/logout` + `$XDG_CONFIG_HOME/gil/mcp.toml`
- TUI/run slash command parser + 핵심 9개 (`/help /status /cost /clear /compact /model /agents /diff /quit`)
- 프로젝트-로컬 `.gil/` 인식 (config + AGENTS.md 오버라이드)
- Permission "always allow" 영속화 (`$XDG_STATE_HOME/gil/permissions.toml`)
- `gil cost` + `gil stats`
- `--output json` global flag
- `gil export <session> [--format md|json|jsonl]` + `gil import`

**Phase 13 (배포)**:
- 실제 배포 채널 결정 (homebrew tap, curl-installer, go install) → `gil update` wiring
- 텔레메트리 stance README 문서화 ("no telemetry by design")

## 주요 reference 파일 경로 (구현 시 lift 출처)

- opencode auth: `packages/opencode/src/cli/cmd/providers.ts`, `src/auth/index.ts`, `src/global/index.ts`
- codex login + subcommand: `codex-rs/cli/src/{main,login,mcp_cmd}.rs`
- codex slash commands enum: `codex-rs/tui/src/slash_command.rs`
- codex AGENTS.md discovery: `codex-rs/core/src/agents_md_tests.rs`
- goose XDG + keyring + first-run: `crates/goose/src/config/{paths,base}.rs`, `crates/goose-cli/src/commands/configure.rs`, `crates/goose-cli/src/cli.rs`
- aider onboarding + shtab: `aider/onboarding.py`, `aider/args.py:855`
- cline rules + permission persistence: `src/core/context/instructions/user-instructions/cline-rules.ts`, `src/core/permissions/types.ts`
- opencode AGENTS.md+CLAUDE.md merge: `packages/opencode/src/session/instruction.ts`
