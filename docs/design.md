# gil — 자율 코딩 하네스 설계 문서

> 살아있는 문서. 진화는 git history로 추적.
> 최초 작성: 2026-04-25

---

## 1. gil이란

gil은 사용자 프롬프팅 한 번만으로 며칠짜리 코딩 작업을 끝까지 완수하는 CLI 에이전트다. 기존 도구들(Claude Code, opencode, codex 등)의 핵심 약점은 *작업 도중 사용자에게 의사결정을 위임하거나 미완성 상태로 마무리하는* 점이다. gil은 이를 두 가지 메커니즘으로 해결한다.

**첫째, 시작 전에 모든 것을 묻는다.** 길고 철저한 인터뷰로 frozen spec을 추출한다. 인터뷰의 시간/자원은 의도적으로 무관 — saturation까지 간다.

**둘째, 끝까지 멈추지 않는다.** 자율 실행 중 사용자에게 묻지 않는다. 막히면 자가 회복을 시도한다. 진짜 *현실적으로 작업이 불가능* 할 때만 멈춰 보고한다.

## 2. 설계 원칙

이 원칙들은 모든 결정의 기준이다. 위반이 발견되면 설계가 잘못된 것.

### 2.1 가지치기 금지
하네스 본체에 도메인/scope/워크스페이스/도구/모델을 박지 않는다. 모든 가변성은 인터뷰가 흡수해 frozen spec의 슬롯으로 표현된다. "어떤 종류의 X?"라고 묻고 싶어지면 잘못 가는 신호.

### 2.2 인터뷰는 길고 철저하게
saturation까지 묻는다. 시간/자원 절약은 추구하지 않는다. 인터뷰 길이는 미덕이지 비용이 아니다. 한 번 끝나면 작업 중 다시 묻지 않는다.

### 2.3 에이전트가 결정, 시스템은 안전망
사용자 facing 결정(질문 내용, 도구 선택, 회복 전략)은 에이전트가 한다. 시스템은 스키마 강제, 한도 enforcement, 객관적 종료 신호, 영속화, 통신 프로토콜만 책임진다. 시스템 코드에 고정 질문/임계값/도구 순서가 박히면 의심해야 한다.

### 2.4 인터뷰 전환 게이트
인터뷰 stage 전환 직전에 에이전트의 명시적 self-audit 1회를 통과해야 한다. 객관적 saturation 측정과 별개로 작동하는 두 번째 게이트. 다른 페이즈 전환에는 일반화하지 않는다.

### 2.5 살아있는 문서, 사용자 git 비오염, 영속 이벤트
설계/진행 문서는 날짜 없이 단일 파일로 유지한다. 사용자의 진짜 git을 절대 건드리지 않는다 — 모든 변경은 shadow git에. 모든 행동/관찰은 append-only 이벤트 로그에 영속화한다.

---

## 3. 고수준 아키텍처

### 3.1 프로세스 토폴로지

gil은 데몬-클라이언트 분리 구조다. 며칠짜리 작업이 터미널 세션과 분리되어야 하기 때문.

`gild`(데몬)는 모든 상태를 보유하고 항상 실행된다. `gil`(CLI), `gil-tui`(TUI), 외부 SDK는 모두 stateless 클라이언트로, gild에 gRPC over HTTP/2 (bidirectional streaming)로 연결한다. 사용자가 터미널을 닫아도 gild는 계속 실행되고, `gil attach`로 언제든 다시 붙는다.

상태 저장소는 `~/.gil/`에 모인다 — SQLite (세션/메시지/메타데이터), JSONL (이벤트 로그), shadow git 디렉토리들 (체크포인트), 마크다운 파일들 (메모리 뱅크).

LLM 호출은 gild에서만 일어난다. 클라이언트는 API 키를 다루지 않는다.

### 3.2 Go workspace 레이아웃

```
gil/
├── go.work
├── core/         # 도메인 라이브러리 (모든 로직)
├── runtime/      # 워크스페이스 백엔드 어댑터
├── proto/        # gRPC .proto 정의
├── server/       # gild 데몬
├── cli/          # gil CLI
├── tui/          # Bubbletea TUI
├── sdk/          # Go SDK
├── mcp/          # 빌트인 MCP 서버
└── docs/
```

총 8개 모듈. `core/`가 모든 도메인 로직의 hub이며 다른 모듈은 core에 의존.

### 3.3 core/ 18개 패키지

| 패키지 | 책임 | 차용 출처 |
|---|---|---|
| `event` | append-only 이벤트 스트림, pub/sub | OpenHands EventStream |
| `spec` | frozen spec 직렬화/검증/lock | Goose Recipe + 자체 |
| `verify` | shell 단언 실행기 | Goose retry.checks |
| `stuck` | 5패턴 시맨틱 감지 + 자가 회복 5단계 | OpenHands StuckDetector + 자체 |
| `compact` | 캐시 보존 압축 (Head/Middle/Tail) | Hermes context_compressor |
| `memory` | 6 마크다운 뱅크 진짜 구현 | Cline Memory Bank 패턴 + 자체 |
| `checkpoint` | shadow git 비파괴 스냅샷 | OpenCode + Cline |
| `permission` | allow/ask/deny + glob | OpenCode permission |
| `interview` | 에이전트 주도 인터뷰 + self-audit | 자체 (참조 도구에 없음) |
| `repomap` | tree-sitter + PageRank + 이진검색 | Aider repomap |
| `edit` | SEARCH/REPLACE 4단 매칭 | Aider editblock |
| `patch` | apply_patch DSL 파서 | Codex apply-patch |
| `exec` | 다단계 도구 압축 (UDS RPC) | Hermes execute_code |
| `budget` | 토큰/비용/시간 예산 + grace call | Hermes IterationBudget |
| `rollout` | JSONL 세션 + resume by UUID | Codex rollout |
| `provider` | LLM 프로바이더 추상화 | LiteLLM 패턴 |
| `session` | 세션 상태 + SQLite 저장 | Goose session_manager |
| `adversary` | LLM 안전 검수 | Goose adversary_inspector |

