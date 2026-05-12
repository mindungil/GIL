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

## 10. Surface-architecture 보강 (G2-UI 구현 중 발견)

§3의 "현재 (M5)" 4-pane 레이아웃은 *giltui binary*의 `view.go` Model이다.
그러나 SessionService.Prompt 스트림은 *chatModel(`chat_view.go`) — bare
`gil` TTY*에만 도착한다. giltui Model은 RunService.Tail만 구독한다.

즉 M6.3-M6.6의 "agent tree pane" 렌더는 giltui에 다음 중 하나가 선행되어야
의미가 있다:

- **A**: giltui Model이 SessionService.Prompt 구독을 추가 (활성 session의
  chat 활동을 받음). 별도 아키텍처 작업.
- **B**: M6를 chat surface (chat_view.go)로 재해석. chat surface는 single-
  column이라 multi-pane 레이아웃 자체가 redesign 필요.
- **C**: 양쪽 surface 모두에 agent tree pane. giltui는 옵션 A, chat은 옵션
  B의 축소판.

V1 결정 보류. 이 결정이 안 정해진 상태에서 view code만 깔면 dead-wiring
(`feedback_check_production_wiring.md`)이 된다. **G2-UI V1은 의도적으로
좁혔다**: M6.1+M6.2 데이터 층 + chat surface status strip에 agent tree
요약 inline 표시 (`chatAgentActivity` in `chat_view.go`). 이건 데이터가
있는 곳(chatModel)에 visible deliverable을 둔다. 4-pane → 3-pane 교체
자체는 surface 결정 후 별도 작업.

이 결정이 정해지면 M6.3-M6.6를 그때 진입하면 된다 — 이미 짠 M6.1+M6.2
데이터 층과 render 함수 stub들은 그대로 재사용.

### Option별 implementation plan (S10)

세 옵션을 코드 수준에서 구체화. 사용자가 A/B/C 중 어느 하나 고르면 그
trajectory의 step list로 바로 진입.

**Option A — giltui가 chat agent 활동도 본다 (least invasive)**

A의 본질은 giltui Model에 *두 번째 stream subscription*을 더해 활성
세션의 chat agent 활동도 받아오는 것. 4-pane 레이아웃의 Activity pane
대상이 RunService.Tail에서 *RunService.Tail + SessionService.Prompt 합본*
이 된다.

  - **A.1** `tui/internal/app/model.go`: Model에 `chatPromptStream
    gilv1.SessionService_PromptClient` + `chatTree *AgentTree` 추가.
    chat agent가 active일 때만 nil 아님.
  - **A.2** 새 메시지 타입 — `giltuiPromptToolCallMsg` /
    `giltuiPromptToolResultMsg` / `giltuiPromptDoneMsg` — chat_stream.go의
    chatModel 메시지 미러. 별도 식별자 (`giltui` prefix) 로 chatModel과
    name conflict 없이 공존.
  - **A.3** 활성 session이 chat-only인지 판정. 휴리스틱: session.Status
    가 "running"이 아니면 chat-only로 간주, Prompt subscription 시도.
    Status가 "running"이면 기존 Tail subscription 사용.
  - **A.4** view.go renderMainColumn 갱신: chatTree 데이터가 있으면
    Activity pane을 chatAgentActivity (chat_view.go의 helper) + tree
    render로 교체. 없으면 기존 activity (run-mode Tail) 유지.
  - **A.5** 키바인딩: `t` 로 tree pane focus toggle (현재 progress/memory
    그대로).
  - **A.6** 스냅샷 테스트: chat-active vs run-active vs idle 세 케이스.

  Estimated touch: 3-5 files, ~400-600 LOC. 가장 적은 surface 재설계,
  가장 빠른 user value.

**Option B — chat surface를 multi-pane으로 재설계**

