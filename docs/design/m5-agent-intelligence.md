# M5 — agent intelligence: verify-loop discipline as a system primitive

Status: design (2026-05-08)

## 1. Principle

코덱스는 "verify 해라"가 **프롬프트 한 줄**이다. 약속도 강제력도 없다. LLM이
까먹으면 끝이다. 체크된 적이 있는지조차 모델 자기 자랑에 의존한다.

gil은 같은 자리에서 한 발 더 들어간다 — verify를 도구 체계로 강제한다.
코드 변경을 일으킨 turn은 `verify` tool 호출 없이는 자연 종료가 의심받는다.
플랜 단계는 acceptance 명령이 통과할 때까지 완료로 마킹할 수 없다. 디시플린이
프롬프트가 아니라 도구의 형상이다.

추가로, 코덱스의 강력한 두 가지는 그대로 가져온다 —
**TurnDiffTracker**(턴 안에서 변경분을 in-memory 누적, 재read 불필요)와
**apply_patch**(multi-hunk atomic 편집). 이건 컨텍스트 절약 정면 효과.

## 2. 비교 — 코덱스 vs gil M5

| 축 | codex | gil M5 |
|---|---|---|
| 검증 강제 | system prompt 권고 | verify tool + plan_step acceptance |
| 변경분 추적 | TurnDiffTracker (재read 없음) | 동일하게 채택 |
| 편집 도구 | apply_patch (multi-hunk atomic) | apply_patch + 기존 edit_file |
| Self-retry | LLM이 알아서 | plan_step 미통과 시 자연 retry 압력 |
| 플랜 도구 | update_plan (체크리스트만) | plan_steps (acceptance_check 박힘) |

코덱스가 단일 turn 루프를 단단히 만든 반면 gil은 "코드 변경 → verify 통과 → 단계 완료"
사이클을 도구 의존성으로 못박는다. 같은 모델 같은 task에서, gil은 verify pass 없이는
다음 단계로 넘어가지 못한다.

## 3. 도구 추가/변경

### 3.1 plan_steps (신규)

```json
{
  "items": [
    {
      "description": "Add Add() function to calc.go",
      "acceptance_check": "go build ./...",
      "status": "pending"
    },
    ...
  ]
}
```

`todowrite`와 분리. todowrite는 메모/낙서용 (현행 유지), plan_steps는
**검증 명령이 박힌 진짜 플랜**. status는 `pending|in_progress|verified|failed`.
`verified`는 verify tool이 acceptance_check를 실행해 exit 0 받았을 때만 자동 전이.
agent가 직접 status를 verified로 박을 수 없다.

저장: 세션 in-memory (todowrite와 같은 패턴). SQLite 영속화는 후속.

### 3.2 verify (신규)

```json
{
  "description": "build passes",
  "command": "go build ./...",
  "step_id": 2  // optional — plan_step이 있으면 연결
}
```

run_bash와 다른 점:
- 결과가 구조화: `{exit_code, stdout_tail, stderr_tail, duration_ms, parsed_failures}`.
- pytest/jest/go 출력 패턴 매칭해서 실패 테스트 이름 따로 surface.
- step_id가 있으면, exit 0 시 plan_steps[step_id].status를 `verified`로 자동 전이.
- exit !=0 시 plan_steps[step_id].status를 `failed`로 마킹하고 stderr_tail을 step에 첨부.

이게 retry 압력의 출처. agent는 step.status가 verified될 때까지 다음 step으로
못 넘어가도록 system prompt에서 강제.

### 3.3 apply_patch (신규)

코덱스 포맷 그대로 채택:

```
*** Begin Patch
*** Update File: server/internal/service/foo.go
@@ context @@
- old line
+ new line
*** Update File: cli/main.go
@@ ... @@
- ...
+ ...
*** End Patch
```

multi-file, multi-hunk, atomic. 한 hunk 실패 시 전체 롤백 + 부분 성공 리포트
(어디까지 적용됐는지). 파서는 codex의 lark 스펙을 Go로 재구현 (외부 의존성 X).

기존 `edit_file`은 유지 — 단일 hunk 케이스에서 더 명료하니까.
LLM이 둘을 자연스럽게 골라쓰도록 description에 가이드.

### 3.4 TurnDiffTracker (server-side state, tool 아님)

`session.WorkingDir` 안에서 발생한 write_file/edit_file/apply_patch/run_bash의
파일 변경을 turn 동안 in-memory에 unified diff로 누적. show_diff가 이 누적분을
즉시 반환 → re-read 없음.

핵심 효과:
- LLM이 "방금 뭐 바꿨더라" 확인하려고 read_file 안 부름.
- 컨텍스트 토큰 절약.
- show_diff가 이미 있으니 도구는 그대로, 내부 구현만 교체.

run_bash가 일으킨 변경은 추적 못 함 (외부 명령). 이 경우 Tracker는 "외부 명령이
실행됨, fs 상태 알 수 없음" 플래그를 기록 → show_diff는 fallback으로 git diff 사용.

### 3.5 system prompt 업데이트

default agent의 system prompt에 추가:

> 코드를 변경한 턴은 반드시 verify tool로 검증을 마쳐야 한다. plan_steps가
> 있다면 verified 단계로 전이되기 전에 다음 단계로 넘어가지 마라. 사용자에게
> "완료"를 말하기 전에 마지막 verify 결과가 통과인지 확인해라.

이건 강제력의 문서적 표현. 진짜 강제는 plan_steps의 status 전이 의존성에서 옴.

## 4. 호환성 / 마이그레이션

- 기존 `edit_file`, `write_file`, `run_bash`, `todowrite` 변경 없음.
- 기존 `show_diff`는 시그니처 동일, 구현만 TurnDiffTracker 기반으로 교체.
- 새로 등록되는 도구: `plan_steps`, `verify`, `apply_patch` (3개).
- agent whitelist:
  - `default`: 모두 사용.
  - `explore` (read-only): plan_steps/verify/apply_patch 차단 유지.
  - `plan`: plan_steps 추가, verify는 차단 (plan agent는 검증 안 함).

## 5. 비범위

- sub-agent spawn (codex의 spawn_agent/wait_agent 등 6개) — 후속 마일스톤. 우리
  agent abstraction이 이미 있으니 lifecycle 플러밍만 추가하면 됨.
- tool_search, request_permissions, web_search — 유용하지만 "코덱스 이기기"
  핵심 디시플린에 직접 기여 안 함. 후속.
- TUI 중앙 시각화 (사용자가 요청한 것) — M6 별도 design 문서.

## 6. 검증 (이게 진짜 코덱스 이기는지)

이 마일스톤이 끝나면 동일 모델/동일 task로 head-to-head 다시 실행:

벤치 케이스:
1. **buggy fix** — 의도적으로 깨진 함수 수정. 이전엔 LLM이 "고쳤습니다" 후 실제
   build/test 안 돌리고 종료하는 케이스를 잡고 싶음. M5 후엔 verify 통과 없이
   완료 못함.
2. **multi-file refactor** — apply_patch 한 번으로 3개 파일 동시 수정. 코덱스도
   되고 우리도 됨. 둘 다 atomic 보장하는지 측정.
3. **regression injection** — 멀쩡한 함수 lint 에러로 변경. plan_step의
   acceptance_check가 lint이면 status가 failed로 남고 retry 발생하는지.

성공 기준: 위 세 케이스에서 verify 통과를 거치지 않은 "완료" 응답이 한 번도
나오지 않을 것. 코덱스는 1번에서 자주 실패함 (관찰 결과).