### 3.4 의존성 흐름

```
proto ──► server, sdk, cli, tui
sdk   ──► cli, tui  (gRPC 클라이언트 wrapper)
core  ──► server  (서버가 core의 모든 패키지 사용)
runtime/* ──► core/exec, core/checkpoint  (workspace 백엔드)
mcp/* ──► core/exec  (외부 MCP 서버 연동)
```

server가 core를 호출해 모든 도메인 로직을 실행한다. 클라이언트는 gRPC로만 통신한다.

---

## 4. 사용자 라이프사이클 (`gil` 명령어)

### 4.1 명령어 목록

```
gil daemon [--detach]              # gild 시작
gil new [--goal "<hint>"]          # 새 세션 (인터뷰 자유)
gil interview <session-id>         # 인터뷰 진행
gil spec <session-id>              # 현재 spec 보기
gil spec freeze <session-id>       # 명시적 freeze
gil run <session-id> [--detach]    # 자율 실행 시작
gil status [<session-id>]          # 상태 (모든 세션 또는 단일)
gil attach <session-id>            # 라이브 이벤트 스트림 구독
gil events <session-id> [--tail|--follow]
gil stop <session-id>              # 강제 중단
gil resume <session-id>            # 중단된 세션 재개
gil diff <session-id>              # shadow git diff
gil merge <session-id>             # 사용자 git에 통합
gil fork <session-id>              # 새 세션, 인터뷰 히스토리 import
```

### 4.2 전형적 흐름

사용자는 `gil daemon` 으로 데몬을 띄우거나, 첫 명령 시 자동 spawn한다.

`gil new` 후 `gil interview <id>`로 인터뷰를 시작한다. 수십~수백 턴이 자연스럽다. 도중에 ctrl-c 가능 — 모든 turn은 이벤트 로그에 영속화되므로 며칠 뒤 `gil resume` 으로 이어진다. saturation 도달 시 시스템이 객관적으로 알려주고, 사용자 명시적 confirm 후 freeze.

`gil run` 으로 자율 실행 시작. 며칠이 걸려도 사용자가 다시 개입할 필요 없다. `gil status`로 진행 확인, `gil attach`로 실시간 관찰. 작업 완료 또는 stop 시 화면에 결과만 출력 — 별도 보고서 파일 없음. 데이터는 이미 이벤트 로그/shadow git에 다 있다.

작업 결과가 마음에 들면 `gil merge`로 사용자 git에 가져간다. 마음에 안 들면 `gil fork`로 새 세션 (기존 인터뷰 히스토리 재사용 가능).

---

## 5. Frozen Spec — 인터뷰의 결정체

### 5.1 위치와 형식

- 정규형: protobuf (`proto/gil/v1/spec.proto`, gRPC 전송용)
- 사람용: YAML (`~/.gil/sessions/{id}/spec.yaml`)
- Lock: SHA-256 (`~/.gil/sessions/{id}/spec.lock`, freeze 시 생성)

freeze 후 spec.yaml 수정해도 lock 검증으로 무시. 변경하려면 `gil fork`.

### 5.2 핵심 슬롯

```proto
message FrozenSpec {
  string spec_id = 1;
  string session_id = 2;
  google.protobuf.Timestamp frozen_at = 3;
  string content_sha256 = 4;

  Goal goal = 10;
  Constraints constraints = 11;
  Verification verification = 12;
  Workspace workspace = 13;
  ModelConfig models = 14;
  Budget budget = 15;
  Tools tools = 16;
  RiskProfile risk = 17;
  repeated Microagent microagents = 18;
  repeated ConditionalRule rules = 19;
  Setup setup = 20;
  Recovery recovery = 21;
}
```

각 슬롯의 상세 메시지 정의는 `proto/gil/v1/spec.proto`에 둔다.

### 5.3 필수 vs 선택 슬롯

freeze가 일어나려면 다음이 모두 채워져 있어야 한다(시스템 안전망):

- `goal.one_liner`, `goal.success_criteria_natural` ≥ 3개
- `constraints.tech_stack` ≥ 1개
- `verification.checks` ≥ 1개
- `workspace.backend`
- `models.main`
- `risk.autonomy`

나머지는 선택. 인터뷰 중 채워지면 들어가고, 비어있으면 합리적 디폴트(에이전트가 명시적으로 결정해 spec에 기록).

### 5.4 검증 기준 (Verification)

