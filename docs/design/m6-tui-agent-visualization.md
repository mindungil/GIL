# M6 — TUI 중앙 시각화: agent의 행동을 코어 pane으로

Status: design (2026-05-08)

## 1. Principle

지금 TUI는 4-pane 미션 콘트롤이지만 chat 모드에서 agent가 *지금* 뭐 하고 있는지는
체감이 안 된다. tool call이 일어나도 활동 로그(Activity pane)에 한 줄 흘러갈 뿐이고,
사용자는 "이게 끝난 건가? 아직 도는 건가? 실패했나?"를 텍스트 응답이 다 도착할
때까지 모른다.

코덱스도 똑같다. 둘 다 채팅 스크롤백이고 agent 내부 상태는 보이지 않는다.
gil이 이기는 자리는 여기다 — agent의 행동 자체를 1급 시각 객체로 만든다.
M5에서 agent 디시플린을 시스템 레벨로 끌어올렸으니 (plan_steps + verify),
화면도 그 디시플린을 보여주는 형태로 맞춘다.

## 2. 무엇이 시각화 대상인가

- **현재 plan_steps의 진행** — 각 step의 status (pending / verified / failed),
  acceptance_check 명령, last_failure tail.
- **이번 turn의 tool call 트리** — 호출 순서, 도구 이름, args 요약,
  duration, status (▶ 진행 / ✓ 완료 / ✗ 실패).
- **show_diff의 turn-scoped 결과** — TurnDiffTracker가 누적한 파일 변경
  스냅샷.

이 셋을 묶으면 "agent가 지금 뭐 하고 있고, 어디까지 했고, 무엇을 검증했는가"가
한 화면에 나온다. 코덱스에는 이게 없다.

## 3. Layout 변경

### 현재 (M5)
```
┌ sessions ┬ progress           ┐
│          │ activity            │
│          │ plan ─or─ memory    │
└──────────┴─────────────────────┘
```

### M6
```
┌ sessions ┬ Agent Tree ─────────┐
│          │ ▾ turn #3 (running) │
│          │   ✓ plan_steps  3.2s │
│          │   ✓ edit_file   1.1s │
│          │   ▶ verify          │
│          │     step_id=1       │
│          │ ✓ turn #2  ▶ to expand
│          ├─────────────────────┤
│          │ Plan (plan_steps)   │
│          │ [✓] 1. fix Add       │
│          │     check: go build  │
│          │ [▶] 2. add test     │
│          │     check: go test   │
│          ├─────────────────────┤
│          │ Diff (turn-scoped)   │
│          │ +12 / -5 in 2 files  │
└──────────┴─────────────────────┘
```

기존 progress + activity + memory를 **agent tree + plan + diff** 3-pane으로 교체.
sessions pane 좌측은 유지. 헤더/푸터 그대로.

원래 progress 정보(iter, tokens, cost)는 헤더로 합쳐서 한 줄. memory pane은
드물게 쓰는 거니 토글로(`m` 키) 띄울 수 있게 하고 기본은 숨김.

## 4. 데이터 소스

세 pane이 binding하는 소스:

| Pane | 소스 | 갱신 트리거 |
|---|---|---|
| Agent Tree | SessionService.Prompt stream의 ToolCallPart/ToolResultPart | 매 Part 도착 |
| Plan | plan_steps tool result + verify tool result (둘 다 plan을 echo) | tool result 시 파싱 |
| Diff | show_diff tool result (renderTrackerSummary 출력) | tool result 시 파싱 |

새 RPC 안 만든다. 기존 `Prompt` 스트림에서 흘러오는 Part를 client측에서 모델로
누적. 데몬에는 변경 없음.

agent tree의 status:
- ToolCallPart 도착 → 노드 추가, status=running, t0 기록
- ToolResultPart 도착 → 매칭 노드의 status를 result.IsError ? failed : ok 로
  전이, duration 계산

turn 경계: PromptResponse가 DonePart 보내면 트리 root 노드 close. 다음 turn
시작 시 새 root 노드. 이전 turn 트리는 collapsed 상태로 위에 남김.

## 5. Tree pane interaction

- ↑/↓ 으로 노드 네비게이션
- Enter — 노드 펼치기/접기 (turn root만 접을 수 있음, leaf는 highlight만)
- 선택된 leaf는 우측 detail pane에 정보 표시... 하고 싶지만 우리 layout은
  세로 stack이라 별도 detail pane 없음. 대신 footer에 "선택된 노드: name args
  duration" 한 줄 표시.

V1 키바인딩:
- 기본은 read-only 시각화. 클릭/선택은 추가 정보용.
- `c` checkpoint 모달은 그대로
- `m` 토글로 memory pane 띄우기 (기존 자리 차지)

## 6. 마이그레이션 / 호환성

- 기존 progress pane의 정보(iter, tokens, cost, autonomy)는 헤더 우측에
  inline. progress 함수는 보존, view에서만 호출 위치 변경.
- 기존 activity pane은 삭제 — agent tree가 대체.
- 기존 plan pane (run mode plan)은 그대로. 단 source는 chat plan_steps와
  통합해서 한 pane에서 둘 다 표시. run plan vs plan_steps를 plan pane이
  둘 다 받음.
- run mode session의 경우 agent tree pane은 "run mode — see Activity history"
  메시지 + 이벤트 sparkline.

## 7. 비범위

- 마우스 인터랙션 X (bubbletea가 지원하지만 V1은 키만)
- 그래픽 차트/스파크라인 — V2
- diff side-by-side 풀스크린 모드 — V2
- agent tree에서 노드 우클릭 → 도구 결과 풀텍스트 모달 — V2

## 8. 검증 — 코덱스보다 보기 좋은가

성공 기준:
1. M5.4 bench 시나리오를 TUI에서 실행하면, 7 tool call을 트리로 즉시 보여준다.
2. 각 verify 호출이 끝났을 때 plan pane의 [ ]가 [✓]로 시각적 전이가 보인다.
3. apply_patch 같은 multi-hunk 편집은 트리에서 한 노드 + 자식들로 분해 (현재는
   tool call 1개로 나오지만 result body에서 수정 파일 추출해 children으로 표시).
4. 코덱스 TUI 스크린샷과 나란히 두면 "agent 내부가 보인다 vs 안 보인다"가 즉시
   구별된다.

스크린샷은 마일스톤 commit의 PR description에 첨부.

## 9. 작업 분해

- M6.1 — agent tree 모델 + render (sessions 옆 영역에 임시 hard-coded data로
  층 분리)
- M6.2 — Prompt stream 이벤트 → tree 갱신 wiring
- M6.3 — plan pane을 plan_steps tool result 파싱하도록 확장
- M6.4 — diff pane (turn-scoped show_diff)
- M6.5 — progress 헤더 통합, activity 제거, memory 토글
- M6.6 — keybinding 정리 (`m`/`d`/`p` 토글)

각 단계마다 단위/스냅샷 테스트.
