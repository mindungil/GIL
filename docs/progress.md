# gil — 진행 추적

> 살아있는 문서. 매 마일스톤에 갱신. git log가 진화 추적.

## 현재 페이즈

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

**Phase 2: 인터뷰 엔진** (대기)
- [ ] `core/interview` 에이전트 주도 대화
- [ ] adversary critique 라운드
- [ ] self-audit gate
- [ ] saturation 객관 측정
- [ ] freeze + SHA-256 lock

**Phase 3: 검증 + Stop** (대기)
- [ ] `core/verify` 셸 단언 실행기
- [ ] `core/stuck` 5패턴 시맨틱 감지
- [ ] 자가 회복 5단계
- [ ] `core/budget` + grace call

**Phase 4: 워크스페이스/체크포인트** (대기)
- [ ] `core/checkpoint` shadow git
- [ ] `runtime/local` (bwrap+Seatbelt)
- [ ] `runtime/docker`
- [ ] `runtime/ssh`

**Phase 5: 컨텍스트/메모리** (대기)
- [ ] `core/compact` 캐시 보존 압축
- [ ] `core/memory` 6 마크다운 뱅크 (진짜 구현)
- [ ] `core/repomap` tree-sitter + PageRank

**Phase 6: 도구/편집** (대기)
- [ ] `core/edit` SEARCH/REPLACE 4단 매칭
- [ ] `core/patch` apply_patch DSL
- [ ] `core/exec` 다단계 도구 압축
- [ ] `core/permission` allow/ask/deny + glob

**Phase 7: TUI + SDK** (대기)
- [ ] `tui/` Bubbletea
- [ ] `sdk/` Go 클라이언트
- [ ] `mcp/` 빌트인 MCP 서버

**Phase 8: 통합 테스트 + 첫 자율 작업 시연** (대기)
- [ ] e2e: 인터뷰 → freeze → run → 검증 통과 → 보고
- [ ] 며칠 무인 시뮬레이션
- [ ] 첫 dogfood: gil 자체 기능 추가를 gil 으로 하기

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

## 미해결 / 추후 결정

- 모델 디폴트 추천 (Anthropic Claude 4.7/4.6 + ?)
- Anthropic 계정 인증 방식 (API key / OAuth setup-token / Claude Code creds)
- 첫 dogfood 작업 무엇으로 할지
- v2: 클라우드 VM 백엔드 (Modal/Daytona) — 우선순위
- v2: HTTP/JSON 호환 (grpc-gateway) — 브라우저 클라이언트 필요 시
