# Phase 14 — Monitoring UX (TUI/CLI for "며칠 자율 실행" 가시성)

> Phase 1-13에서 자율 엔진 + harness UX foundation + distribution까지 닫음. 그러나 TUI/CLI는 여전히 **event-stream-centric**이고 **progress-centric**이 아님 — 사용자가 며칠 짜리 작업을 "켜놓고 떠나서 가끔 들여다보는" 시나리오에 최적화되어 있지 않음. 이 phase는 그걸 고친다.

**Goal**: 사용자가 5초 안에 "지금 어디까지 왔나, 비용은 얼마이고, 막혔나?" 답을 얻을 수 있는 TUI + CLI.

---

## A. 현재 UX의 4가지 한계

| # | 한계 | 영향 |
|---|---|---|
| 1 | TUI는 raw event firehose — verify-pass / checkpoint / stuck 같은 milestone가 묻힘 | 며칠짜리 작업에서 "지금 뭐 하고 있나" 파악이 어려움 |
| 2 | spec (= 계약)이 어디에도 안 보임 | 사용자가 자기가 시킨 게 뭔지 잊음, 결과만 보면 평가 불가 |
| 3 | autonomy/cost/iter budget이 status pane 한 줄로만 — 시각화 없음 | "남은 budget" 직감 0 |
| 4 | `gil status` / `gil events`는 텍스트 dump | terminal로 "흘러가는" 정보를 글랜스로 못 잡음 |

## B. 7 Tracks

### Track A — TUI 4-pane 재구성

**Files**: `tui/internal/app/{view,model,update,layout}.go` 재작성

기존 3-pane (Sessions / Detail / Status):

```
┌Sessions─────┬Detail──────────────────────┐
│ * abc123    │ status: RUNNING            │
│   def456    │ iter: 23 / 100             │
│             │ tokens: 12k in / 3k out    │
│             │                            │
│             │ events                     │
│             │  18:34 tool_call bash…     │
│             │  18:34 tool_result …       │
│             │  18:35 verify_check …      │
│             │  …                         │
└─────────────┴────────────────────────────┘
[status bar]  q quit · r refresh · / help
```

신규 4-pane:

```
┌Sessions─────┬Spec & Progress─────────────────────────┐
│ * abc123    │ goal: "Add dark mode to web frontend"  │
│   def456    │                                        │
│   ghi789    │ Progress  ▓▓▓▓▓▓▓░░░░░  iter 23 / 100  │
│             │ Verify    ✓ ✓ ✓ ✗ - -    4/6 checks    │
│             │ Tokens    32.1K in / 8.4K out          │
│             │ Cost      $0.61 (estimate)             │
│             │ Stuck     none                         │
│             │ Autonomy  ASK_DESTRUCTIVE_ONLY         │
│             │                                        │
│             ├Activity (filtered)─────────────────────┤
│             │ 18:34  iter 22  bash "git diff HEAD"   │
│             │ 18:35  iter 22  verify  ✓ tests pass   │
│             │ 18:36  iter 23  edit src/app.tsx       │
│             ├Memory bank (excerpt)───────────────────┤
│             │ progress.md (last update 18:34):       │
│             │   • dark mode toggle wired             │
│             │   • need to refactor theme provider    │
└─────────────┴────────────────────────────────────────┘
[status bar]  q quit · r refresh · / help · t toggle-tail · c checkpoints
```

핵심 추가 위젯:
- **Progress bar** (iter / max_iter)
- **Verify check matrix** (✓/✗/- per check)
- **Cost meter** (live USD estimate)
- **Stuck indicator** (pattern + recovery in flight)
- **Memory bank excerpt** (active progress.md)

Activity는 filtered (default: iteration boundaries + verify + checkpoint + stuck only — tool_step 같은 noise는 `t` 토글로 표시).

라이브러리: 기존 lipgloss는 그대로. progress bar는 stdlib + lipgloss로 작성.

Commit: `feat(tui): 4-pane progress-centric layout (spec/progress/activity/memory)`

### Track B — Stuck/Recovery 시각화

**Files**: `tui/internal/app/stuck.go`, modify `view.go`

stuck detector가 패턴 발견 시 이벤트 stream에 `stuck_detected{pattern}` emit. recovery strategy 결정 시 `stuck_recovery_started{strategy}`. Recovery 종료 시 `stuck_recovery_done{strategy, success}`.

TUI는 이걸 받아서 progress pane에 (raw event 아닌) 한 줄 highlight:
```
Stuck     ⚠ RepeatedAction → recovery: AltToolOrder (in flight)
```
Recovery 성공 시 1초 fade out, 실패 시 다음 strategy로 넘어가는 게 보임.

3회 미회복 → red `Stuck     ✗ exhausted (3 strategies)` + 세션 status가 "stuck"으로 전환됨이 즉시 반영.

Commit: `feat(tui): live stuck detection + recovery strategy panel`

### Track C — Checkpoint 타임라인

**Files**: `tui/internal/app/checkpoint.go`, modify `keys.go`

