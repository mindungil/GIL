# gil — terminal aesthetic spec

> "며칠짜리 자율 실행을 켜놓고 가끔 들여다보는" 도구. 적합한 비유는 **mission control console** — 비행기 cockpit, NASA 통제실, brewing tank 모니터. 정보가 많지만 정렬되어 있고, 한눈에 "지금 안전한가" 답을 줌.

**Anti-pattern**: generic CLI ("ls -la" 미학), rainbow ANSI, "AI" 그라데이션, 흔한 ASCII 아트, padding-less crammed text.

## 1. Palette

16-color ANSI fallback + truecolor 권장. Bubbletea/lipgloss + 자체 wrapper.

| 역할 | Truecolor | Fallback |
|---|---|---|
| primary | `#fafafa` | white |
| surface (default text) | `#cdcdcd` | (none) |
| meta / dim | `#7a7a7a` | bright black |
| frame (light) | `#3a3a3a` | bright black |
| accent — info | `#5eead4` (mint-cyan) | cyan |
| accent — success | `#86efac` (sage) | green |
| accent — caution | `#fbbf24` (amber) | yellow |
| accent — alert | `#fb7185` (coral) | red |
| accent — emphasis | `#a5b4fc` (lavender) — sparingly for "now active" | bright magenta |
| highlight bg | `#1a1a1a` | (bg invert) |

**규칙**: 한 번에 보이는 화면에 accent 색은 **최대 2개**. 나머지는 surface + dim. Rainbow 금지.

## 2. Typography (terminal = char style)

| 역할 | 스타일 | 예 |
|---|---|---|
| Display header | UPPERCASE + 1글자 letterspacing + bold | `G I L   M I S S I O N` |
| Section header | bold + accent | `**Progress**` (cyan) |
| Body | regular | regular text |
| Data (tabular) | regular monospace | aligned columns |
| Meta | dim italic | `last update 18:34 • 2m ago` |
| Critical | bold + alert | `**STUCK**` (coral) |
| Quote / log line | left ▏ margin + dim | `▏ 18:34 iter 22 verify ✓` |

**No font fallback hacks.** Terminal fonts are user choice. 우리는 Unicode + ANSI만 쓴다.

## 3. Iconography (Unicode only — no emoji)

| 의미 | 글리프 | 비고 |
|---|---|---|
| running | `●` (filled circle) | accent-info |
| idle / pending | `○` (open circle) | dim |
| paused | `◐` (half circle) | caution |
| done / verify ok | `✓` | success |
| failed / verify fail | `✗` | alert |
| stuck / warn | `⚠` | caution |
| trend up | `▲` | success |
| trend down | `▼` | alert |
| arrow / next-step | `›` | accent-info |
| bullet (list) | `»` | dim |
| quote bar | `▏` | dim, single column on left |
| section divider | `━━━━` (heavy h) | full width |
| sub divider | `────` (light h) | full width |

**Progress fill**: `▰` (filled) + `▱` (empty). 부드러운 sub-cell 전환은 `▏▎▍▌▋▊▉█` (eighths).