```proto
message Verification {
  repeated Check checks = 1;
  int32 max_retries_per_check = 2;       // 기본 5
  int64 per_check_timeout_seconds = 3;   // 기본 600
  repeated string cleanup_on_failure = 4;
}

message Check {
  string name = 1;
  CheckKind kind = 2;        // SHELL | FILE_EXISTS | HTTP | REGEX_MATCH | CUSTOM_SCRIPT
  string command = 3;
  int32 expected_exit_code = 4;
  optional string expected_stdout_regex = 5;
  optional HttpProbe http_probe = 6;
  optional string requires_file = 7;
}
```

이 checks가 모두 통과하면 작업 완료. 인터뷰 단계에서 사용자의 자연어 성공 기준을 에이전트가 셸 명령으로 변환해 채운다.

---

## 6. 인터뷰 엔진

인터뷰는 gil의 진짜 차별 요소. 7개 참조 도구 중 인터뷰 단계가 진지한 것은 0개.

### 6.1 3-스테이지 구조

**Stage 1: Domain Sensing**
사용자 첫 입력을 LLM에 보내 도메인을 추정한다 (예: web-saas, cli-tool, ml-pipeline). 결과로 관련 microagent/skill 후보가 자동 로드된다 — 단, *시스템이 선택* 하는 게 아니라 후보를 에이전트에 노출만 시킨다.

**Stage 2: Agent-driven Conversation**
에이전트가 모든 질문을 결정한다. 시스템은 매 턴마다 다음을 inject한다:
- frozen spec proto 스키마
- 현재까지 채워진 spec 상태
- 대화 history
- 도메인 추정
- adversary critique 큐 (있으면)

에이전트가 결정하는 것:
- 어느 슬롯을 다음에 팔지
- 어떤 표현/시나리오로 물을지 (도메인에 맞게)
- 한 슬롯에 더 깊이 갈지 옆으로 넘어갈지
- 거울 질문("지금까지 정리하면…") 시점
- adversary 호출 시점 (시스템이 강제하는 최소/최대 라운드는 있음)

**Stage 3: Confirm + Freeze**
saturation 도달 시 사용자에게 spec 보여주고 명시적 confirm. 마지막 자유 입력 1회 받은 뒤 SHA-256 lock.

### 6.2 Adversary Critique

Stage 2 도중 적절한 시점에 에이전트가 adversary를 호출한다. adversary는 별도 LLM 패스(spec.models.adversary로 모델 분리 가능). 시스템 프롬프트:

> "You are an ADVERSARIAL spec reviewer. The agent will run this spec autonomously for DAYS without human input. Find every place where this spec is INSUFFICIENT for multi-day autonomous execution. Output JSON array of findings with severity/category/finding/question_to_user/proposed_addition. Be ruthless. If truly complete, return []."

Adversary 결과의 blocker/high는 에이전트가 사용자 질문으로 변환해 인터뷰에 다시 끌어들인다. medium/low는 spec.notes에 기록 — 작업 시 LLM이 참고.

시스템 안전망: 최소 1라운드, 최대 10라운드. adversary가 빈 배열 반환 후에야 saturation 가능.

### 6.3 Saturation 객관 정의

다음이 모두 충족되면 saturation:
1. 최근 N개 답변에서 spec 신규 슬롯/제약/검증 추가 비율 < 10% (anti-thrashing)
2. 마지막 adversary 라운드가 빈 배열 반환
3. 필수 슬롯 모두 채워짐

이 측정은 stateless로 매 턴 재계산. 인터뷰 중단/재개 가능.

### 6.4 Self-Audit Gate (인터뷰 전환에만)

원칙 2.4의 구체화. 인터뷰에 두 곳의 self-audit gate가 있다:

**Gate 1: Sensing → Conversation 전환**
에이전트가 자문: "도메인 추정이 다음 질문 던지기에 충분한가? 모호함이 너무 크면 한 번 더 자유 질문을 던져야 하나?" 결과는 `interview.gate_audit` 이벤트로 기록.

**Gate 2: Conversation → Freeze 전환** (가장 중요)
에이전트가 자문: "필수 슬롯 다 채웠는가? adversary 모든 finding 해소됐는가? spec 내부에 모순 없는가? 며칠 무인 실행에 정말 충분한가?" 통과 못 하면 freeze 차단, 추가 질문 라운드 진입.

각 게이트는 1회만 — 무한 재귀 방지.

### 6.5 가시화

`gil status <id>` 출력 (인터뷰 중):

```
🎤 Session 01HXY... — Interview in progress
Stage:    2/3 (Agent-driven Conversation, adversary round 2/10)
Turns:    47
Spec slots filled:
  ✓ goal.one_liner
  ✓ goal.success_criteria_natural (3/3)
  ✓ constraints.tech_stack
  ⏳ verification.checks (4/7 estimated)
  ✗ workspace.backend
  ✗ models.adversary
  ✗ risk.autonomy
Adversary findings (last round): 2 blockers, 3 high → answering...
Saturation:    62% (estimated)
Estimated time remaining: ~30 min
```

---

## 7. Stop Condition — 하이브리드

stop은 의미적으로 1개("현실적으로 작업이 불가") 지만 검출 채널이 3개 있다. 모든 채널은 같은 결론 메시지로 수렴한다.

### 7.1 채널 1: Verifier (성공 판정자)

