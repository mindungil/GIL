# subagent — 부모-자식 위임 (design)

Status: design (G5)

## 1. 동기

며칠짜리 자율 실행에서 단일 agent loop는 자주 막힌다. 큰 작업을 작은
컨텍스트로 잘라 병렬·격리 탐색을 하려면 *부모가 자식 agent를 띄우고 결과만
받아오는* 1급 메커니즘이 필요하다. 현재 gil M5는 turn 내 verify-loop만
가지며, 사용자 prompt 1개당 단일 agent loop만 돈다.

codex는 `agent/{registry,mailbox,role,control}` + `spawn_agent / wait_agent /
send_input` tool 세트를 1급으로 가지고 있다 (`research/codex/codex-rs/core/
src/agent/`, `tools/handlers/multi_agents_spec.rs`). gil은 같은 자리를 비워두고
있고 (memory: `project_gil_phase_sequencing.md` § 갭), M5.4 honest 섹션이
"다음" 목록 첫 줄에 적어둔 항목이다.

이 문서는 코드 0줄로 시작해 슬롯·계약·플로우만 픽스한다. 구현은 별도
phase에서 진입.

## 2. 원칙 (memory 합치)

- **에이전트가 결정.** 어떤 subagent를 띄울지 / 무엇을 위임할지 / 언제
  대기 끝낼지 — 전부 *부모 agent의 tool decision*. 시스템에 고정 trigger
  임계값·고정 분류 분기 박지 않음.
- **시스템은 안전망만.** registry로 동시 subagent 수 / 최대 깊이 / spec
  budget 한도 / 격리(별도 workspace + spec slice) / 결과 영속화만 enforce.
- **가지치기 금지.** subagent "유형"을 코드에 박지 않음. codex의 builtin
  awaiter/explorer 같은 toml registry는 *V2 옵션*. V1은 부모가 자유로운
  자연어 message로 자식의 system_prompt를 채움.
- **자연어 단일 surface.** 사용자는 subagent를 직접 부르지 않음. "병렬로
  알아봐줘" 같은 자연어 → 부모 agent가 `spawn_agent` tool 호출.

## 3. 데이터 모델

### 3.1 SubagentSpec — FrozenSpec slice

FrozenSpec proto에 이미 `Budget.max_subagent_depth`가 있다 (depth 한도만
존재, 실제 spec slice 정의는 없음). 추가:

```protobuf
// proto/gil/v1/spec.proto — FrozenSpec에 1개 필드 추가
message FrozenSpec {
  // ... 기존 필드 ...
  SubagentPolicy subagent = 22;
}

message SubagentPolicy {
  // 부모 spec의 어떤 슬롯을 자식이 상속하는지. 미지정 = 전체 상속.
  bool inherit_workspace = 1;       // path/backend
  bool inherit_models = 2;          // ModelChoice main + planner + editor
  bool inherit_tools = 3;           // Tools.bash, file_ops, ...
  bool inherit_verification = 4;    // Verification.checks
  bool inherit_constraints = 5;

  // 한도 — 부모 한도의 비율 (0~1.0). 0 = 한도 없음.
  int64  max_iterations_per_subagent = 10;
  int64  max_tokens_per_subagent = 11;
  double max_cost_per_subagent_usd = 12;
}
```

부모는 freeze_spec 단계에서 이 정책을 채울 수 있다(에이전트가 결정). 미지정
시 default: 모든 inherit_* true, per-subagent 한도는 부모 한도의 ⅓.

### 3.2 Session 계층

기존 session.Repo는 flat이다. subagent 부모-자식 관계 추가:

```sql
-- migration v3
ALTER TABLE sessions ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN subagent_depth INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN subagent_label TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_sessions_parent ON sessions(parent_session_id);
```

`subagent_depth = parent.subagent_depth + 1`. `subagent_label`은 부모가 zo
띄울 때 자식을 식별하기 위해 단 nickname (codex의 nickname과 같음 — 자유
문자열, 충돌 시 시스템이 suffix). `parent_session_id`가 빈 문자열이면 root.

### 3.3 SubagentRegistry — 동시 한도 enforcement

`server/internal/service/subagent_registry.go`:

```go
type subagentRegistry struct {
    mu           sync.Mutex
    activePerRoot map[string]int   // rootSessionID → count
    maxPerRoot   int               // 시스템 한도 (default 8)
    maxDepth     int               // 시스템 한도 (default 3)
}
```