**Spinner**: Braille `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` (10 frames, 80ms/frame). 일반적인 `|/-\` 보다 distinctive.

## 4. Box drawing

| 용도 | 글리프 |
|---|---|
| Primary frame (TUI 외곽, modal) | `╭ ╮ ╰ ╯ ─ │` (light rounded) |
| Sub-divider 안쪽 | `─` `│` (light) |
| Critical highlight (stuck modal) | `╔ ╗ ╚ ╝ ═ ║` (heavy double) — 아주 드물게 |

**Default = light rounded**. 무거운 ASCII 박스 (`+---+`)는 금지.

## 5. Spatial composition

- **Asymmetric splits**: sessions pane ~25%, main view ~75%. 50/50 절대 금지.
- **Padding inside borders**: 좌우 2 cols, 상하 1 row.
- **Vertical rhythm**: section 사이 2 blank lines, subsection 사이 1.
- **Margins to terminal edge**: 1 col 좌우 (border 자체가 column 0/끝을 점유 — 그 안쪽 padding이 여유).
- **Negative space**: dense하지 않을 때 남겨라. 정보 0인 영역은 빈 공간으로.

## 6. Layout — 4-pane TUI

```
╭─ G I L   ─  v0.1.0-alpha  ─────────────────  mindungil  ●  ssh-host ─╮
│                                                                       │
│  ┌─ Sessions ──────────┐  ┌─ abc123 ─────────────────────────────────┐│
│  │                     │  │  Add dark mode to web frontend           ││
│  │  ●  abc123          │  │                                          ││
│  │     RUNNING  iter23 │  │  Progress  ▰▰▰▰▰▰▰▱▱▱▱▱   23 / 100      ││
│  │                     │  │  Verify    ✓ ✓ ✓ ✗ ─ ─    4 / 6 checks  ││
│  │  ✓  def456          │  │  Tokens    32.1K in / 8.4K out          ││
│  │     DONE     iter45 │  │  Cost      $0.61                        ││
│  │                     │  │  Stuck     ─                            ││
│  │  ⚠  ghi789          │  │  Autonomy  ASK_DESTRUCTIVE              ││
│  │     STUCK    iter12 │  │                                          ││
│  │                     │  └──────────────────────────────────────────┘│
│  │                     │  ┌─ Activity (milestones)─────────────────  │
│  │                     │  │  ▏ 18:34  iter 22  bash "git diff HEAD" │
│  │                     │  │  ▏ 18:35  iter 22  verify  ✓ tests pass │
│  │                     │  │  ▏ 18:36  iter 23  edit src/app.tsx     │
│  │                     │  │  ▏ 18:37  iter 23  ⠋ thinking…          │
│  │                     │  │                                         │
│  │                     │  ┌─ Memory ────────────────────────────────│
│  │                     │  │  progress.md (2m ago):                  │
│  │                     │  │  » dark mode toggle wired               │
│  │                     │  │  » need theme provider refactor         │
│  └─────────────────────┘  └────────────────────────────────────────  │
│                                                                       │
│  q quit  ·  r refresh  ·  / commands  ·  c checkpoints  ·  t toggle  │
╰───────────────────────────────────────────────────────────────────────╯
```

**Asymmetry**: sessions 25%, main 75%. Spec/Progress + Activity + Memory 세 sub-pane이 main 75% 안에서 vertical stack — 쪼개진 정보가 한 column 안에서 위에서 아래로 흐름.

**Header**: minimal — `G I L` letterspaced, version, 사용자, 호스트. 한 줄.

**Footer**: 키맵 한 줄. 점(`·`) 구분, dim.

## 7. CLI surface — 같은 aesthetic in flat layout

### `gil` (no-arg)

```
G I L   v0.1.0-alpha                  mindungil  ●  ssh-host

   3 sessions

   ●  abc123   ▰▰▰▰▰▰▰▱▱▱▱▱   23/100   $0.61   Add dark mode to web frontend
   ✓  def456   ▰▰▰▰▰▰▰▰▰▰▰▰   45       $1.20   Implement OAuth login
   ⚠  ghi789   ▰▰▰▱▱▱▱▱▱▱▱▱   12       $0.32   Fix flaky test
                                                ⚠ STUCK · RepeatedAction (2 of 3)


   ›  gil status              ›  gil watch abc123       ›  gil events ghi789 --tail
   ›  gil interview <new>     ›  gil --help
```

### `gil watch <id>`

```
G I L   ●  abc123   Add dark mode to web frontend

   Progress   ▰▰▰▰▰▰▰▱▱▱▱▱   23 / 100   ⠋
   Verify     ✓ ✓ ✓ ✗ ─ ─   4 / 6
   Cost       $0.61   ▲ +$0.04 / min
   Stuck      ─

   ▏ 18:36  iter 23  edit src/app.tsx
   ▏ 18:35  iter 22  verify  ✓ tests pass
   ▏ 18:34  iter 22  bash "git diff HEAD"

   live · ctrl-c to exit
```

In-place ANSI redraw every 2s. 새 event 추가 시 위에 1 line slide-up.

### `gil status` (visual mode)

```
   ●  abc123   ▰▰▰▰▰▰▰▱▱▱▱▱   23/100   $0.61   Add dark mode to web frontend
               iter 23  ·  ASK_DESTRUCTIVE  ·  started 18:01  ·  2h 36m

   ✓  def456   ▰▰▰▰▰▰▰▰▰▰▰▰   45       $1.20   Implement OAuth login
               iter 45  ·  ASK_DESTRUCTIVE  ·  finished 17:42

   ⚠  ghi789   ▰▰▰▱▱▱▱▱▱▱▱▱   12       $0.32   Fix flaky test
               iter 12  ·  ASK_PER_ACTION  ·  STUCK · RepeatedAction (2 of 3)