마일스톤 단위 또는 에이전트가 `verify_now` 도구를 명시 호출할 때 실행. spec.verification.checks를 모두 돌려 결과를 모은다. 한 check 실패가 즉시 stop이 아님 — 결과 전체를 LLM에 피드백, 다음 턴에 재시도. `max_retries_per_check`까지.

모든 check 통과 = **작업 완료**. 화면에 출력하고 데몬은 세션을 done 상태로 마무리.

### 7.2 채널 2: StuckDetector + Recovery (시맨틱 막힘 + 자가 회복)

OpenHands 5패턴을 시맨틱(객체 ID 아닌 의미 기반) 비교로 감지:
1. 동일 Action–Observation 4회 반복
2. 동일 Action–Error 3회 반복
3. 사용자 입력 없는 monologue 3+
4. 두 페어 핑퐁 6+ 사이클
5. 컨텍스트 윈도우 에러 반복

OpenHands는 detect 후 halt하지만 gil은 다르다 — 자가 회복 5단계를 시도한다 (spec.risk.recovery에 순서 선언):

1. **alt_tool_order**: 같은 목표 다른 도구 조합
2. **model_escalate**: 메인 모델 → escalation_chain[0] (더 강한 모델)
3. **subagent_branch**: fresh subagent에 같은 작업 위임 (cold start)
4. **reset_section**: 마지막 N step 롤백 (shadow git checkout) 후 재시도
5. **adversary_consult**: adversary가 막힌 이유 분석 + 제안

5단계 다 실패해야 stop ("stuck after recovery").

### 7.3 채널 3: Budget (자원 한도)

토큰/USD/wall-clock/iteration 한도 enforcement. 한도 도달 시:
1. **Grace call** (Hermes 패턴): 정확히 1회 추가 호출 허용 — 모델이 깨끗한 마무리 메시지 작성할 기회
2. **Escalation chain**: spec.budget에 escalation 허용된 경우 다음 모델로
3. 다 소진 → stop ("budget exhausted")

### 7.4 환경 장애는?

별도 채널 없음. LLM API 끊기면 다음 호출 실패 → retry-backoff → 계속 실패하면 채널 2(같은 에러 반복) 또는 채널 3(시간 초과)이 자동으로 잡는다. 별도 EnvHealth 모니터는 거짓 양성 위험만 늘리므로 두지 않는다.

### 7.5 Stop 보고

화면에만 출력. 별도 파일 없음.

```
✅ Session 01HXY... done
Started:   2026-04-25 21:00 KST
Ended:     2026-04-28 03:14 KST  (54h 14m)
Tokens:    12.4M / Cost: $87.42
Changed:   142 files, +8421 / -2103 lines
Verified:  7/7 checks passed

Inspect:   gil events 01HXY... --tail
Diff:      gil diff 01HXY...
Merge:     gil merge 01HXY...
```

stop 시는 동일 형식, 다만 첫 줄이 `❌ Session ... stopped`, reason 1줄 추가.

---

## 8. 컨텍스트 관리

며칠 + 100M 토큰 작업의 *지속력* 을 결정한다.

### 8.1 캐시 보존 압축 (Head / Middle / Tail)

Hermes 패턴이 결정적. Anthropic prompt cache가 살아있으면 같은 prefix는 90% 할인. 며칠짜리 작업에서 이게 깨지면 비용이 5~10배 차이.

**Head**(시스템 프롬프트 + 초기 메시지) 절대 미수정. **Tail**(최근 토큰 일정량) 보존. **Middle**만 LLM이 구조화된 마크다운 요약으로 대체.

원본 메시지 리스트는 `deepcopy` 후 사본에만 cache_control marker 부착 — 원본 절대 변형 금지. session DB에 저장된 시스템 프롬프트는 재사용 (압축 후에도 prefix cache hit 유지).

### 8.2 압축 트리거

시스템은 95% 강제 트리거 안전망만 둔다. 그 이전엔 에이전트가 컨텍스트 사용률을 보고 자기 판단. Anti-thrashing: 최근 두 번 압축이 둘 다 10% 미만 절감하면 다음 압축 skip (Hermes 패턴).

### 8.3 요약 템플릿 (OpenCode 차용)

압축 결과는 다음 구조의 마크다운:

```markdown
## Goal
- [single-sentence task summary]

## Constraints & Preferences
- ...

## Progress
### Done
- ...
### In Progress
- ...
### Blocked
- ...
```

"그냥 요약" 보다 훨씬 일관적. LLM이 이어받기 좋다.

### 8.4 압축 후 turn

시스템이 합성 메시지를 만들지 않고, 에이전트가 "방금 압축됐고 다음 단계로 넘어가겠다"는 짧은 자기 발화 1턴 — Hermes 패턴.

### 8.5 Anthropic 캐시 전략 "system-and-3"

Hermes 그대로. messages[0] (system) + 마지막 3개 non-system에 cache_control marker. 최대 4 breakpoints (Anthropic 제한).

---

## 9. 메모리 뱅크 — 6 마크다운 진짜 구현

Cline은 이 패턴을 시스템 프롬프트로 시키기만 했지 코드로 구현 안 했다. gil은 진짜로 만든다.

### 9.1 6개 파일

