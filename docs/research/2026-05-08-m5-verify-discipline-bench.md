# M5 verify-loop discipline — end-to-end bench (2026-05-08)

snapshot. M5.4. provider=oslab vllm, model=qwen3.6-27b. 비교는
gil M4 → gil M5 (동일 모델, 동일 시나리오).

## Setup

```
/tmp/m5_bench/
  main.go        # buggy function
  main_test.go   # failing test
  go.mod
```

세션은 `working_dir=/tmp/m5_bench`에 핀, `default` agent 사용.

## Case 1: 단일 함수 버그 (Add)

`func Add(a, b int) int { return a - b }` ← 의도된 버그.
프롬프트: "Add 함수가 버그라서 테스트가 실패. 고쳐줘. plan_steps로 단계를 만들고 verify로 각 단계를 검증해"

**결과 (gil M5):**

```
[tool.call #1] read_file        main.go
[tool.call #2] glob             *_test.go
[tool.call #3] read_file        main_test.go
[tool.call #4] plan_steps       2단계: 수정 + 테스트, 각각 acceptance_check 박힘
[tool.call #5] edit_file        return a - b → return a + b
[tool.call #6] verify           go build ./... → exit 0, step 1 → verified
[tool.call #7] verify           go test -v ./... → exit 0, step 2 → verified
```

플랜:
```
[✓] 1. Add 함수의 버그 수정: a - b → a + b
    check: go build ./...
[✓] 2. 테스트 통과 확인
    check: go test -v ./...
```

7 tool calls, 1 turn, 16s, 28k tokens in / 650 out, 두 step 모두 `[✓]` 후 종료.

## Case 2: 회귀 주입 (Multiply)

`func Multiply(a, b int) int { return a + b }` ← 의도된 버그.
프롬프트에 "처음 verify에서 fail 나오면 다시 시도해" 명시.

**결과:**

agent는 read → plan_steps(1단계) → edit_file(`a + b` → `a * b`) → verify(go test) →
PASS, step 1 → verified로 종료. retry 트리거 없이 한 번에 통과 (LLM이 첫
시도에서 정확한 수정).

## Codex 대비

코덱스(M4 시점 비교, 동일 모델 가정 — 직접 실행 불가, 디자인 비교):

| 측면 | codex | gil M5 |
|---|---|---|
| 검증 강제 | system prompt 권고만 | plan_step.status는 verify pass에만 verified로 전이 |
| 변경분 추적 | TurnDiffTracker (재read 없음) | 동일 채택 (M5.1) |
| 다중 hunk 편집 | apply_patch 다중 hunk atomic | 동일 채택 (M5.2) |
| Plan 도구 | update_plan (체크리스트) | plan_steps (acceptance_check 박힘) |
| Self-retry | LLM이 알아서 | step status 전이가 자연 retry 압력 |

가장 중요한 차이는 **검증 강제**다. 코덱스는 "verify 해라"가 prompt 한 줄이라
LLM이 까먹으면 끝. gil은 plan_step.status=verified가 verify tool 의 acceptance_check
명령 exit 0에서만 전이되도록 시스템 레벨에서 잠가 둔다. agent가 plan_steps를
재호출해 status="verified"를 박으려 해도 schema가 status 필드를 받지 않아
무시된다 (TestPlanStepsThenAgentCannotMarkVerified 단위 테스트로 핀).

## 한계 — 정직하게

1. **첫 시도 retry 케이스 직접 검증 안 됨** — qwen이 Case 2에서 한 번에
   맞는 수정을 해 retry 압력이 발현되지 않았다. retry 압력의 시스템 레벨 동작은
   TestToolVerify_FailTransitionsStep에서만 검증됨.
2. **codex 직접 실행 비교 부재** — OpenAI API key 없음. 코덱스 비교는 source
   분석에 기반 (research/codex 클론 분석).
3. **TurnDiffTracker run_bash 추적 부재** — 외부 명령으로 인한 fs 변경은
   tracker가 못 잡음. show_diff에 caveat 한 줄 붙는 것으로 표시.
4. **persistence 부재** — plan_steps와 verify 결과 모두 in-memory. 데몬 재시작
   시 사라짐. SQLite 영속화는 후속.

## 다음

- M6: TUI 중앙 시각화 (agent tree pane)
- M7: verb-mode subcommands → headless-only
- 후속: sub-agent spawn (codex의 spawn_agent/wait_agent), tool_search,
  request_permissions
