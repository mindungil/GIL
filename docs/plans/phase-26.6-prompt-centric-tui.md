# Phase 26.6 — Prompt-Centric TUI Chat Surface

Status: design (2026-05-02)

## 1. Why

design.md §2.6 ("자연어 단일 surface, 내부 에이전트 라우팅") was added 2026-05-02
after P26 shipped a slash-table-driven, line-based stdout REPL that violated the
principle in three structural ways (#42–#44 in `phase-26.5-followups.md`):

- Bare `gil` branches on TTY (chat REPL) vs non-TTY (one-shot summary) — not a
  single surface.
- Slash table is the canonical input surface — natural language is treated as a
  pass-through to the LLM, not as the routing primitive.
- Visual hierarchy gives the user no signal that prompting is what gil is for.
  The idle status strip prior to step (a) literally instructed users to memorize
  `/sessions`.

Step (a) (chat entry self-disclosure, completed) closed the discoverability
gap on the line-based surface. Step (b)/(c) (intent router) addresses the
routing gap. **This phase (the design doc you're reading) addresses the
visual-hierarchy gap**: rebuild the human-TTY chat surface so prompting is the
visible center of mass — a persistent panel TUI where the prompt input is the
brightest, most-bordered, most-spaced element on screen, and everything else
(sessions, status, conversation history) recedes into supporting chrome.

## 2. Goal

When a user runs `gil` on a TTY, they should:

1. Without typing or reading anything, immediately see WHERE to type. The
   prompt panel is visually unmistakable as the input affordance.
2. Without learning slash commands, see what's available — past sessions
   self-disclose in the upper region, with prose-led context.
3. Stay inside one persistent layout for the whole session: prompt always
   pinned bottom-center, conversation streams above it, status pinned just
   above the prompt panel. No "modes" the user has to switch between.

The non-TTY path (pipes, scripts) keeps the existing summary one-shot output;
this phase does not unify that surface (#42 is a separate followup that depends
on this work landing first).

## 3. Layout

Reference resolution: 100 cols × 32 rows. Layout degrades gracefully on
narrower / shorter terminals (see §7).

```
   ╭──────────────────────────────────────────────────────────────────────────────────────────╮
   │   ▏  G  I  L                      /home/ubuntu/proj             claude-opus-4-7  · 1M    │
   ╰──────────────────────────────────────────────────────────────────────────────────────────╯


      3 past sessions

         ●  add-dark-mode-toggle         4m    interview      7/11 slots · 64% sat
         ○  refactor-auth-mw             2h    done           ✓ 5/5 checks · $0.42
         ○  fix-oauth-redirect           1d    interview      paused

         ›  describe a new task or resume one above by name



      [conversation scrolls here as you type and the agent responds]




   ─────────────────────────────────────────────────────────────────────────────────────────────
                                                                            idle · agent ready

   ╔══════════════════════════════════════════════════════════════════════════════════════════╗
   ║                                                                                          ║
   ║    ›   ▎                                                                                 ║
   ║                                                                                          ║
   ╚══════════════════════════════════════════════════════════════════════════════════════════╝
      describe a task, resume by slug, or ask what's running         v0.1 · ↑↓ history · / cmds
```

### 3.1 Regions (top to bottom)

| Region          | Rows | Content                                                    |
|-----------------|------|------------------------------------------------------------|
| Header          | 3    | Rounded box: `▏ G I L`, working dir, model, context window |
| Conversation    | flex | Pre-first-turn: session list + invite. Post: streaming msgs|
| Status strip    | 2    | Single divider rule + one-line phase/cost/state            |
| Prompt panel    | 5    | Heavy `╔═╗` magenta box, blank-cursor-blank, wrap to width |
| Affordance line | 1    | One row: left-aligned NL subtitle (per §4.3) + right-aligned footer hints (`v0.1 · ↑↓ history · / cmds`) |

### 3.2 The two transitions

- **Entry → first turn**: The session list at top of the conversation area
  scrolls up as the user's first prompt + agent response push it. After the
  first turn, the conversation area is purely turn-by-turn dialogue. The
  session list does NOT redraw on top of conversation history.
- **Run phase → stream**: While the agent is running an interview/run turn,
  the prompt panel border switches from magenta (active input) to dim (input
  disabled). The cursor `▎` is replaced with a braille spinner from the
  existing `uistyle.SpinFrames` pool. The status strip carries iter/cost.

## 4. Visual vocabulary

Reuses the existing `cli/internal/cmd/uistyle` palette + glyph constants. The
TUI has its own mirrored constants under `tui/internal/app/style.go` and
`tui/internal/app/glyph.go` per `docs/design/terminal-aesthetic.md`; this phase
adds prompt-panel styles to that mirror.

### 4.1 Color budget — strict

`terminal-aesthetic.md` rule: max 2 accent colors per visible screen.
This design's accent budget:

- **Magenta**: prompt panel border ONLY. The single brightest chrome on
  screen. Never used elsewhere — not on logo, not on links, not on
  emphasis text. This is what enforces "the prompt is the center."
- **Green** (running glyph `●`) OR **Cyan** (saturation/cost figures) OR
  **Yellow** (caution): one of these is the second accent at any given
  moment, depending on what state the user is in. Idle screens have only
  magenta.

Everything else: surface white, dim gray, meta dim italic.

### 4.2 Glyphs

| Element              | Glyph (Unicode) | ASCII fallback |
|----------------------|-----------------|----------------|
| Header rule          | `╭╮╰╯─`         | `+|-`          |
| Logo brand bar       | `▏`             | `|`            |
| Logo letterforms     | `G I L` (spaced)| `G I L`        |
| Running session      | `●` (green)     | `*`            |
| Idle session         | `○` (dim)       | `o`            |
| Done check           | `✓`             | `v`            |
| Failed check         | `✗`             | `x`            |
| Prompt panel border  | `╔═╗║╚╝`        | `+=|`          |
| Prompt arrow         | `›`             | `>`            |
| Cursor               | `▎` (blinking)  | `_`            |
| Status divider       | `─`             | `-`            |

### 4.3 Empty-state subtitle text

Below the prompt panel, always one short prose line of what's possible,
state-dependent:

| Phase             | Subtitle                                                       |
|-------------------|----------------------------------------------------------------|
| idle              | describe a task, resume by slug, or ask what's running         |
| interview         | answer the question above, or ask gil to clarify               |
| awaiting-confirm  | type "freeze" to start the run, or keep iterating              |
| run               | run in progress · type to queue follow-ups                     |
| stuck             | recovery exhausted · "stop" to halt or "retry" to continue     |
| done              | run complete · "diff" to review · "merge" to apply             |

These are the user-typeable verbs in NL form. Slash equivalents (`/run`,
`/diff`, `/merge`) remain hidden fallback per §2.6 — never shown in the
subtitle.

## 5. Module placement: TUI module

This surface lives in `tui/internal/app/` (NOT in `cli/internal/chat/`).

Why:

- `tui/` already depends on bubbletea + lipgloss, which give us alt-screen,
  resize handling, key dispatch, and rerender for free. Reimplementing those
  in `cli/` would be 500–800 lines of raw ANSI machinery for a module whose
  charter is line-based output.
- `tui/internal/app/` already has `renderHeader`, `paneBox`, `renderFooter`,
  `clarify`, `permission`, `slash`, `tail` — the chat surface reuses these
  layout helpers. Net new code in `tui/` is one chat-mode model, one input
  panel, one conversation view. Net deletion in `cli/` is the
  StdoutChatRenderer's TTY usage (kept around for non-TTY/script fallback).
- §2.6 "단일 채팅 surface" is satisfied by routing bare-`gil`-on-TTY into
  `tui.RunChat` (the new entry point), so the TUI module owns ALL human-TTY
  surfaces (chat + watch/monitor). The CLI module handles non-TTY only.

What stays in `cli/`:

- `cli/internal/chat/repl/loop.go` — kept for non-TTY scripts that pipe
  `gil chat` for transcript capture. Step (a)'s self-disclosure stays here
  too. Eventually demoted to a `--script` mode flag once the TUI is the
  default everywhere.
- `cli/internal/cmd/uistyle/` — palette/glyph constants. The TUI mirrors
  them in `tui/internal/app/style.go` per existing convention.

What's new in `tui/`:

- `tui/internal/app/chat_model.go` — bubbletea Model for chat mode (state:
  history, input buffer, current phase, session list, streaming buffer)
- `tui/internal/app/chat_view.go` — View() rendering: header, conversation
  region, status strip, prompt panel, subtitle
- `tui/internal/app/chat_input.go` — textinput pane with magenta border,
  history (↑↓), slash-fallback parser
- `tui/internal/app/chat_stream.go` — gRPC bidi adapter; consumes events
  from the daemon and emits bubbletea Msgs

## 6. Routing change in `cli`

`cli/internal/cmd/root.go` RunE shim, currently:

```go
if !noChat && stdoutIsTTY() {
    return runChat(cmd, defaultSocket(), "", "")
}
return runSummary(cmd.OutOrStdout(), defaultSocket(), defaultBase(), asciiMode)
```

becomes:

```go
if !noChat && stdoutIsTTY() {
    return tuirun.Chat(cmd, defaultSocket()) // imports tui/cmd/giltui or new entry
}
return runSummary(...) // unchanged
```

`gil chat` (explicit subcommand) routes to the same `tuirun.Chat` for
consistency. The `--no-chat` flag continues to bypass into `runSummary` for
power users who actively want the line surface (kept as escape hatch per
§2.6).

This is the SHAPE of the cross-module call, not the final import path —
the implementation plan (next step) decides whether to expose
`tui/cmd/giltui` directly or to add a `tui/run` package.

## 7. Resize and degraded layouts

| Terminal size       | Behavior                                                |
|---------------------|---------------------------------------------------------|
| ≥ 80 × 24           | Full layout per §3                                      |
| 60–79 cols          | Header collapses model+context to one line; sessions    |
|                     | drop the rightmost saturation/cost column               |
| < 60 cols           | Drop session list pre-first-turn (still self-disclose   |
|                     | with text "3 past sessions — type /sessions to list")   |
| < 16 rows           | Conversation region clipped to 4 rows; prompt panel     |
|                     | shrinks to 3 rows (border + cursor + border, no padding)|
| `--ascii` mode      | All glyphs swap per table 4.2; magenta accent stays     |
|                     | (single ANSI color is fine without Unicode)             |
| `NO_COLOR`          | Magenta swap to bold; layout otherwise unchanged. The   |
|                     | "prompt is center" message survives because the heavy   |
|                     | `═╔╗` border is intrinsically louder than `─` rule      |
| Not a TTY (pipe)    | `cli` runSummary path runs as today; this surface is    |
|                     | never invoked. SIGWINCH not handled (irrelevant)        |

## 8. State machine: phases ↔ layout

Existing `cli/internal/chat/render/renderer.go` defines the phase enum
(idle / interview / awaiting-confirm / run / stuck / done). The TUI consumes
the same phases and maps them to:

- Status strip body (current "interview · 4/11 slots · sat 36%" style)
- Prompt panel border color (magenta when input enabled, dim when run-phase
  blocks input)
- Cursor glyph (`▎` blinking when input enabled, braille spinner during
  agent turn)
- Subtitle text (per table 4.3)

No new phases are introduced.

## 9. Out of scope (followups)

- **#42 unified non-TTY surface**: pipe path keeps summary one-shot. Future
  phase: render a stripped-down version of this layout to plain stdout.
- **#43 LLM intent router**: the prompt input still ships text straight to
  the daemon's interview/run service. This phase doesn't add the
  natural-language → action classifier — that's step (b)/(c) of §2.6 work.
  This phase makes the surface ready for the router, not the router itself.
- **#44 `gil chat` redundancy**: keeps both bare `gil` and `gil chat`
  routing into the TUI, identical. Removal is downstream cleanup.
- **Scrollback**: bubbletea viewport handles in-session scroll. Persistent
  scrollback across restart is not part of this phase.
- **Multi-line input**: V1 is single-line textinput. Multi-line (newline via
  Shift+Enter, paste handling, code-block-aware) is a downstream task.
- **Session switching from the prompt panel**: in V1, switching is via
  natural language ("switch to refactor-auth-mw") and the eventual intent
  router maps it to the SDK call. Until the router lands, the slash
  `/switch` remains the deterministic path.

## 10. Testing

- **Visual snapshots**: render the View() at fixed terminal sizes
  (100×32, 80×24, 60×18, 40×14) and snapshot the output. Lipgloss
  output is deterministic given fixed env (no animations in snapshots —
  cursor renders as fixed string `<cursor>`, spinner as `<spinner>`,
  via test-mode replacements at the View() boundary, so snapshot diffs
  reflect layout changes only).
- **Phase transitions**: drive the model with synthetic
  `interview.slot_filled`, `run.started`, `run.done` Msgs and assert
  the rendered subtitle, status strip, and prompt border match.
- **Resize**: send `tea.WindowSizeMsg` for each row in §7 and assert
  the layout decision (full / collapsed columns / no list / clipped).
- **NO_COLOR / ASCII**: snapshot under both env settings.
- **Input events**: send keystrokes for typed prompt → assert SendPrompt
  call to the gRPC mock; ↑/↓ → history navigation; `/quit` → graceful
  exit; `/sessions` → fallback to slash dispatch (until router lands).

The TUI module already has `app_test.go` patterns using bubbletea's
test harness; chat-mode tests follow the same shape.

## 11. Acceptance

- [ ] Bare `gil` on a TTY enters the new persistent panel
- [ ] Prompt panel border is the only magenta on screen at idle
- [ ] Past sessions self-disclose pre-first-turn; scroll off after
- [ ] Status strip + subtitle line update with phase
- [ ] Resize redraws layout per §7
- [ ] `--ascii` and `NO_COLOR` produce a usable layout
- [ ] Non-TTY path unchanged (cli runSummary continues to fire)
- [ ] All existing chat-related tests in `cli/internal/chat/repl` still pass
      (line surface preserved as fallback)
- [ ] New TUI tests (snapshot + interaction) pass on the four reference
      sizes