`~/.gil/sessions/{id}/memory/`:
- `projectbrief.md` — 한 번에 알 수 있는 프로젝트 요약
- `productContext.md` — 왜 / 누가 / 어떤 사용자
- `activeContext.md` — 지금 무엇을 하고 있는가
- `systemPatterns.md` — 사용 중인 아키텍처 패턴
- `techContext.md` — 기술 스택 / 의존성 / 제약
- `progress.md` — 완료 / 진행 중 / 막힘

freeze 시 frozen spec에서 자동 초기 채움. 작업 진행하며 갱신.

### 9.2 명시적 갱신 도구

에이전트가 `memory_update` 1급 도구로 갱신:
```
memory_update(file: "progress", section: "Done", append: "...")
memory_update(file: "activeContext", replace: true, content: "...")
```

문자열 치환이 아닌 의미 단위 갱신. 에이전트가 직접 파일을 read/write할 수도 있지만 명시적 도구가 권장 경로.

### 9.3 마일스톤 갱신 게이트

verifier check 통과될 때 에이전트가 자기 검사 1회: "지금까지 한 일 메모리에 반영해야 할 게 있나?" 후 갱신.

### 9.4 압축에서 보존

메모리 뱅크 6개 파일의 *현재 내용* 은 시스템 프롬프트에 항상 prepend. 너무 크면 progress.md만 prepend하고 나머지는 RAG식으로 필요할 때 로드 (에이전트가 `memory_load` 도구로 호출).

### 9.5 Resume 시 자동 로드

`gil resume` 하면 메모리 뱅크 + frozen spec + 최근 N step만 새 컨텍스트로 시작. 며칠 전의 turn-by-turn history는 RAG로 fetch.

---

## 10. 체크포인트 — Shadow Git

### 10.1 위치와 격리

`~/.gil/shadow/{cwd-hash}/.git`. `core.worktree` 설정으로 사용자 작업 디렉토리를 가리킨다 — Cline 패턴 그대로. 사용자의 진짜 `.git`은 절대 접근 안 한다.

Identity: `user.name="gil checkpoint"`, `user.email="checkpoint@gil.local"`. 커밋 메시지: `step-{step-id}-{tool}-{summary}`. 플래그: `--allow-empty --no-verify`.

### 10.2 매 step 스냅샷

도구 호출(편집/실행) 직후 1 커밋. opencode식 git write-tree → read-tree → checkout-index 패턴으로 비파괴 롤백 가능. 수천 커밋이 쌓여도 상관없음 — git gc 주기적으로.

### 10.3 Exclusions

`.gilignore` 또는 spec.workspace.exclude_patterns. 디폴트는 Cline의 합리적 셋:
- `node_modules/`, `build/`, `dist/`, `target/`
- 미디어 (이미지/비디오)
- 캐시 (`.next/`, `.pytest_cache/`)
- 시크릿 (`.env`)
- 데이터베이스 파일
- 로그
- 중첩 git 저장소 (`.git_disabled` 접미사로 임시 비활성화)

### 10.4 3-way 복원

자가 회복의 `reset_section` 전략이 사용:
- `task` (대화만): 이벤트 로그 트림
- `workspace` (파일만): `git reset --hard <hash>`
- `taskAndWorkspace` (둘 다)

### 10.5 사용자 git 통합

작업 끝나면 `gil merge`가 shadow git에서 새 브랜치 만들어 사용자 작업 디렉토리에 push. 사용자가 PR로 검토. 또는 `gil diff`로 단일 패치만 뽑아 사용자 직접 적용도 가능.

---

## 11. 서브에이전트

### 11.1 컨텍스트 격리

부모가 작업 위임 시 부모의 `_last_resolved_tool_names`를 save. 자식은 더 좁은 도구 셋으로 시작. 자식 종료 시 *결과 요약만* 부모 컨텍스트에 흡수. turn-by-turn은 부모로 새지 않음 — 컨텍스트 폭발 방지의 핵심.

### 11.2 다계층 (grandchild OK)

`spec.budget.max_subagent_depth` 한도 (기본 3). opencode 1단보다 넓게 — 풀스택 작업 본질상 2~3단이 자연스러움.

### 11.3 Heartbeat + stale detection

자식이 너무 오래 in_tool에 머무르면 부모가 timeout 결정. in_tool / idle 임계값 분리 (Hermes 패턴).

### 11.4 dispatch 결정

에이전트가 결정. 시스템 트리거 강제 안 함. (인터뷰 전환과 달리 self-audit gate는 두지 않음 — 원칙 2.4)

---

## 12. Sandbox & Runtime

### 12.1 백엔드 추상화

`runtime/` 패키지가 통일 인터페이스 제공:
- `local`: OS 샌드박스 (Linux: bwrap+seccomp, macOS: Seatbelt)
- `docker`: 컨테이너
- `ssh`: 원격
- `vm`: 클라우드 VM (Modal/Daytona — v2)

선택은 spec.workspace.backend. 인터뷰 중 에이전트가 위험도/도메인 보고 추천, 사용자 확인.

### 12.2 Linux Sandbox (Codex 차용)

bubblewrap 인자 (Codex `linux-sandbox` 그대로):
- `--new-session --die-with-parent --bind / / --unshare-user --unshare-pid` (+`--unshare-net` 조건부)
- 모드별 마운트: `read-only` (`--ro-bind / /`), `workspace-write` (작업 디렉토리만 `--bind`), `danger-full-access` (샌드박스 비활성)
- 보호 경로 (`.git`, `.gil`, `.codex`) 재바인드 read-only
- 네트워크 모드: FullAccess / Isolated / ProxyOnly
- ProxyOnly에선 TCP→UDS→TCP 프록시 브리지 (Codex `proxy_routing` 그대로)