`c` 키 → modal 띄움:
```
Checkpoints (Shadow Git history)

  step  when         tool/iter         summary
  ────  ───────────  ────────────────  ──────────────────────────
   1    18:01:23     iter 1 init       baseline
   2    18:04:11     iter 3 edit       wired theme provider
   3    18:09:55     iter 7 verify ✓   first 2 checks passing
   4    18:14:02     iter 11 edit      added dark/light toggle
   5    18:22:48     iter 18 verify ✓  4/6 checks passing
 → 6    18:31:17     iter 23 edit      latest

  ↑/↓ navigate · enter restore · esc close
```

`enter`로 restore 트리거 (RunService.Restore RPC). running 세션은 restore 거부 (FailedPrecondition) — modal에 표시.

Commit: `feat(tui): checkpoint timeline modal + restore navigation`

### Track D — `gil watch <id>` (TUI 없는 라이브)

**Files**: `cli/internal/cmd/watch.go`

TUI 띄울 환경이 안 되는 곳 (CI, bare ssh)에서 사용:

```
$ gil watch abc123
Session abc123 — Add dark mode to web frontend
Progress: ▓▓▓▓▓▓▓░░░░░  iter 23 / 100  · 4/6 verify  · $0.61  · ASK_DESTRUCTIVE
Last:     18:36 iter 23 edit src/app.tsx
          18:35 iter 22 verify ✓ tests pass
          18:34 iter 22 bash "git diff HEAD"
[updates every 2s. Ctrl-C to exit]
```

ANSI 한 줄 in-place 갱신 (carriage return + 화면 클리어). `--once` 플래그로 한 번만 출력 후 exit (스크립트 친화적).

Commit: `feat(cli): gil watch <id> — live single-pane progress monitor`

### Track E — `gil events --filter`

**Files**: modify `cli/internal/cmd/events.go`

```
gil events <id> --tail --filter iteration,verify,stuck,checkpoint
```

기본은 `--filter ALL` (현재 동작). 추가 표준 set: `milestones` (= iteration/verify/stuck/checkpoint), `errors` (= 모든 *_error/failed/stuck), `tools` (모든 tool_*).

Repeated --filter는 합집합. NDJSON 모드는 그대로 동작.

Commit: `feat(cli): gil events --filter (filter event stream by kind)`

### Track F — `gil status` 시각 모드

**Files**: modify `cli/internal/cmd/status.go`

기존 (line per session):
```
ID       STATUS    ITER  TOKENS    GOAL
abc123   RUNNING   23    32k/8k    Add dark mode to web frontend
def456   DONE      45    52k/15k   Implement OAuth login
```

신규 (visual for RUNNING):
```
abc123   ▓▓▓▓▓▓▓░░░░░  iter 23 / 100  · 4/6 verify  · $0.61  · RUNNING
         "Add dark mode to web frontend"
def456   ▓▓▓▓▓▓▓▓▓▓▓▓  iter 45        · 6/6 verify  · $1.20  · DONE
         "Implement OAuth login"
```

`--quiet`/`--plain` 으로 옛 포맷 유지 (스크립트 친화).

Commit: `feat(cli): gil status visual progress bars + verify matrix for RUNNING`

### Track G — `gil` no-arg quick summary

**Files**: modify `cli/internal/cmd/root.go`

현재 `gil` (인자 없음) → cobra default = help 텍스트 dump.

신규: 한눈 요약:
```
$ gil
gil v0.1.0-alpha

3 sessions:
  ▓▓▓▓▓▓▓░░░░░  abc123  RUNNING  iter 23/100  $0.61   "Add dark mode..."
  ▓▓▓▓▓▓▓▓▓▓▓▓  def456  DONE     iter 45      $1.20   "Implement OAuth..."
                ghi789  STUCK    iter 12      $0.32   "Fix flaky test..."

Next: gil status (verbose) · gil watch abc123 · gil events ghi789 --tail
       gil interview (new task) · gil --help (commands)
```

세션 0개면 이전 onboarding 메시지 그대로.

Commit: `feat(cli): gil (no args) prints quick session summary + next-step hints`

---

## Phase 14 완료 체크리스트

- [ ] TUI 4-pane (spec/progress/activity/memory) 작동
- [ ] stuck/recovery 시각화
- [ ] checkpoint 타임라인 + restore navigation
- [ ] `gil watch <id>` live monitor
- [ ] `gil events --filter`
- [ ] `gil status` visual mode + `--plain` 옵션
- [ ] `gil` no-arg quick summary
- [ ] e2e14 — TUI snapshot + watch sanity (TUI는 PTY 없이 어렵다면 unit test으로 대체)

## 추가 ground rules

- **observation only** — 모든 신규 위젯은 observation. agent 결정 변경 불가 (slash commands ground rule 그대로 확장)
- **degraded gracefully** — 좁은 터미널 (40 cols)에서도 깨지지 않게: 위젯 자동 hide
- **screen reader 호환** — TUI는 lipgloss border 있어 어쩔 수 없지만 `gil watch` / `gil status`는 plain text 옵션 제공