- `Spawn(parentID)`: depth/count 검사 후 자식 row 생성, count++ 반환 err
- `Done(rootID)`: count--
- root 추적은 sessions.parent_session_id를 transitive로 따라 올라가 가장
  위 ancestor를 찾는 쿼리 1회 (or recursive CTE).

V1 한도는 시스템 상수. V2에서 사용자가 spec.subagent에 override 가능.

## 4. Tool 표면 (chat agent의 default toolset에 추가)

기존 G1 lifecycle 도구와 동일 패턴 (`agent_tools_subagent.go`):

### `spawn_agent`

```json
{
  "type": "object",
  "properties": {
    "label": {"type": "string", "description": "Short identifier for this subagent (lowercase, used in logs and wait_agent)."},
    "task": {"type": "string", "description": "Required. The instruction the subagent receives as its first user message."},
    "agent_type": {"type": "string", "description": "Which agent profile to use (default / explore / plan). Default: default."},
    "spec_override": {
      "type": "object",
      "description": "Optional overrides — narrows what the subagent inherits from the parent spec.",
      "properties": {
        "workspace_path": {"type": "string"},
        "tools_allowlist": {"type": "array", "items": {"type": "string"}},
        "max_iterations": {"type": "integer"}
      }
    }
  },
  "required": ["label", "task"]
}
```

Behavior:
1. Lookup parent session, check spec.subagent existence / depth.
2. Apply subagent_registry.Spawn — fails with clear error if at limit.
3. Build child FrozenSpec by slicing parent + applying spec_override.
4. Create child session (parent_session_id=parent, depth=parent+1,
   label=args.label).
5. Spawn child runner detached (RunService.Start with detach=true, child
   session_id, sliced spec).
6. Return `{"agent_id": child_session_id, "label": args.label}` to parent.

### `wait_agent`

```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"},
    "label": {"type": "string"},
    "timeout_seconds": {"type": "integer", "description": "Hard cap; default 600."}
  },
  "anyOf": [
    {"required": ["agent_id"]},
    {"required": ["label"]}
  ]
}
```

Behavior:
1. Resolve target child session (by id or by label among parent's children).
2. Poll session.Status — block agent loop until status ∈ {"done", "failed",
   "stopped", "budget_exceeded"} or timeout.
3. Return child's final state + verify_results + summary (last assistant
   message from chatHistory).

### `agent_status`

```json
{"type":"object","properties":{},"additionalProperties":false}
```

Returns list of parent's live children: `[{label, agent_id, status, iter,
tokens, cost}]`. Lets parent peek without blocking.

## 5. Lifecycle

```
parent.tool.spawn_agent(label="explore-auth", task="grep for OIDC handler sites and summarize")
  → child session created (parent.session_id=parent, depth=1)
  → child RunService.Start (detached) with sliced spec
  → parent receives {agent_id: "01K...", label: "explore-auth"}
parent.tool.spawn_agent(label="explore-db", task="...")   // 병렬
parent.tool.wait_agent(label="explore-auth")
  → blocks until child status terminal
  → returns child's final message + verify results