### 12.3 macOS Sandbox

Codex의 Seatbelt 프로파일 차용. `sandbox-exec` + 모드별 .sb 파일.

### 12.4 Docker

OpenHands 패턴: 베이스 이미지 + ActionExecutionServer 주입. 단순화 — 우리는 별도 inner-server 안 두고, gild가 docker exec로 직접 명령 실행. 컨테이너 내부에 gil 코드는 안 들어감.

---

## 13. 도구 시스템

### 13.1 빌트인 도구

- 파일: `read`, `write`, `edit`, `apply_patch`
- 검색: `grep`, `glob`
- 셸: `bash`, `exec` (다단계 압축)
- 코드: `repomap`, `go_to_def`, `find_refs`
- 웹: `web_fetch`, `web_search`
- 메타: `memory_update`, `memory_load`, `verify_now`, `subagent`
- 모드: `plan_exit` (Plan→Build 전환)

### 13.2 도구 형식

JSON tool-use (provider native function calling). Cline의 XML 태그 형식 대신 — 메이저 프로바이더 모두 native function calling 지원하므로 더 깔끔하고 파싱 안정.

### 13.3 apply_patch DSL (Codex 그대로)

```
*** Begin Patch
*** Update File: path/to/file
@@ context_line
- old line
+ new line
*** End Patch
```

ParseMode: Strict / Lenient(GPT-4.1 호환) / Streaming.

### 13.4 SEARCH/REPLACE 4단 매칭 (Aider 차용)

`apply_patch`보다 LLM이 잘 만드는 형식. 4단 폴백:
1. 완벽 일치
2. 공백 유연
3. `...` 생략 확장
4. 퍼지 매칭 (SequenceMatcher ≥0.8, ±10% 길이)

실패 시 "Did you mean…" 힌트 + reflected_message로 LLM에 자가 수정 요청. `max_reflections = 3`.

### 13.5 다단계 도구 압축 (Hermes execute_code)

`exec` 도구 — 중간 도구 결과가 컨텍스트에 안 들어가는 패턴. LLM이 Python 스크립트 작성, 스크립트가 UDS RPC로 gil 도구들 호출. 7개 sandbox 도구만 허용 (`web_search/web_extract/read_file/write_file/search_files/patch/terminal`). DEFAULT_TIMEOUT=300s, MAX_TOOL_CALLS=50, MAX_STDOUT_BYTES=50KB.

### 13.6 권한

OpenCode 패턴. `Rule { permission, pattern, action: "allow"|"deny"|"ask" }`. `findLast` 매칭 (명시적 규칙 우선). Glob: minimatch 호환.

자율성 다이얼 (spec.risk.autonomy):
- `PLAN_ONLY`: 읽기 전용 도구만
- `ASK_PER_ACTION`: 모든 액션 사용자 확인 (없을 듯, 며칠 무인이라)
- `ASK_DESTRUCTIVE_ONLY`: 파괴적 액션만 확인
- `FULL`: 디폴트 — 절대 안 묻음

### 13.7 Adversary Reviewer (Goose 차용 + 변형)

`~/.gil/adversary.md` 또는 spec.risk에 인라인. 도구 호출 전 비동기 검수 (병렬). 시스템 프롬프트:

> "You are an adversarial security reviewer, protecting the user in case the other agent is rogue. Decide if this tool call is safe given the user's task and rules. Respond with ALLOW or BLOCK on the first line, then a brief reason."

**Goose와 다른 점**: BLOCK 시 즉시 중단하지 않고 → 모델에 reason 전달 → 다른 접근 시도. 같은 action 반복 BLOCK → stuck signal로 escalate. **Fail-closed 디폴트** (Goose는 fail-open) — 며칠 무인이라 안전 우선. `risk.adversary_fail_open=true` 옵션으로 변경 가능.

### 13.8 MCP 통합

`mcp/` 패키지. Hermes/Goose 패턴:
- builtin (gild 내부)
- stdio (자식 프로세스)
- streamable_http (원격)

spec.tools.mcp_servers에 활성 서버 목록. 권한은 일반 도구와 같은 시스템 통과.

---

## 14. Provider 추상화

### 14.1 인터페이스

```go
type Provider interface {
    Name() string
    Models() []Model
    Complete(ctx, req) (Stream, error)
    SupportsCacheControl() bool
    SupportsThinking() bool
}

type Stream interface {
    Recv() (Chunk, error)  // text | tool_call | metrics | finish
    Close() error
}
```

### 14.2 지원 프로바이더 (v1)

- Anthropic (Claude Opus 4.7, Sonnet 4.6, Haiku 4.5) — 1순위
- OpenAI (GPT-5, GPT-5-mini)
- Google (Gemini 3 Pro)
- Local (Ollama, llama.cpp) — 모델 능력에 따라 일부 기능 제한

### 14.3 모델 역할 (Aider/Cline 차용)