```

`--plain` flag으로 옛 텍스트 dump 포맷 유지 (script 친화).

## 8. Motion

- **Progress bar**: 값 변경 시 부드럽게 (sub-cell `▏▎▍▌▋▊▉█` 활용). 16 step/cell × 12 cells = 192 step 해상도.
- **Spinner**: 80ms/frame. 옆에 동사 ("thinking", "compacting", "verifying").
- **Status flip**: 색상 변경 시 1프레임 highlight bg → 1프레임 bold → settle.
- **Event slide**: 새 line 추가 시 위 line이 dim해지면서 새 line은 normal로 들어옴.
- **Stuck warning**: caution 색이 0.5s 주기로 normal ↔ bold (subtle pulse, 3사이클 후 정지).

**금지**: 마우스 hover 효과 흉내, 회전 grad, 무지개, 깜빡이는 빈 칸.

## 9. Degraded gracefully

- **터미널 폭 < 80 cols**: TUI는 sessions pane 숨김 + 메인만. 라인 wrap 금지 (truncate with `…`).
- **컬러 미지원** (`NO_COLOR=1`): glyph는 유지, 색만 빠짐. ✓ ✗ ⚠가 색 없이도 의미 전달.
- **Unicode 미지원** (`LANG=C`): glyph fallback (`●→*`, `▰→#`, `▱→.`, `›→>`, `▏→|`). `gil --ascii` 글로벌 flag.
- **Screen reader**: TUI는 어쩔 수 없음. `gil status`/`watch`는 plain text 자동 (TTY 감지 시).

## 10. Anti-checklist (피하는 것)

| 안티-패턴 | 왜 |
|---|---|
| Rainbow ANSI | 정보 위계 무너짐 |
| 🚀 ☑️ 📊 같은 emoji | 모노스페이스 폭 깨짐 + 가독성 ↓ + AI slop |
| Inter / Roboto / system font 강제 | 터미널은 font 선택 사용자 영역 |
| 5+ accent colors | overwhelming |
| Padding 0 | 정보 밀집 ≠ 가독성 |
| `+---+` ASCII boxes | obsolete, 못생김 |
| `[██████░░░░] 60%` | text 위주 — `▰▰▰▰▰▰▱▱▱▱` (Unicode block)이 더 sharp |
| 회전 무지개 spinner | distracting |
| 유머/재치 메시지 ("oops!", "let me think...") | 며칠짜리 작업의 진지함 깸 |

## 11. Modals

Modals follow the **same rounded light frame** as the TUI outer chrome (`╭ ╮ ╰ ╯ ─ │`). Padding 1×2 (rows × cols). Title prefix `╭─ <Title> ` flush left.

| 종류 | 경계 | 용도 |
|---|---|---|
| Permission ask | light rounded | 6-tier allow/deny choice (Phase 12 contract) |
| Checkpoints | light rounded | timeline list + restore |
| Stuck warn | heavy double `╔═╗ ║ ╚═╝` | only when 3-strategy recovery exhausted |

**Footer hint** inside every modal: dim, `·` separated, e.g. `↑/↓ navigate · enter restore · esc close`.

A selected row inside a modal uses the `›` arrow glyph (accent-info) flush left, not background highlight. Background fills inside modals are forbidden.

## 12. Activity filter taxonomy

The Activity pane defaults to **milestones** — events that mark progress, not noise. Toggling with `t` flips to **all events** (firehose).

| Filter | Event types |
|---|---|
| milestones (default) | `iteration_start`, `verify_run`, `verify_result`, `checkpoint_committed`, `stuck_detected`, `stuck_recovered`, `stuck_unrecovered`, `compact_done`, `run_done`, `run_max_iterations`, `run_error`, `permission_ask`, `permission_denied` |
| all | every emitted event |

Each line is rendered:

```
▏ HH:MM  iter N  <verb> <one-line summary>
```

`verb` is derived from event type — never the raw type. e.g. `tool_call(bash …)` → `bash "<truncated cmd>"`, `verify_result` with all-pass → `verify ✓ N checks`, `stuck_detected` → `stuck <pattern>`.

The bottom-most line shows the spinner (`⠋⠙⠹…`) followed by `thinking…` whenever the most recent event is a `provider_request` without a matching `provider_response`, OR a `tool_call` without a matching `tool_result`.

## 13. Implementation notes (TUI surface plumbing)

The slash-command overlay panel (`/` prompt + last-output box) reuses the rounded light frame. Background highlight on the input prompt is **forbidden** — instead use accent-info on the leading `›` arrow + surface text.

Width-< 80 degraded mode also collapses the slash overlay panel into one line (no border).
