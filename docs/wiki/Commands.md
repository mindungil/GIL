# Commands

전체 CLI reference. `gil <cmd> --help` 으로 각 명령어 상세.

## 셋업

| 명령 | 동작 |
|---|---|
| `gil init` | first-run scaffolding (XDG dirs + config.toml + auth login) |
| `gil doctor` | 환경 진단 (5 그룹: Layout/Daemon/Credentials/Sandboxes/Tools) |
| `gil completion <bash\|zsh\|fish\|powershell>` | shell completion script |
| `gil --version` | 버전 + commit + 빌드 시각 |
| `gil --help` | 명령어 리스트 |

## 자격증명

| 명령 | 동작 |
|---|---|
| `gil auth login [<provider>]` | API key 등록 (interactive) |
| `gil auth list` | 등록된 provider + masked key |
| `gil auth status` | credstore + env var fallback 표시 |
| `gil auth logout <provider>` | credential 삭제 |

## 채팅 (Phase 24 — primary entry)

| 명령 | 동작 |
|---|---|
| `gil` (no-arg, TTY) | 채팅 REPL — 자연어로 task 설명, gil이 알아서 dispatch |
| `gil chat` | 위와 동일 (명시적 형태; 파이프 환경에서도 강제 chat) |
| `gil --no-chat` | 채팅 끄고 legacy summary로 |

채팅은 첫 메시지를 NEW_TASK / RESUME / STATUS / HELP / EXPLAIN로 분류 후 적절한 flow로 자동 라우팅. 비대화형(stdout pipe)일 땐 자동으로 summary 모드 fallback — 스크립트 호환.

## 세션

| 명령 | 동작 |
|---|---|
| `gil new --working-dir <dir>` | 새 세션 생성 (verb-mode; 채팅이 자동 처리) |
| `gil status [<id>]` | 세션 list (visual) — `--plain` / `--output json` |
| `gil session list` | session list 별도 alias |
| `gil session show <id>` | metadata + spec + event count |
| `gil session rm <id>` | 세션 삭제 (--yes) |
| `gil session rm --status DONE --older-than 7d` | 일괄 삭제 |
| `gil export <id> [--format markdown\|json\|jsonl]` | 세션 dump |
| `gil import <file.jsonl>` | replay (read-only) |

## 인터뷰

| 명령 | 동작 |
|---|---|
| `gil interview <id>` | 대화형 spec 채우기 |
| `gil resume <id>` | in-progress 인터뷰 재개 |
| `gil spec <id>` | 현재 (또는 frozen) spec 표시 |
| `gil spec freeze <id>` | 수동 freeze |

## 실행

| 명령 | 동작 |
|---|---|
| `gil run <id>` | 동기 자율 실행 |
| `gil run <id> --detach` | 백그라운드 |
| `gil run <id> --interactive` | slash 명령 stdin 모드 |
| `gil watch <id>` | 라이브 진행률 (in-place ANSI) |
| `gil events <id> [--tail] [--filter <set>]` | 이벤트 stream — set: all / milestones / errors / tools / agent |

## 모니터링

| 명령 | 동작 |
|---|---|
| `gil cost [<id>]` | 토큰 + USD (단일 세션) |
| `gil stats [--days N]` | 누적 (per-model breakdown) |
| `giltui` | Bubbletea TUI (4-pane mission control) |

## 복구

| 명령 | 동작 |
|---|---|
| `gil restore <id> <step>` | shadow git checkpoint 으로 복원 |

## MCP

| 명령 | 동작 |
|---|---|
| `gil mcp list` | 등록된 MCP server list |
| `gil mcp add <name> --type stdio -- COMMAND ARGS...` | stdio server 추가 |
| `gil mcp add <name> --type http --url URL [--bearer K]` | http server 추가 |
| `gil mcp remove <name>` | 삭제 |

## Permission

| 명령 | 동작 |
|---|---|
| `gil permissions list` | 영속 always_allow/deny rules |
| `gil permissions remove <pattern> --allow\|--deny` | rule 삭제 |
| `gil permissions clear --yes` | project 전체 clear |

## 데몬

| 명령 | 동작 |
|---|---|
| `gild --foreground` | 데몬 시작 (수동) |
| `gild --foreground --user X --base /var/lib/gil` | multi-user |
| `gild --foreground --http :8080` | HTTP/JSON gateway |
| `gild --foreground --metrics :9090` | Prometheus endpoint |
| `gild --foreground --grpc-tcp :7070 --auth-issuer ...` | OIDC bearer auth |

대부분 명령어는 gild 자동 spawn — 수동 시작 불필요.

## 업데이트

| 명령 | 동작 |
|---|---|
| `gil update` | 설치 method 감지 + 업그레이드 |
| `gil update --check` | 최신 tag 확인만 |

## 글로벌 플래그

| 플래그 | 동작 |
|---|---|
| `--output text\|json` | 출력 포맷 (events / status / mcp list / auth list / cost / stats / doctor) |
| `--ascii` | Unicode glyph fallback (LANG=C 환경) |
| `--socket <path>` | gild socket 경로 override |

## 슬래시 명령 (TUI / `gil run --interactive`)

| 명령 | 동작 |
|---|---|
| `/help` | list available commands |
| `/status` | 현재 세션 정보 |
| `/cost` | 누적 비용 |
| `/clear` | local event ring buffer reset |
| `/compact` | 다음 turn 강제 compact |
| `/model <name>` | next-turn 모델 hint |
| `/agents` | AGENTS.md 열기 ($EDITOR) |
| `/diff` | 마지막 checkpoint 대비 diff |
| `/quit` | TUI 종료 |

**Ground rule** (Phase 12): 슬래시는 observation only — mid-tool-call interrupt 금지.