- `models.main`: 추론·계획 (강한 모델)
- `models.weak`: 커밋 메시지·요약·단순 분류 (저렴, Haiku급)
- `models.editor`: SEARCH/REPLACE 적용 (빠른 모델, Sonnet급)
- `models.adversary`: 안전 검수 (별도, 추천: 다른 프로바이더)
- `models.interview`: 인터뷰 진행 (강한 모델 권장)
- `models.escalation_chain`: 자가 회복 시 사용 (점점 강한 모델)

### 14.4 인증

API key 우선. 환경 변수 (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) 또는 `~/.gil/credentials.toml` (mode 0600). Claude Code OAuth credentials (`sk-ant-oat*`) 도 지원 (Hermes 차용).

---

## 15. 데이터 레이아웃 (`~/.gil/`)

```
~/.gil/
├── credentials.toml             # API keys (0600)
├── adversary.md                 # 글로벌 adversary 규칙 (선택)
├── config.toml                  # 글로벌 디폴트
│
├── sessions/
│   ├── sessions.db              # SQLite (메타데이터/메시지/통계)
│   ├── {session-id}/
│   │   ├── spec.yaml            # frozen spec (사람용)
│   │   ├── spec.lock            # SHA-256 (freeze 후)
│   │   ├── events/
│   │   │   ├── {n}.json         # 개별 이벤트
│   │   │   └── cache/{a-b}.json # 페이지 캐시 (25 events/page)
│   │   ├── memory/
│   │   │   ├── projectbrief.md
│   │   │   ├── productContext.md
│   │   │   ├── activeContext.md
│   │   │   ├── systemPatterns.md
│   │   │   ├── techContext.md
│   │   │   └── progress.md
│   │   └── rollout.jsonl        # Codex식 누적 JSONL (resume용)
│   │
│   └── archived/                # 오래된 세션
│
├── shadow/
│   └── {cwd-hash}/
│       └── .git/                # shadow git
│
├── microagents/                 # 글로벌 microagents
└── skills/                      # agentskills.io 표준
```

### 15.1 SQLite 스키마

Goose v11 스키마 차용 + 단순화:
- `sessions(id, spec_id, status, started_at, ended_at, total_tokens, total_cost_usd, ...)`
- `messages(id, session_id, role, content_json, created_at, tokens, ...)`
- `events_index(id, session_id, event_id, file_path, ...)` — JSONL 파일에 대한 인덱스
- `verification_results(id, session_id, check_name, attempt, passed, exit_code, ...)`

### 15.2 이벤트 스키마

```json
{
  "id": 1234,
  "timestamp": "2026-04-25T21:30:00.123Z",
  "source": "AGENT|USER|ENVIRONMENT|SYSTEM",
  "kind": "action|observation|note",
  "type": "tool_call|tool_result|llm_request|llm_response|interview_question|gate_audit|...",
  "data": { ... },
  "cause": 1230,           // 선행 이벤트 ID
  "metrics": { tokens, cost, latency_ms }
}
```

이벤트는 시크릿 마스킹 자동 적용 (`<secret_hidden>`).

---

## 16. 에러 처리

### 16.1 도구 실패

도구 실패는 `observation.error` 이벤트로 기록 + LLM에 다음 턴 input. 에이전트가 자기 회복 시도. 같은 도구가 반복 실패하면 채널 2(StuckDetector)가 잡는다.

### 16.2 LLM API 실패

retry-backoff (exponential, max 5회). 영구 실패면 채널 2가 잡거나 budget 시간 초과로 채널 3.

### 16.3 시스템 패닉

gild가 panic하면 systemd/launchd가 재시작. 세션은 이벤트 로그에서 재개 가능 (incomplete 자동 감지). 재시작 시 진행 중이던 세션은 `auto-paused` 상태로 표시 — 사용자가 `gil resume` 명시.

### 16.4 클라이언트 연결 끊김

gild는 영향 안 받음. 클라이언트가 재연결하면 그 시점부터 이벤트 스트림 재구독.

---

## 17. 테스트 전략

### 17.1 단위 테스트

각 패키지 내부. Go 표준 testing + `testify/require`.

### 17.2 통합 테스트

`tests/integration/` 트리 분리. 실제 SQLite, 실제 shadow git, mocked LLM provider.

### 17.3 E2E 테스트

전체 라이프사이클: 인터뷰 → freeze → run → verify → stop.
LLM은 mocking — 결정적 응답 시퀀스 (replay testing).

### 17.4 자율성 시뮬레이션

며칠 무인 실행을 시뮬레이션하는 fixture: 도구 호출 N개 → 검증 통과. 다양한 stuck/recovery 시나리오 fixture.

### 17.5 Dogfood

gil 자체 기능 추가/수정을 gil로 한다. 첫 dogfood 작업이 gil의 첫 자율 작업 시연이 된다.

---

## 18. 통신 프로토콜 (gRPC)

### 18.1 .proto 정의

`proto/gil/v1/`:
- `spec.proto`: FrozenSpec + 슬롯 타입
- `event.proto`: Event 직렬화
- `session.proto`: SessionService (CRUD)
- `interview.proto`: InterviewService (bidirectional streaming)
- `run.proto`: RunService (bidirectional streaming)
- `tool.proto`: 도구 정의 메타

### 18.2 핵심 RPC