parent integrates findings; may spawn more or report to user
```

terminal states (자식 → 부모 안 부름):
- `done` — verify checks all green
- `failed` — verify failed or AskCallback ran out of approval budget
- `stopped` — parent (or stuck detector / budget exhaust) killed
- `budget_exceeded` — per-subagent budget hit

부모는 wait_agent에서 final state를 받으므로, 자식의 상태 변화를 push받는
별도 채널은 V1 없음 (codex Mailbox 패턴은 V2 옵션 — InterAgentCommunication
이 필요할 때).

## 6. 격리

### 6.1 Workspace
- Default: 자식이 부모의 workspace.path를 그대로 상속.
- Override: spec_override.workspace_path로 자식 전용 디렉토리 (parent.path/
  .subagent/<label>/).
- Backend: 부모와 동일 (LOCAL_NATIVE / DOCKER / 등). 같은 backend를 공유해
  shadow-git race가 없도록 자식 작업은 *별도 branch* (sub/<label>) 위에 쌓고
  부모가 wait_agent 후에 명시적으로 merge tool로 통합 (별도 단계, 본 design
  범위 밖).

### 6.2 Tool budget
- 자식 tool registry는 부모의 부분집합 (spec_override.tools_allowlist).
- 시스템 기본: 자식은 *spawn_agent를 다시 호출 불가*. depth 1을 hard cap으로
  V1 시작. spec.subagent.max_depth로 V2에서 풀 수 있음.

### 6.3 Permission propagation
- 자식의 AutonomyDial은 부모와 동일 (codex와 다른 가정 — gil은 사람 안 부르는
  설계). 자식이 ASK 상황을 만나면: 부모의 pendingAsks 큐로 burst 없이
  단일 ask 이벤트를 통과시켜 사용자 1명에게만 묻는다 (실제 V1 자율 실행에선
  AutonomyDial=FULL이 default이므로 ask 자체가 드뭄).

## 7. Persistence

- 자식 session.Status 변화는 v1 sessions 테이블에 그대로 영속화.
- 자식의 plan_steps/verify는 G3 v2 schema에 그대로 영속화 — 자식
  session_id로 격리됨.
- 부모-자식 트리는 sessions.parent_session_id의 재귀로 재구성.
- daemon 재시작 시 wait_agent 중이던 부모는 streaming 중이 아니므로 영향
  없음 — wait_agent는 새 tool call로 다시 부르면 자식 상태가 이미 terminal이면
  바로 결과 반환.

## 8. 비범위 (V1에서 안 함)

- **InterAgentCommunication (Mailbox)** — codex의 자식→부모 push 채널. V1은
  부모 wait_agent의 pull only. 충분한 patterns가 누적되면 V2 추가.
- **builtin subagent type registry** (codex awaiter/explorer.toml). 가지치기
  금지 원칙. 사용자 spec_override가 충분.
- **자식의 자식**. depth=1 hard cap. 명백한 use case가 나오면 풀어.
- **Cross-subagent shared state**. 자식들은 격리 — 통신 필요하면 부모를 통해
  pull.
- **streaming 자식 출력 to parent** (자식이 매 chunk를 부모로 흘림). wait_agent
  return 시 최종 메시지만.

## 9. 가시화 — M6 tree pane 통합

M6의 agent tree pane은 turn 안의 tool calls를 트리로 보여준다. spawn_agent
tool call이 fire되면:
- 트리에 `spawn_agent label=explore-auth` 노드 (status=running)
- 자식 session의 진행을 옆에 small inline progress bar — 또는 별도 자식 트리.
- wait_agent 결과로 status → ok/failed 전이.

상세 layout은 G2-UI (M6.3-6.6) 단계에서 결정.

## 10. 검증 시나리오 (구현 완료 후)

1. **간단 위임**: 부모가 spawn_agent("count test files", "find . -name '*_test.go' | wc -l") → wait_agent → 부모가 결과를 받아 사용자에게 보고.
2. **병렬 탐색**: 부모가 2개 동시 spawn (auth audit + db audit) → 두 wait_agent → 통합 보고.
3. **자식 실패 복구**: 자식이 verify failed로 끝남 → 부모가 final state 받아 다른 spawn 시도.
4. **한도 enforcement**: 부모가 maxPerRoot+1개를 spawn 시도 → 마지막 호출이 명확한 "limit reached" 에러로 거부.
5. **격리**: 자식이 부모 spec의 forbidden 슬롯을 위반하려 함 → 자식 system_prompt가 같은 제약을 받아 LLM이 거부.

## 11. 작업 분해 (의존 순서, 시간 X)

- **S1** proto: SubagentPolicy 추가 + spec.proto 재생성
- **S2** session schema v3: parent_session_id / subagent_depth / subagent_label
- **S3** subagentRegistry (in-memory count + depth check)
- **S4** spec slicing 함수: parent FrozenSpec + SubagentPolicy + override → child FrozenSpec
- **S5** spawn_agent tool — chat default toolset에 추가
- **S6** wait_agent tool
- **S7** agent_status tool
- **S8** 자식 runner의 system_prompt에 "you are a subagent of session=X label=Y" 줄 추가
- **S9** AskCallback의 부모 라우팅 (자식 ask → 부모 ask queue) — V1 optional
- **S10** M6 tree pane 통합 (G2-UI 안에서)
- **S11** 검증 시나리오 1~5 e2e

S5는 S1-S4가 다 끝나야 의미가 있고, S6은 S5 후. S10/S11은 모든 위 작업
후. 각 step은 단위 테스트 + persist 시 SQLite 마이그레이션 idempotent 보장.