chat_view.go의 single-column transcript-prompt 레이아웃을 갈아엎고
sessions left + agent tree center + prompt below 구조로 재구성.

  - **B.1** `chat_view.go` 분리: 현재 chatView() 함수 안의 단일 column
    빌더를 lipgloss.JoinHorizontal로 좌우 분할. left rail = sessions
    list (현재 renderPreFirstTurn 로직 이주). right = transcript +
    prompt.
  - **B.2** right column 내부에 중앙 pane 추가: agent tree (M6 design
    §4 layout). transcript는 prompt 위로 압축 (오른쪽 절반의 절반).
  - **B.3** new render functions: renderAgentTreePane,
    renderTurnDiffPane (turn-scoped show_diff 데이터). 기존 plan pane은
    plan_steps 파싱하도록 확장 (현재 plan.json 파일 기반).
  - **B.4** Model에 turnDiff 필드 추가 — show_diff tool result에서
    파싱. plan_steps도 마찬가지로 tool result에서 파싱 (혹은 새 RPC
    GetPlanSteps 추가).
  - **B.5** 모바일 폭 fallback: width < 100 column에서는 multi-pane
    drop하고 single-column으로 폴백 (terminal-aesthetic.md §3 rhythm
    유지).
  - **B.6** 스냅샷 테스트 광범위 갱신: 현존 8-9개 chat snapshot은 모두
    레이아웃 변경으로 깨짐. 새 baseline 생성.

  Estimated touch: 6-10 files, ~1000-1500 LOC. 가장 큰 redesign, chat
  surface의 시각 캐릭터 변경. Visual review 사이클 필요.

**Option C — A + B 모두**

C는 A와 B를 leaf level에서 일치시키기: giltui도, chat surface도, 같은
3-pane을 보여준다. 차이는 입력 surface (chat: prompt panel, giltui:
read-only) 뿐.

  - **C.1** A.1-A.6 그대로.
  - **C.2** B.1-B.6 그대로 (chat surface 재설계).
  - **C.3** render functions를 양쪽에서 공유: renderAgentTreePane,
    renderTurnDiffPane을 `tui/internal/app/agent_tree_render.go` (신규)
    에 통합. Model + chatModel 모두 호출.
  - **C.4** 일관성 테스트: chat surface와 giltui가 같은 데이터 위에서
    같은 출력 produce 하는지 차이만 측정.

  Estimated touch: A + B의 합. 가장 일관된 user experience, 가장 무거운
  구현.

### 권장

A부터 시작. 가장 작고 검증 가능. C는 A가 잘 돌아간 다음 B를 별도로 추가.
B만 단독 진입은 권하지 않음 — chat surface 캐릭터를 바꾸는데 giltui가 그대로
면 일관성이 깨짐.

### 진행 상태 (2026-05-12)

**Option A V1 landed**. 구현 분기:

- **A.1** giltui Model에 `chatTree *AgentTree` 필드 추가 (`model.go`).
  세션 전환 시 reset, lazy allocate via `chatTreeOrNew()`.
- **A.2** Separate Prompt subscription 대신 더 가벼운 경로 채택:
  `SessionService.Prompt`가 `tool_call` / `tool_result` 이벤트를
  per-session event stream (`RunService.ensureSessionStream`)으로
  bridge. 기존 `RunService.Tail` 한 굴뚝으로 chat-mode + run-mode
  활동이 통합. giltui Model은 별도 RPC 없이 기존 Tail이 이 이벤트들을
  받음.
- **A.3** `Tail`이 chat-only 세션도 따라가도록 RUNNING 게이트 완화 +
  Tail이 stream을 lazy-allocate (`ensureSessionStream`). NotFound은
  세션 자체가 없을 때만 반환.
- **A.4** `view.go` `renderMainColumn`이 `chatTree`에 노드가 있으면
  Activity pane을 Agent Tree로 교체 (있는 그대로 render되는 single-pane).
  비어 있으면 기존 activity 행 유지.
- **A.5** 키바인딩 추가 보류 — V1은 read-only display로 충분.
- **A.6** unit 테스트 9개 (`chat_tree_bridge_test.go`): nil-guard,
  iteration_start, tool_call → 노드, tool_result → 상태 전이,
  malformed JSON, render maxRows / empty.

후속 (Option B 또는 C): chat surface 측 multi-pane. Surface 결정이
사용자 결정사항이라 별도 분기.

Estimated ordering not committed — 위 옵션은 surface 결정 후 사용자가 phase
번호 부여.