```proto
service SessionService {
  rpc Create(CreateRequest) returns (Session);
  rpc Get(GetRequest) returns (Session);
  rpc List(ListRequest) returns (ListResponse);
  rpc Status(StatusRequest) returns (StatusResponse);
  rpc Stop(StopRequest) returns (Empty);
  rpc Resume(ResumeRequest) returns (Empty);
  rpc Fork(ForkRequest) returns (Session);
}

service InterviewService {
  rpc Start(StartRequest) returns (stream InterviewEvent);
  rpc Reply(stream UserReply) returns (stream InterviewEvent);
  rpc Confirm(ConfirmRequest) returns (FrozenSpec);
}

service RunService {
  rpc Start(StartRequest) returns (stream RunEvent);
  rpc Attach(AttachRequest) returns (stream RunEvent);
}

service EventService {
  rpc Tail(TailRequest) returns (stream Event);
  rpc Query(QueryRequest) returns (stream Event);
}
```

### 18.3 트랜스포트

기본: Unix Domain Socket (`~/.gil/gild.sock`) — 로컬 효율 최대.
TCP 옵션: `gil daemon --listen 127.0.0.1:7878` — 원격 클라이언트.

---

## 19. 운영적 고려

### 19.1 첫 실행 마법사

`gil` 첫 실행 시:
1. `~/.gil/` 생성
2. credentials.toml 부재 시 인터랙티브 입력 (Anthropic API key 우선)
3. 디폴트 모델 추천 (Claude Opus 4.7 + Sonnet 4.6)
4. 데몬 자동 spawn

### 19.2 시스템 서비스 등록 (v2)

`gil daemon install` — systemd unit 또는 launchd plist 자동 생성.

### 19.3 업데이트

`gil update` — 새 바이너리 download, 데몬 재시작. 진행 중이던 세션은 `auto-paused` 상태로 표시되며 사용자 명시적 `gil resume`이 필요하다 (16.3과 동일 정책 — 안전 우선).

---

## 20. 미해결 / v2 항목

- 클라우드 VM 백엔드 (Modal/Daytona) 어댑터
- HTTP/JSON 호환 (grpc-gateway) — 브라우저/curl
- VS Code 확장 (gil SDK 사용)
- 웹 UI (gild에 HTTP 서버 옵션)
- 다중 사용자 (현재는 단일 사용자 가정)
- gil↔gil 협업 (한 gild가 여러 작업, 한 작업이 여러 gild)
- Atropos RL 통합 (학습된 도구 호출 최적화)
- Honcho식 cross-session 사용자 모델링
- 로컬 모델 special handling (작은 컨텍스트 윈도우 대응)

---

## 부록 A. 참조 도구별 차용 매트릭스

| 도구 | 차용 자산 |
|---|---|
| **OpenHands** | EventStream 구조, StuckDetector 5패턴, LLMSummarizingCondenser 비파괴 압축, microagents YAML+md |
| **Codex** | linux-sandbox bwrap+seccomp, apply-patch DSL, rollout JSONL+resume, AGENTS.md→developer_instructions |
| **Hermes Agent** | execute_code 다단계 압축, 캐시 보존 압축 불변식, IterationBudget grace call, system-and-3 cache control |
| **OpenCode** | 서버-TUI 분리(HTTP→gRPC로), git write-tree 스냅샷, 구조화 압축 템플릿, permission glob, plan/build 모드 |
| **Aider** | tree-sitter+PageRank repomap, 이진검색 토큰 피팅, SEARCH/REPLACE 4단 매칭 + reflected_message, architect/editor 모델 분리 |
| **Cline** | shadow git 체크포인트(core.worktree), 9-카테고리 auto-approve, .clinerules conditional, mode-specific provider |
| **Goose** | Recipe DSL + retry.checks (stop signal 핵심), MCP 6 백엔드 추상화, adversary_inspector, GooseMode 자율성 다이얼, SQLite 세션 스키마 |

## 부록 B. 핵심 코드 파일 (구현 시작 위치)

| 패키지 | 첫 파일 | 책임 |
|---|---|---|
| `core/event` | `stream.go` | append-only log + pub/sub |
| `core/spec` | `proto.go` | proto 변환 + lock |
| `core/interview` | `engine.go` | Stage 머신 + self-audit |
| `core/verify` | `runner.go` | shell 단언 실행 |
| `core/stuck` | `detector.go` | 5패턴 시맨틱 비교 |
| `core/checkpoint` | `shadow.go` | shadow git 작업 |
| `core/compact` | `compactor.go` | Head/Middle/Tail 압축 |
| `core/memory` | `bank.go` | 6 마크다운 관리 |
| `core/edit` | `editblock.go` | 4단 매칭 |
| `core/patch` | `parser.go` | apply_patch DSL |
| `core/exec` | `rpc.go` | UDS RPC 다단계 |
| `core/budget` | `budget.go` | 자원 한도 + grace |
| `core/permission` | `evaluate.go` | findLast + glob |
| `core/adversary` | `reviewer.go` | LLM 검수 |
| `core/provider/anthropic` | `client.go` | Anthropic 어댑터 |
| `runtime/local` | `bwrap.go` | Linux sandbox |
| `runtime/local` | `seatbelt.go` | macOS sandbox |
| `server/service` | `session.go` | gRPC 서비스 구현 |
| `cli/cmd` | `root.go` | cobra 루트 |
| `tui/model` | `app.go` | Bubbletea root |
