# Phase 26 — Chat-Only Surface

**Status**: design approved (2026-04-28), implementation pending
**Predecessor**: Phase 25 (UX elevation — onboarding, error hints, /help grouping, slug names, relative time)

## 1. Goal

The user-facing surface of `gil` becomes a single prompt loop. The user types prose; the system internally drives the existing interview engine (slot-filling, adversary, self-audit), the run loop (iterations, stuck recovery, budget), and context management — and **surfaces those internal phases inline so the user can see that gil is doing more than relaying prompts to an LLM**.

The success criterion is a fix for the user feedback "그냥 LLM endpoint를 연결한것이랑 다를게 없는 느낌이야 — 온전하지가 않아." After Phase 26, a new user typing `gil` and entering their first prompt sees the interview phase manifest itself (slots filling, saturation rising, adversary findings, freeze handoff), a confirmed transition into autonomous run, and live observation of run progress — all in one stdout chat surface.

## 2. Non-Goals

- **No Bubbletea TUI** in this phase. The Renderer abstraction is designed so a TUI implementation can be added later; we do not build it now.
- **No web/desktop UI**. Same reason.
- **No removal of verb-mode CLI**. The 35+ subcommands (`gil new`, `gil interview`, `gil run`, `gil status`, etc.) are preserved as a headless/automation channel, hidden from the default `--help` view via cobra grouping.
- **No multi-user or cloud VM backend work**. Phase 9+ territory.
- **No new LLM provider, model, or sandbox**. Pure surface redesign.

## 3. Architecture

### 3.1 Renderer Abstraction (extensibility safeguard)

A new package `cli/internal/chat/render` defines a single interface:

```go
type Renderer interface {
    Banner(state SessionState)
    AssistantText(chunk string)              // streamed model output
    SystemNote(kind NoteKind, msg string)    // [spec], [adversary], [note queued]
    StatusStrip(state SessionState)          // re-drawn above each prompt
    PromptCue()                              // ">" prompt
    Confirm(question string, def bool) (bool, error)
    Diff(hunks []DiffHunk)
    Spec(spec *sdk.SpecView)
    Close() error
}
```

V1 ships one implementation, `StdoutChatRenderer`. All chat code routes output through the interface — no `fmt.Println` in chat paths. Future Bubbletea TUI or web/desktop UI implements the same interface and slots in without touching REPL logic.

### 3.2 Chat REPL = EventStream Consumer

```
gil (chat)  ──gRPC subscribe──▶  daemon EventStream
   │                                       ▲
   │ user prompt ──InterviewService.Reply──┘
   │             (or RunService.AddNote when in run — V1.1)
   ▼
Renderer.AssistantText / SystemNote / StatusStrip
```

The current `runChat()` intent classifier (NEW_TASK / RESUME / STATUS / HELP / EXPLAIN) is removed. The new contract:

- **Bare prompt** = "next message in the active session." If no session is active, the first prompt creates one (slug name + workspace pre-fill auto).
- **`/`-prefixed input** = explicit slash command. Parsed client-side, dispatched to a static command table.
- **Slash with no active session**: session-agnostic commands (`/sessions`, `/new`, `/help`, `/quit`) work; session-bound commands (`/spec`, `/status`, `/diff`, `/merge`, `/run`) error gracefully with `"no active session — start one with a prompt or /sessions"`.
- **`gil` invocation**: bare `gil` (no args, TTY detected) enters chat. `gil chat` may be retained as an explicit alias for scripting clarity; decision deferred to implementation.

Client-side state tracks the active session's phase by consuming EventStream. Phases: `idle`, `interview`, `awaiting-confirm`, `run`, `done`, `stuck`.

### 3.3 Verb-Mode CLI as Headless Channel

The 35+ existing verb subcommands are not removed. They are moved into a cobra group labeled "Advanced (headless / scripting)" using the group infrastructure already added in Phase 25 A2. CI scripts, cron jobs, and subprocess callers continue to function unchanged. Default `gil --help` shows only the chat surface and a small set of headline verbs (`init`, `auth`, `doctor`, `daemon`); the advanced group is collapsed by default, surfaced via `gil --help --verbose` or by typing `gil <unknown-verb>`.

## 4. Chat UX

### 4.1 Status Strip (5 phase variants)

A single dim line redrawn above each prompt. Color via uistyle palette (NO_COLOR + --ascii honored).

