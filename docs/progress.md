# gil — 진행 추적

> 살아있는 문서. 매 마일스톤에 갱신. git log가 진화 추적.

## 현재 페이즈

**Phase 9: Remote sync / Cloud / Soak / Polish** (완료 — 2026-04-26). 다음 → Phase 10 (실제 클라우드 deployment + dogfood + VS Code).

**Phase 0: 설계** (완료)

- [x] 사용자 요구사항 추출 (Q&A)
- [x] 7개 참조 하네스 1차 분석 (web)
- [x] 7개 참조 하네스 2차 분석 (git clone, 라인 레벨)
- [x] 핵심 결정 사항 합의 (아키텍처 C, gRPC, MIT, gil 명령어)
- [x] 설계 4섹션 narrative 합의 (architecture, stop, interview, context)
- [x] `docs/design.md` 작성
- [x] design.md self-review
- [x] 사용자 검토 + 승인
- [x] 구현 계획서 (`docs/plan.md`) 작성

**Phase 1: 코어 골격** (완료 — 2026-04-26)
- [x] `go.work` + 모듈 8개 초기화
- [x] proto 정의 (gil/v1/*.proto)
- [x] `core/event` (이벤트 스트림)
- [x] `core/spec` (frozen spec)
- [x] `core/session` (SQLite + JSONL)
- [x] `server/` 데몬 + gRPC service stub
- [x] `cli/` 기본 명령어 (`gil daemon/new/status`)

**Phase 2: 인터뷰 엔진** (완료 — 2026-04-26)
- [x] `core/interview` 에이전트 주도 대화
- [x] adversary critique 라운드
- [x] self-audit gate
- [x] saturation 객관 측정
- [x] freeze + SHA-256 lock

**Phase 3: 인터뷰 엔진 (실작동)** (완료 — 2026-04-26)
- [x] core/interview SlotFiller (LLM이 user reply에서 spec field 추출)
- [x] core/interview Adversary (별도 LLM 패스로 spec 비판)
- [x] core/interview SelfAuditGate (Conversation→Confirm 자기 검사)
- [x] Engine.RunReplyTurn 오케스트레이션 (slotfill + adversary + audit)
- [x] server/InterviewService Reply는 RunReplyTurn 사용
- [x] gil resume (in-progress 인터뷰 재개)
- [x] E2E phase03 sanity script

**Phase 4: Run Engine** (완료 — 2026-04-26)
- [x] core/tool 인터페이스 + Bash + WriteFile + ReadFile
- [x] core/verify Runner (shell 단언 실행기)
- [x] core/provider Retry wrapper (exponential backoff for 5xx/timeout/rate_limit)
- [x] core/runner AgentLoop with Anthropic native tool use
- [x] proto RunService (Start sync + Tail stub)
- [x] server/RunService 구현 (frozen→running→done/stopped 상태 전환)
- [x] gild + SDK + CLI run 통합
- [x] gil events 명령 (Phase 5 stub 처리 포함)
- [x] E2E phase04 — autonomous hello.txt run with mock provider

**Phase 5: Run Engine 개선** (완료 — 2026-04-25)
- [x] secret masking on event persistence (core/event)
- [x] AgentLoop emits live events to optional Stream
- [x] per-session event Stream + Persister wired into RunService.Start
- [x] RunService.Tail 실제 구현 (live subscribe; replay = Phase 6)
- [x] gil events --tail 실제 출력 (RFC3339 + source/kind/type/data)
- [x] gil run --detach + 라이브 iteration/tokens (status enrich)
- [x] core/stuck Detector (OpenHands 5 패턴)
- [x] core/stuck Recovery 인터페이스 + ModelEscalateStrategy (others Phase 6)
- [x] AgentLoop 통합 stuck detection + 3회 미회복 시 abort
- [x] runtime/local bwrap Sandbox (ReadOnly / WorkspaceWrite / FullAccess)
- [x] core/tool: Bash CommandWrapper + WriteFile ReadOnly
- [x] RunService respects spec.workspace.backend (LOCAL_SANDBOX → bwrap)
- [x] core/checkpoint Shadow Git (separate .git outside workspace)
- [x] AgentLoop checkpoints per tool-using step
- [x] gil restore <id> <step> + Restore RPC + SDK
- [x] InterviewService per-stage models (slot/adversary/audit fallback)
- [x] e2e phase05 (async + checkpoint + restore + sandbox sanity)
- [x] make install 타겟

**Phase 6: 컨텍스트/메모리/Repomap** (완료 — 2026-04-26)
- [x] core/compact: Compactor (Head/Middle/Tail) + OpenCode 템플릿 + anti-thrashing + Anthropic cache_control
- [x] AgentLoop 95% 자동 압축 + compact_now 도구
- [x] core/memory: Bank (6 markdown) + memory_update / memory_load tools
- [x] AgentLoop 시스템 프롬프트에 bank prepend (full < 4k tokens, else progress only)
- [x] post-verify milestone gate (memory_update 권유)
- [x] core/repomap: ParseFile (Go: stdlib go/parser, py/js/ts: regex; CGO 회피)
- [x] WalkProject (sensible exclusions) + PageRank scoring + token-budget Fit
- [x] repomap tool (TTL cache) + RunService 통합
- [x] Stuck Recovery 4종 모두 실작동: ModelEscalate + AltToolOrder (Cline lift) + ResetSection (Cline lift) + AdversaryConsult (Goose lift)
- [x] runtime/local Seatbelt sandbox (Codex lift, darwin only) + non-darwin stub
- [x] e2e phase06 (repomap + memory + milestone gate sanity)

**Phase 7: Edit/Patch/Permission/TUI** (완료 — 2026-04-26)
- [x] core/edit: 4-tier MatchEngine (Aider editblock_coder.py lift) + DSL parser + edit tool + find_similar_lines hint
- [x] core/patch: apply_patch DSL parser + applier + tool (Codex apply-patch lift)
- [x] core/permission: Evaluator (last-wins glob, OpenCode lift) + AgentLoop gate + spec.risk.autonomy 기반 rule 생성
- [x] tui: Bubbletea 3-pane (sessions/detail/status) + 라이브 event tail + permission_ask 모달 + AnswerPermission RPC
- [x] Stuck Recovery SubagentBranch 실작동 (read-only sub-loop으로 정찰)
- [x] runtime/docker: Wrapper + Container lifecycle (per-command exec)
- [x] e2e phase07 (edit + apply_patch + permission deny sanity)

**Phase 8: Exec/MCP/SSH/Gateway** (완료 — 2026-04-26)
- [x] core/exec: Recipe + Runner (Hermes execute_code lift) — 다단계 도구 압축, 중간 출력은 LLM context에서 숨김
- [x] exec tool + RunService 통합 (intermediate exec_step_done events 송출)
- [x] mcp/cmd/gilmcp 서버 (JSON-RPC 2.0 stdio; list/get/create_sessions tools)
- [x] core/mcp 클라이언트 (Goose subprocess 패턴 lift; 외부 MCP 서버 subprocess launch)
- [x] runtime/ssh: Wrapper + ParseTarget (per-command ssh exec; file ops는 local)
- [x] HTTP/JSON gateway via grpc-gateway: GET/POST /v1/sessions, /v1/sessions/{id}/run/restore/events 등 (gild --http :PORT)
- [x] e2e phase08 (exec recipe + HTTP curl + gilmcp JSON-RPC sanity) 통과
- [x] dogfood 절차 문서화 (`docs/dogfood/2026-04-26-first-run-procedure.md`) — 실제 실행은 ANTHROPIC_API_KEY 필요

**Phase 9: Remote sync / Cloud / Soak / Polish** (완료 — 2026-04-26)
- [x] runtime/ssh.Syncer (rsync Push/Pull)
- [x] RunService SSH push 전 / pull 후 (file ops local 제한 해소)
- [x] runtime/cloud.Provider 인터페이스 (Sandbox + Teardown shape)
- [x] runtime/modal stub (MODAL_TOKEN_ID/SECRET 게이팅)
- [x] runtime/daytona stub (DAYTONA_API_KEY 게이팅)
- [x] proto WorkspaceBackend MODAL=6/DAYTONA=7 + RunService routing
- [x] run-soak mock provider (30턴, file writes + memory + compact + stuck loop)
- [x] e2e9 soak sanity (>=50 events, no panic, memory bank evolved)
- [x] gild --user 데이터 디렉토리 분리
- [x] gild --metrics Prometheus 엔드포인트 (6 metrics)
- [x] README.md 빠른 시작 + 아키텍처 다이어그램
- [x] docs/install.md 단계별 설치 가이드

## 최근 결정 사항

| 일자 | 결정 |
|---|---|
| 2026-04-25 | 프로젝트 시작, 명령어 `gil`, 디렉터리 `/home/ubuntu/gil/` |
| 2026-04-25 | 아키텍처 C (server-client 분리), gRPC, MIT |
| 2026-04-25 | 참조 7개 (opencode/codex/hermes + aider/cline/goose/openhands) |
| 2026-04-25 | hybrid stop (verifier + stuck recovery + budget) |
| 2026-04-25 | 인터뷰 에이전트 주도, 시스템은 스키마/saturation/freeze만 |
| 2026-04-25 | 페이즈 전환 시 self-audit gate 필수 |
| 2026-04-25 | 살아있는 문서 (design/progress) 날짜 X, 단일 파일 유지 |
| 2026-04-26 | Phase 1 (코어 골격) 완료 — gild + gil new/status + event/spec/session 영속화. 18 tasks, ~30 commits. |
| 2026-04-26 | Phase 2 (인터뷰 엔진) 완료 — 데몬 자동 spawn + Anthropic provider + InterviewService gRPC + gil interview/spec CLI. 13 tasks. adversary/self-audit는 Phase 3로 이연. |
| 2026-04-26 | Phase 3 (인터뷰 엔진 실작동) 완료 — SlotFiller + Adversary + SelfAuditGate + RunReplyTurn 오케스트레이션 + gil resume. 7 tasks. cross-restart resume + per-stage 모델 분리 + retry/backoff은 Phase 4로 이연. |
| 2026-04-26 | Phase 4 (Run Engine) 완료 — Tool/Bash/WriteFile/ReadFile + verify.Runner + provider Retry + AgentLoop (Anthropic native tool use) + RunService gRPC + gil run/events. 9 tasks. e2e4가 mock으로 hello.txt 자율 생성 + verifier 통과 시연. Phase 5: sandbox + shadow git + stuck recovery + async run. |
| 2026-04-25 | Phase 5 (Run Engine 개선) 완료 — 18 tasks. secret masking + AgentLoop event emit + per-session Stream/Persister + RunService.Tail real + gil events --tail real + gil run --detach + 라이브 iteration/tokens + 5-pattern Stuck Detector + ModelEscalate recovery + 3-strike abort + bwrap Sandbox + WorkspaceBackend 라우팅 + Shadow Git checkpoint + gil restore + per-stage interview models + e2e5 + make install. e2e-all 5 페이즈 통과. Phase 6: 컨텍스트/메모리/리포맵. |
| 2026-04-26 | Phase 6 (컨텍스트/메모리/Repomap) 완료 — 20 tasks + 1 fix. core/compact (Hermes 캐시 보존 + OpenCode 템플릿 + anti-thrashing + system-and-3 cache_control) + AgentLoop 95% auto-compact + compact_now 도구. core/memory.Bank 6 file + 2 tools + 시스템 프롬프트 prepend + post-verify 마일스톤 게이트. core/repomap (CGO 회피하여 stdlib go/parser + py/js/ts regex로 대체; PageRank + binary-search Fit + TTL cache tool). Stuck recovery 4종 풀 구현 (Cline loop-detection lift + Cline resetHead lift + Goose adversary_inspector lift). runtime/local Seatbelt (Codex seatbelt.rs lift, darwin only). e2e6 통과. server-side memory bank wiring fix 포함. e2e-all 6 페이즈 통과. |
| 2026-04-26 | Phase 7 (Edit/Patch/Permission/TUI) 완료 — 16 tasks. core/edit (Aider editblock_coder.py 4-tier MatchEngine + DSL parser + find_similar_lines hint, edit tool). core/patch (Codex apply-patch parser + applier with seek_sequence 3-tier, apply_patch tool). core/permission (OpenCode evaluate.ts + wildcard.ts: last-wins glob with " *" 트레일링 옵셔널 quirk; AgentLoop gate; spec.risk.autonomy 기반 rule generator FULL/ASK_DESTRUCTIVE/ASK_PER_ACTION/PLAN_ONLY). tui (Bubbletea 3-pane + live event tail + permission_ask 모달 + AnswerPermission RPC, AskCallback 60s timeout). Stuck SubagentBranch 실작동 (read-only sub-loop, AgentLoop.RunSubagent 어댑터). runtime/docker (per-command exec wrapper + Container lifecycle). e2e7 (edit + apply_patch + ASK_DESTRUCTIVE rm 차단 sanity) 통과. e2e-all 7 페이즈 통과. 각 reference lift는 commit 메시지에 출처 명기. |
| 2026-04-26 | Phase 8 (Exec/MCP/SSH/Gateway) 완료 — 9 tasks. core/exec.Recipe + Runner (Hermes code_execution_tool.py lift): 다단계 도구 압축, 중간 결과는 LLM context에서 숨기고 templated summary만 노출. exec tool. mcp/gilmcp 서버 (hand-rolled JSON-RPC 2.0 over stdio; 3 tools). core/mcp 클라이언트 (Goose subprocess 패턴 lift). runtime/ssh (per-command ssh exec wrapper). HTTP/JSON gateway via grpc-gateway (gild --http :PORT). e2e8 (exec + HTTP curl + gilmcp 핸드셰이크) 통과. dogfood 절차 문서화. e2e-all 8 페이즈 통과. |
| 2026-04-26 | Phase 9 (Remote sync / Cloud / Soak / Polish) 완료 — 13 tasks. runtime/ssh.Syncer (rsync Push/Pull) + RunService Push 전/Pull 후 → SSH 백엔드 file ops 제한 해소. runtime/cloud.Provider 인터페이스 + runtime/modal + runtime/daytona stubs (env var 게이팅; 실제 deployment는 Phase 10). proto WorkspaceBackend MODAL=6/DAYTONA=7. run-soak mock + e2e9 (30턴 soak; 122 events, 10+ files, memory bank evolved, no panic). gild --user 데이터 분리, --metrics Prometheus 엔드포인트 (6 metrics: iterations/compact/stuck/tool_calls/sessions_running/build_info). README.md + docs/install.md 폴리시. e2e-all 9 페이즈 통과. |

## 차용 출처 (코드/패턴)

설계 근거: `docs/research/2026-04-25-reference-harnesses-deep-dive.md`

- **Goose**: Recipe DSL, retry.checks, MCP 6 백엔드, adversary reviewer
- **Codex**: linux-sandbox (bwrap+seccomp), apply-patch DSL, rollout JSONL
- **OpenHands**: EventStream, StuckDetector 5패턴, LLMSummarizingCondenser
- **OpenCode**: 서버-TUI 분리, git write-tree 스냅샷, 구조화 압축 템플릿, permission glob
- **Aider**: tree-sitter+PageRank repomap, SEARCH/REPLACE 4단 매칭, architect/editor 분리
- **Cline**: shadow git checkpoint, Plan/Act 토글, 9-카테고리 auto-approve
- **Hermes**: 캐시 보존 압축 불변식, execute_code 다단계 압축, IterationBudget grace call

## Phase 1 산출물 요약 (2026-04-26)

- **데몬**: `gild` (20MB) — gRPC over UDS, SQLite + 이벤트 영속화
- **CLI**: `gil` (3.4MB) — `daemon` (가이드) / `new` (세션 생성) / `status` (목록)
- **SDK**: Go 클라이언트 wrapper (Dial/Create/Get/List)
- **검증**: `make test` 8 모듈 + `make e2e` 통합 테스트 모두 통과
- **다음 단계**: Phase 2 — 인터뷰 엔진 + 데몬 자동 spawn + frozen spec lock 디스크 저장

## Phase 2 산출물 요약 (2026-04-26)

- **데몬 자동 spawn**: `gil new` 첫 실행 시 `gild` background 자동 기동 (수동 실행 불필요)
- **LLM provider 추상화**: `core/provider` 인터페이스 + Mock + Anthropic 어댑터 (anthropic-sdk-go v1.38.0)
- **인터뷰 엔진**: `core/interview` State 머신 (Sensing → Conversation) + Engine (RunSensing + NextQuestion)
- **Spec 영속화**: `core/specstore` (spec.yaml + spec.lock, tamper detection via spec.VerifyLock)
- **InterviewService gRPC**: Start/Reply/Confirm/GetSpec, per-session state with cleanup on Confirm
- **CLI**: `gil interview <id>` 대화형 + `gil spec <id>` (JSON view) + `gil spec freeze <id>`
- **세션 status 전환**: created → interviewing → frozen
- **검증**: `make test` + `make e2e` (Phase 1) + `make e2e2` (Phase 2 sanity) 모두 통과
- **gild 바이너리**: 33MB (+13MB from Phase 1, due to Anthropic SDK)
- **다음 단계**: Phase 3 — adversary critique + self-audit gate + dynamic spec slot filling

## Phase 3 산출물 요약 (2026-04-26)

- **SlotFiller**: LLM이 user reply에서 spec.goal/constraints/verification/workspace/risk/models 슬롯 자동 추출 (dotted-path JSON 업데이트)
- **Adversary**: 별도 LLM 패스가 working spec 비판 → finding 배열 (severity/category/finding/question_to_user/proposed_addition)
- **SelfAuditGate**: 인터뷰 stage 전환(Conversation→Confirm) 직전 명시적 자기 검사 (design.md §2.4)
- **Engine.RunReplyTurn**: slotfill → (saturated 시) adversary 1회 → (clean 시) audit → ready 시 stage 전환, else NextQuestion
- **server.Reply 통합**: outcome에 따라 StageTransition 또는 AgentTurn 이벤트 emit
- **gil resume**: empty first_input sentinel로 in-progress 인터뷰 마지막 agent turn 재현 (cross-restart resume은 Phase 4)
- **검증**: `make e2e-all` (e2e + e2e2 + e2e3) 모두 통과
- **다음 단계**: Phase 4 — 진정한 cross-restart resume (state 디스크 영속화) + Provider retry/backoff + per-stage 모델 분리 (main/weak/editor/adversary)

## Phase 4 산출물 요약 (2026-04-26)

- **Tool 추상화**: `core/tool.Tool` 인터페이스 + builtin (Bash with timeout/truncation, WriteFile with mkdir, ReadFile with 16KB cap)
- **Verifier**: `core/verify.Runner` — `spec.Verification.Checks` 셸 단언 실행 (exit code 기반, per-check 60s timeout, stdout/stderr 4KB 캡)
- **Provider Retry**: `core/provider.Retry` wrapper — exponential backoff for 5xx/timeout/rate_limit; ctx 취소 존중; 비-retryable 즉시 propagate
- **AgentLoop**: `core/runner.AgentLoop` — Anthropic native tool use, 시스템 프롬프트가 verification.checks 명시, 도구 dispatch, verify 실패 시 피드백 → 다음 턴, max_iterations / "done" / "error" 종료
- **RunService gRPC**: Start (sync), Tail (Phase 5 stub). 세션 status 전환: frozen → running → done/stopped
- **gil run / gil events**: 사용자가 frozen session에 대해 자율 실행 트리거, 결과 (status/iterations/tokens/verify) 표시
- **e2e4 시연**: GIL_MOCK_MODE=run-hello로 gild 띄우고, frozen spec 인젝션 후 `gil run`이 mock provider 통해 write_file 호출 → hello.txt 생성 → verifier 통과 → "done"
- **검증**: `make e2e-all` 4 phase (e2e + e2e2 + e2e3 + e2e4) 모두 통과
- **다음 단계**: Phase 5 — 진짜 OS sandbox (bwrap/Seatbelt) + shadow git checkpoint per step + stuck detection + 자가 회복 + 비동기 run + core/event session 통합

## Phase 5 산출물 요약 (2026-04-25)

- **Live event observability**: AgentLoop가 매 iteration/provider/tool/verify 단계마다 Event를 emit; per-session Stream + JSONL Persister; secret masking (sk-ant-/Bearer/password 등)이 디스크 쓰기 직전에 적용
- **Async run**: `gil run <id> --detach` → 즉시 `Status: started` 반환; goroutine이 background 실행; `gil status`가 RUNNING 세션의 ITER/TOKENS 라이브 표시
- **Live tail**: `gil events <id> --tail`이 RunService.Tail로 구독; RFC3339 timestamp + SOURCE + KIND + type + data_json 포맷
- **Stuck detection**: `core/stuck.Detector` 5 패턴 (RepeatedActionObservation/RepeatedActionError/Monologue/PingPong/ContextWindow); AgentLoop가 매 iteration 검사; ModelEscalateStrategy로 회복 시도; 3회 미회복 시 `Result.Status="stuck"` abort
- **Sandbox**: `runtime/local.Sandbox` (bwrap) ReadOnly/WorkspaceWrite/FullAccess 모드; `core/tool.CommandWrapper` 인터페이스로 Bash 옵션 wrap; `WriteFile.ReadOnly` 강제; RunService가 `spec.workspace.backend == LOCAL_SANDBOX`일 때 자동 적용 (DOCKER/SSH/VM은 Phase 6)
- **Shadow Git checkpoints**: `core/checkpoint.ShadowGit` — 워크스페이스 외부의 별도 .git (`~/.gil/sessions/<id>/shadow/<hash>/.git`); AgentLoop가 매 tool-using iteration + 최종 done 시점에 commit; 사용자 repo는 무오염
- **Restore**: `gil restore <id> <step>` (1 = oldest, -1 = latest); RunService.Restore RPC; running 세션은 거부 (FailedPrecondition)
- **Per-stage interview models**: `StartInterviewRequest`에 slot_model/adversary_model/audit_model 추가; 빈 값은 main으로 fallback; `NewEngineWithSubmodels` 4번째 인자 audit (이전엔 main 재사용)
- **검증**: `make e2e-all` 5 페이즈 모두 통과 (e2e5 = 비동기 + tail + checkpoint + restore sanity)
- **make install**: `bin/gil` `bin/gild` → `/usr/local/bin/`
- **다음 단계**: Phase 6 — `core/compact` (캐시 보존 압축, Hermes 패턴), `core/memory` 6 markdown 뱅크, `core/repomap` (tree-sitter + PageRank), Stuck recovery 4종 풀 구현, macOS Seatbelt sandbox

## Phase 6 산출물 요약 (2026-04-26)

- **Cache-preserving compression**: Hermes 패턴 — Head + Tail 보존, Middle을 LLM 요약으로 교체. OpenCode 템플릿 (Goal/Constraints/Progress with Done/InProgress/Blocked). Anti-thrashing (최근 2회 압축이 둘 다 <10% 절감이면 skip). Anthropic system-and-3 cache_control 마커. AgentLoop는 추정 토큰이 95% 임계 도달 시 자동 압축; agent는 compact_now 도구로 명시적 트리거 가능.
- **Memory Bank**: 6개 markdown 파일 (`<sessionDir>/memory/`). projectbrief / productContext / activeContext / systemPatterns / techContext / progress. Init은 stub만 만들고 InitFromSpec은 stub인 파일만 spec 데이터로 채움 (사용자 편집 보존). memory_update + memory_load 도구. AgentLoop 시스템 프롬프트에 항상 prepend (작으면 6개 전부, 4k 토큰 초과 시 progress만). 검증 통과 후 milestone 게이트 1회 — agent에게 "메모리 업데이트할 거 있어?" 묻고 memory_update만 dispatch.
- **Repomap**: tree-sitter 대신 stdlib (Go: go/parser+go/ast 정확, py/js/ts: regex 개요급). WalkProject + PageRank (def↔ref 그래프, 30 iter, damping 0.85) + Fit (binary-search, 4-chars/token). repomap 도구 (60s TTL cache).
- **Stuck Recovery 4종 풀**: ModelEscalate (P5에서 완성) + AltToolOrder (Cline loop-detection.ts soft warning lift, single-shot system note 주입) + ResetSection (Cline CheckpointTracker.resetHead lift, ShadowGit.Reset로 second-newest 체크포인트 hard reset) + AdversaryConsult (Goose adversary_inspector consult_llm lift, 별도 LLM이 1줄 제안 → next iter에 system note로 주입).
- **macOS Seatbelt**: Codex seatbelt.rs + seatbelt_base_policy.sbpl 발췌 (deny-default + minimal allow rules). bwrap.go와 동일한 Mode/Wrap API. darwin build tag + non-darwin stub.
- **검증**: `make e2e-all` 6 페이즈 모두 통과 (e2e6 = repomap + memory_update + write_file + verify + milestone memory_update). 각 reference lift는 commit 메시지에 출처 명기.
- **다음 단계**: Phase 7 — core/edit (SEARCH/REPLACE 4단), core/patch (apply_patch DSL), core/permission (allow/ask/deny + glob), TUI (Bubbletea), DOCKER/SSH workspace backends.

## Phase 7 산출물 요약 (2026-04-26)

- **core/edit (Aider)**: 4-tier MatchEngine (exact / leading-WS / trailing-WS / fuzzy via LCS ratio ≥0.8). DSL parser handles 5-9 char `<<<<<` markers, fenced filename detection, currentFilename fallback for consecutive blocks. edit tool reports per-block status; on miss surfaces `find_similar_lines` hint (closest 6-line chunk). RunService wires the tool.
- **core/patch (Codex)**: 1108-line Codex parser ported to ~400 lines Go (strict mode only; lenient/streaming Phase 8+). Three hunk kinds (Add / Delete / Update with optional Move). seek_sequence 3-tier (exact → rstrip → trim-both) with EOF anchoring. apply_patch tool reports per-hunk; per-hunk failure continues vs Codex which bails.
- **core/permission (OpenCode)**: Evaluator with `findLast` semantics — last matching rule wins, default Ask. Wildcard supports `*`/`?` + the OpenCode trailing-" *" optional quirk (so `"ls *"` matches both `"ls"` and `"ls -la"`). FromAutonomy maps spec.risk.autonomy → rules: FULL = no gate, ASK_DESTRUCTIVE_ONLY = allow-all + deny rm/mv/chmod/chown/dd/mkfs/sudo, ASK_PER_ACTION = allow only read-only, PLAN_ONLY = deny all.
- **AgentLoop permission gate**: pre-tool dispatch evaluation with permissionKeyFor extractor (bash→command, file ops→path, memory_*→file). AskCallback hook for interactive Ask path; without callback, Ask = Deny (Phase 7 fallback).
- **TUI (Bubbletea)**: 3-pane layout (sessions / detail / status). j/k navigation, r refresh, q quit. Live event tail per RUNNING session (200-event ring buffer). permission_ask 모달 (y/n/esc) → AnswerPermission RPC unblocks the run. 60s timeout = deny.
- **Stuck SubagentBranch**: read-only sub-loop (read_file + repomap + memory_load) investigates project, returns 1-3 sentence finding. AgentLoop.RunSubagent 어댑터로 import cycle 회피. Result.FinalText로 sub-loop output 노출.
- **runtime/docker**: Wrapper builds `docker exec [-w wd] [-u user] container cmd args`. Container.Start/Stop manages per-session container lifecycle. RunService rewires Bash.Wrapper after Container.Start in DOCKER backend.
- **검증**: `make e2e-all` 7 페이즈 모두 통과 (e2e7 = edit + apply_patch + ASK_DESTRUCTIVE deny).
- **다음 단계**: Phase 8 — core/exec (Hermes execute_code 다단계 도구 압축), mcp/ (Goose MCP 백엔드), SSH workspace backend, HTTP/JSON gateway, 첫 dogfood.

## Phase 8 산출물 요약 (2026-04-26)

- **core/exec (Hermes lift)**: Recipe = JSON sequence of tool calls + summary template ({{step_N_output}} placeholders). Runner는 in-process 실행, 중간 결과는 hidden, summary 1회만 LLM에게 반환. 캐시 절약의 결정적 패턴 (Hermes의 Python subprocess 대신 typed JSON으로 RCE 회피). Resource 한도: 300s/step, 50 steps, 50KB output 모두 Hermes에서 직접 lift.
- **exec tool**: agent-callable Recipe wrapper. 자기 자신을 inner Tools에서 필터링 (recursion 방지). RunService가 Emit 콜백으로 exec_step_* 이벤트를 stream에 흘려 observer는 보지만 LLM은 못 봄.
- **mcp/gilmcp 서버**: hand-rolled JSON-RPC 2.0 over stdio (Goose lib.rs shape lift; rmcp Rust crate에 해당하는 Go SDK가 없어서 직접 구현). 3 tools: list_sessions / get_session / create_session. 외부 MCP 클라이언트(Claude Desktop, Cline 등)가 gil을 백엔드로 사용 가능.
- **core/mcp 클라이언트 (Goose lift)**: 외부 MCP 서버 subprocess launch + JSON-RPC 다중화 (atomic ID, sync.Map pending). RemoteTool로 wrapping해서 core/tool.Tool 인터페이스 구현 → AgentLoop가 외부 MCP tools 사용 가능.
- **runtime/ssh**: bwrap/seatbelt/docker와 동일한 Wrapper API. ParseTarget이 user@host[:port][/key] 4가지 형식 처리. Phase 8 limitation: file ops (write_file/read_file)는 local 유지; 원격 file sync는 Phase 9.
- **HTTP/JSON gateway (grpc-gateway)**: session/run RPC에 google.api.http 어노테이션 추가. `buf generate`가 .pb.gw.go 생성. gild --http :PORT가 gateway mux 마운트. `curl http://127.0.0.1:8080/v1/sessions` 작동. browser/curl 클라이언트가 gRPC 클라이언트 없이 gil 사용 가능.
- **검증**: `make e2e-all` 8 페이즈 모두 통과 (e2e8 = exec recipe + HTTP curl 2회 + gilmcp JSON-RPC initialize/tools/list 핸드셰이크).
- **dogfood**: 첫 실행 절차 (`docs/dogfood/2026-04-26-first-run-procedure.md`)에 문서화. 실제 run은 ANTHROPIC_API_KEY 필요해서 user-driven.
- **다음 단계**: Phase 9 — 며칠 무인 시뮬레이션 + 첫 dogfood 실제 실행 + 클라우드 VM 백엔드 + 원격 file sync + VS Code 확장.

## Phase 9 산출물 요약 (2026-04-26)

- **SSH 원격 file sync**: `runtime/ssh.Syncer` (rsync 기반 Push/Pull, ssh transport는 Wrapper 재사용). RunService가 SSH 백엔드 run 전 Push, defer Pull. rsync 미설치 시 soft-warn (event-only). Phase 8의 "file ops stay local" 제한 해소.
- **클라우드 백엔드 scaffolding**: `runtime/cloud.Provider` 공통 인터페이스 (Available/Provision/Sandbox+Teardown). `runtime/modal` (MODAL_TOKEN_ID + MODAL_TOKEN_SECRET + modal CLI 게이팅), `runtime/daytona` (DAYTONA_API_KEY 게이팅). 두 stub 모두 placeholder argv 생성 (실제 deployment는 Phase 10 — credentials 필요). proto enum MODAL=6/DAYTONA=7. RunService.executeRun이 Provision → Bash.Wrapper rewire → Teardown defer.
- **Soak 시뮬레이션**: `run-soak` mock provider (30턴: 10 file writes + memory_update + compact_now + 6 repeated bash for stuck + final write/memory + end_turn). e2e9가 122 events / 10+ workspace files / 메모리 progress.md 갱신 / stuck_detected 발사 / gild process 무결성 모두 검증.
- **Multi-user dir isolation**: `gild --user <name>` → `<base>/users/<name>/` 데이터 분리. socket/sqlite/sessions/events 모두 분리. Phase 10 OAuth 위한 디렉토리 수준 기반.
- **Prometheus metrics**: `gild --metrics :PORT` 엔드포인트. 6 metrics: `gil_run_iterations_total` (counter), `gil_compact_done_total` (counter), `gil_stuck_detected_total{pattern}` (counter), `gil_tool_calls_total{tool,result}` (counter), `gil_sessions_running` (gauge), `gil_build_info{version}` (gauge). RunService에 third event subscriber goroutine 추가 — event type/data 파싱하여 적절한 counter bump.
- **문서 폴리시**: README.md를 placeholder에서 빠른 시작 가이드 + ASCII 아키텍처 다이어그램 + 명령어 표 + workspace 백엔드 매트릭스 + 자율성 다이얼 + lift 출처 매트릭스로 교체. docs/install.md 단계별 설치 가이드 (요구사항/빌드/환경설정/첫 실행/옵션 백엔드 활성화/문제 해결/검증).
- **검증**: `make e2e-all` 9 페이즈 모두 통과. `make build` 4 binary 생성.
- **다음 단계**: Phase 10 — 실제 Anthropic-driven dogfood 실행 + Modal/Daytona 실제 deployment + VS Code 확장 + OAuth multi-user + Atropos RL 통합 (모두 외부 자원/계정 필요).

## 미해결 / 추후 결정

- 모델 디폴트 추천 (Anthropic Claude 4.7/4.6 + ?)
- Anthropic 계정 인증 방식 (API key / OAuth setup-token / Claude Code creds)
- 첫 dogfood 작업 무엇으로 할지
- v2: 클라우드 VM 백엔드 (Modal/Daytona) — 우선순위
- v2: HTTP/JSON 호환 (grpc-gateway) — 브라우저 클라이언트 필요 시