| Phase | Strip example |
|---|---|
| idle | `[idle · type a prompt to start, or /sessions to resume]` |
| interview | `[interview · 4/11 slots · sat 36% · 1 adv finding]` |
| awaiting-confirm | `[interview · ready to freeze · /run to start, prompt to keep iterating]` |
| run | `[run · iter 23/100 · $0.61 · ASK_DESTRUCTIVE]` |
| stuck | `[run · iter 45/100 · STUCK after recovery]` |
| done | `[done · 87 iters · $2.34 · ✓ 4/4 checks · /diff /merge]` |

Color mapping: interview=Info, awaiting-confirm=Primary, run=Primary, stuck=Caution, done=Primary, idle=Dim.

### 4.2 Slash Commands (V1 minimum)

| Command | Purpose |
|---|---|
| `/sessions` | List sessions, fuzzy pick |
| `/switch <id\|name>` | Switch active session |
| `/new` | Start a new session explicitly (rare; first prompt auto-creates) |
| `/quit` | Exit chat (daemon + sessions keep running) |
| `/spec` | Show current spec (yaml) |
| `/status` | Full status snapshot |
| `/diff` | Shadow git diff |
| `/merge` | Apply diff, with default-no inline confirm |
| `/run` | Explicit run start from awaiting-confirm phase |
| `/help` | Show slash commands |

V1 deliberately omits commands requiring server-side coordination: `/interrupt`, `/stop`, `/cost` (already verb).

### 4.3 Phase Transitions

**Saturation reached → confirm** (no auto-freeze):
```
[interview · ready to freeze]
gil: Spec covers 11/11 slots, adversary 0 blockers. Start the run?
     ~80 iters cap · ~$3 budget · autonomy: ASK_DESTRUCTIVE
     /run to start, or any prompt to keep iterating.
```

**Run started**:
```
[run · iter 1/100]
gil: Starting. Slash commands: /status /diff (run-control in V1.1).
```

**Run done**:
```
[done · 87 iters · $2.34 · ✓ 4/4 checks]
gil: /diff to review (12 files, +340 -82), /merge to apply.
```

**Stuck after recovery**:
```
[run · STUCK]
gil: Stuck pattern: repeated-action-error 3x. Recovery (alt_tool_order + escalate) didn't unblock.
     V1.1 will offer /interrupt and /stop. For now, server-side stop via `gil stop <id>`.
```

### 4.4 Run-Time User Prompt Behavior

**V1**: When in `run` phase, user prompts are **echoed back as a system note** explaining that run-time interaction is V1.1:
```
[run · iter 23/100]
> hey, also add a tooltip on hover
[note] run-time prompts are V1.1. For now: wait for done, or `gil stop <id>` from another shell.
```

**V1.1** (deferred): User prompts queue as notes, injected into agent's next-turn system prompt at the next iter boundary:
```
[run · iter 23/100]
> hey, also add a tooltip on hover
[note queued · agent will see at iter 24]
```

This requires a new `RunService.AddNote(sessionID, text)` RPC + agent loop consumption logic.

### 4.5 Inline Confirm

Reuse `confirmInline()` already built in Phase 25 S3 (`chat_onboarding.go`). Convention: destructive (`/merge`, `/stop`) defaults to N; progressive (`/run`) defaults to Y.

## 5. Migration Plan

### 5.1 File changes

**New (`cli/internal/chat/`)**:
- `render/renderer.go` — interface + types (NoteKind, SessionState, DiffHunk)
- `render/stdout.go` — StdoutChatRenderer implementation
- `repl/loop.go` — REPL body: EventStream subscribe, slash dispatch, prompt routing
- `repl/slash.go` — slash command table + parser
- `repl/state.go` — client-side phase tracker, derived from EventStream

**Major edits**:
- `cli/internal/cmd/chat.go` — strip intent classifier; reduce to thin caller of `repl.Run()`. Keep onboarding gate (chat_onboarding.go) intact.
- `cli/internal/cmd/root.go` — move 30+ verbs into cobra group `"Advanced (headless / scripting)"` using `GroupID`. Mark headline verbs (`init`, `auth`, `doctor`, `daemon`) as primary group.

**Minor edits**:
- `cli/internal/cmd/chat_test.go` — update expectations (intent classifier removed, slash routing added)
- `cli/internal/cmd/summary.go`, `status_render.go` — keep as verb-only fallback rendering, not used by chat REPL

**No edits in V1**:
- `chat_onboarding.go` (Phase 25 S3 work — entry gate stays)
- Server (`server/`, `core/`, `proto/`) — V1 is purely client-side
- SDK (`sdk/`) — gRPC stubs unchanged

### 5.2 V1 Cut vs V1.1

**V1 (this phase, target: feels-whole chat)**:
1. Renderer interface + StdoutChatRenderer
2. REPL replacing intent classifier
3. EventStream consumer surfacing interview slot/saturation/adversary inline
4. Saturation → confirm dialog
5. Slash commands: `/sessions /switch /new /spec /status /diff /merge /run /quit /help`
6. Verb CLI moved to "advanced" group
7. Run-phase user prompts echo "V1.1" note

**V1.1 (deferred, requires server changes)**:
- `/interrupt`, `/stop` slash commands (server cancellation coordination)
- Run-time queued notes (`RunService.AddNote` proto + agent injection)
- `/cost` slash (low priority — verb already covers)

This split is deliberate: V1 is almost entirely client-side, low risk, no proto changes. V1.1 carries the server-coordination complexity.

## 6. Testing

- **Unit — Renderer**: capture-mock implementation asserts call sequence + content. Same tests reusable when Bubbletea/web renderers added.
- **Unit — slash parser**: input → command + args mapping; malformed prefix handling.
- **Unit — status strip**: snapshot tests for all 5 phase variants, with NO_COLOR and --ascii combinations.
- **Unit — phase tracker**: feed canned EventStream sequences, assert phase transitions.
- **Integration — REPL**: mock daemon stream, assert REPL emits correct Renderer calls for an interview→saturation→confirm→run sequence.
- **E2E — dogfood**: real interview session through new chat surface. Verify slot updates, saturation %, adversary findings all visibly surface. Run a small actual mission to done; verify /diff /merge work.

## 7. Risks & Mitigations

- **EventStream may not emit slot/adversary/saturation events today**. **Step 0** of the implementation plan is an event-catalog audit (`proto/gil/v1/event.proto` + emitter call sites in `core/interview/` and `server/`). Decision rule: if ≤3 missing event types, expand V1 scope to include the proto + emitter additions (V1 grows from "client-side only" to "client-side + small server delta"). If >3, defer the gap-fillers to V1.1 and surface only the events that already exist (degraded but shippable V1).
- **chat_test.go expectations change broadly**. Expected; rewrite to assert against Renderer mock calls, not raw stdout strings.
- **Verb-mode callers broken**. Risk = 0. No verbs are removed; only re-grouped in --help.
- **Phase tracker drift between client and server**. Mitigation: phase derived only from EventStream events the server emits; never from client-side guesses. If server says phase=run, client says phase=run.
- **Slash command discoverability**. Mitigation: `[idle]` strip mentions `/sessions`; `[awaiting-confirm]` mentions `/run`; `[done]` mentions `/diff /merge`. Always one slash visible in the strip when relevant.

## 8. Open Decisions Deferred to Implementation

- Exact saturation % threshold for `awaiting-confirm` strip (server already computes; surface what server sends).
- Behavior when EventStream connection drops mid-run: client should reconnect with last-event resume. Detail belongs in implementation plan, not design.
- Whether `/sessions` fuzzy-pick uses bubbletea (a constrained TUI for one widget, not full surface) or plain numbered list. Lean: numbered list for V1, matches stdout-chat aesthetic.

## 9. Out of Scope (this phase)

- Bubbletea TUI implementation (Renderer abstraction enables it later).
- Web/desktop UI.
- Multi-user, cloud VM backends (Phase 9+).
- New LLM providers, sandbox modes.
- Honcho-style cross-session user modeling.

## 10. Acceptance

Phase 26 V1 is done when a user typing `gil` from a clean install:
1. Sees the Phase 25 onboarding gate (NoInit / NoCreds) if applicable.
2. Enters chat, types a mission prompt, watches slot count + saturation % rise + adversary findings appear inline in dim system notes.
3. Receives the saturation → confirm dialog at freeze readiness.
4. Confirms with `/run`, watches iter/cost strip update during the run.
5. On done, sees the `[done]` strip with `/diff /merge` hints; runs both, mission applied to repo.
6. The verb-mode CLI continues to work for any scripted or programmatic caller.

V1.1 adds run-time control (`/interrupt`, `/stop`, queued notes) once server cancellation + note-injection plumbing lands.
