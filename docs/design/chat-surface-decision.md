# Chat surface — Option B/C decision (P31)

Status: design (2026-05-16)

> Closes the surface-decision item left open by
> [`m6-tui-agent-visualization.md`](m6-tui-agent-visualization.md) §10
> and the Severity-1 "Surface — Option B/C decision" entry in
> [`docs/plans/roadmap-post-v0.2.0.md`](../plans/roadmap-post-v0.2.0.md).
> Option A landed in v0.2.0 (commit ad9274b context). This doc decides
> what — if anything — happens to the bare `gil` chat surface.

## 1. The question

Option A (giltui Agent Tree, fed via the `RunService.Tail` chat-tree
bridge) is shipped. Two options remain on the table:

- **Option B** — redesign the bare `gil` chat surface as multi-pane
  (sessions left rail + transcript center + agent tree pane + diff
  pane). 6-10 files, ~1000-1500 LOC, breaks the 8-9 chat snapshots,
  visual-character change.
- **Option C** — A + B. Same multi-pane in both surfaces, shared
  render functions.

The **status quo** (defer indefinitely) is also a real option, since
giltui already covers users who want the dashboard view.

## 2. The actual gap (what users feel today)

Reading the code surfaced something the original Option B/C framing
missed: the bare `gil` chat `Renderer` interface
(`cli/internal/chat/render/renderer.go:66-76`) has **no
ToolCall / ToolResult methods at all**. It exposes:

```go
type Renderer interface {
    Banner(state SessionState)
    AssistantText(chunk string)
    SystemNote(kind NoteKind, msg string)
    StatusStrip(state SessionState)
    PromptCue()
    Confirm(question string, def bool) (bool, error)
    Diff(hunks []DiffHunk)
    Spec(view *SpecView)
    Close() error
}
```

The chat-architecture design doc claimed "tool_call은 ⚒ 라인,
tool_result는 결과 라인" (`chat-architecture.md` §3.1) but that
contract was never wired into the stdout renderer. So the bare `gil`
user sees **assistant text only**. Tool calls happen invisibly on the
daemon. During a long turn the user sees a spinner-equivalent at best,
not what gil is actually doing.

That's the *real* surface gap, and it's smaller and more concrete than
the original B/C dilemma. The Option B/C debate has been about a
multi-pane redesign that would visually mirror giltui, but the
practical user need is much narrower — **make the agent's actions
visible in the transcript**.

## 3. Decision

**Reject Option B and Option C as written.** Adopt a **narrow
alternative**: thread tool-call/tool-result narration into the
existing single-column transcript via two new Renderer methods. No
pane redesign, no snapshot churn, no character change.

Rationale, in priority order:

1. **Architectural distinction is a feature.** The chat surface and
   giltui surface serve different intents — chat is conversational
   (vertical scrollback, prompt-driven), giltui is dashboard
   (multi-pane, observation-driven). Collapsing them into one shape
   removes the design separation that lets each be excellent at its
   own job. [`terminal-aesthetic.md`](terminal-aesthetic.md) §6 vs §7
   already encodes this split intentionally — bare `gil` is "flat
   layout", giltui is "4-pane TUI".

2. **The actual user pain is narrow.** Users complain "I can't tell
   what gil is doing during a turn." That's solved by a one-line
   narration per tool call, not by a tree pane. The tree-shape value
   (parent/child grouping, expandable children) is dashboard ergonomics
   for *observation*, not for *interaction*.

3. **Snapshot churn cost is real.** Option B breaks 8-9 chat snapshots
   and changes the visual baseline for everyone. The narrow
   alternative adds two new lines to the transcript — additive, not
   replacing.

4. **The dashboard view already exists.** Users who want the Agent
   Tree open giltui in another pane (split tmux / second terminal /
   IDE side panel). Option A landed precisely so this is friction-free.
   The set of users who want the dashboard *inside* the chat surface
   without opening giltui is small — and is dominated by users who
   would also accept inline narration.

5. **Reversibility.** Inline narration is a strictly weaker change than
   a pane redesign. If a future deeply-felt need for in-chat panes
   emerges (e.g. live diff scrubbing during a long turn), the narration
   layer doesn't block it — it's lines in the transcript, removable or
   replaceable. Option B/C, once shipped, owns the chat surface
   character forever.

## 4. The narrow alternative — what gets built

### 4.1 Renderer interface — two new methods

Append to `Renderer` (`cli/internal/chat/render/renderer.go`):

```go
// ToolCall is called when the agent decides to invoke a tool. Renders
// a single line in the transcript: ⚒ <name> <one-line args summary>.
// Returns an opaque handle the caller passes to ToolResult so the
// renderer can correlate (e.g. for inline duration on stdout, or
// in-place line update on a future TUI renderer).
ToolCall(name, argsSummary string) ToolCallHandle

// ToolResult finalizes a previously-announced tool call. On stdout
// renderer, prints a one-line outcome on the next line:
// ✓ <name>  (<duration>)  or  ✗ <name>  (<duration>) <err tail>.
ToolResult(handle ToolCallHandle, ok bool, duration time.Duration, summary string)
```

Plus `type ToolCallHandle any` (renderer-private). `StdoutChatRenderer`
uses an int counter; future renderers can use any opaque key.

### 4.2 Loop wiring

In `cli/internal/chat/repl/loop.go`, the `SessionService.Prompt`
stream consumer already pattern-matches on Part oneof. Add:

```go
case *gilv1.Part_ToolCall:
    h := renderer.ToolCall(p.ToolCall.Name, summarizeArgs(p.ToolCall.Args))
    handles[p.ToolCall.Id] = h
case *gilv1.Part_ToolResult:
    h, ok := handles[p.ToolResult.CallId]
    if !ok { continue }
    renderer.ToolResult(h, !p.ToolResult.IsError,
        durationFromTimestamps(p.ToolResult), summarizeResult(p.ToolResult))
    delete(handles, p.ToolResult.CallId)
```

`summarizeArgs` and `summarizeResult` are one-line truncating
formatters (≤80 chars; ellipsis on overflow).

### 4.3 What it looks like (target visual)

```
> 이 디렉토리의 main.go에 있는 Greeting 상수 값 'Hello, World!'를 'Hello, gil!'로 바꾸고 빌드 통과시켜.

⚒ read_file path=main.go
✓ read_file  (0.1s)

⚒ edit_file path=main.go old="Hello, World!" new="Hello, gil!"
✓ edit_file  (0.2s)

⚒ verify command="go build ./..."
✓ verify  (1.4s)

main.go의 Greeting 상수를 'Hello, gil!'로 변경하고 빌드를 확인했습니다.

  iter 1  ·  3 tools  ·  1.7s  ·  $0.001
```

The trailing `iter 1 · ...` line is the existing `StatusStrip`
(unchanged). The new lines are the four `⚒` / `✓` pairs.

### 4.4 Iconography (matches `terminal-aesthetic.md` §3)

| Phase | Glyph | Color |
|---|---|---|
| tool call announced | `⚒` | accent-info |
| tool result ok | `✓` | success |
| tool result fail | `✗` | alert |
| tool result timeout | `◐` | caution |

Compact-mode (NO_COLOR / ASCII fallback): `*`, `+`, `!`, `~`.

### 4.5 What does NOT change

- chat surface character (single-column transcript).
- giltui Agent Tree (already shipped).
- Renderer's existing methods (additive only).
- `Banner` / `StatusStrip` / `PromptCue` — untouched.
- Snapshot tests for chat — additive, baseline lines stay.

### 4.6 What's explicitly out of scope (separate phases)

- Tree-shape grouping in the chat transcript. The narration is
  flat-per-turn; the tree relationship is implicit (all calls between
  one user prompt and the next assistant text reply belong to the same
  turn).
- Inline diff rendering inside chat (already covered by `Diff(hunks)`).
- Permission ask UI inside chat (separate decision; outside this doc).
- Per-tool collapsible details. Argument and result summaries are
  one-line truncated; users who want full bodies open giltui.

## 5. Implementation outline (sketch — not a plan)

A future P-numbered plan will detail this. Rough size:

| Step | Files | LOC est. |
|---|---|---|
| Renderer interface + handle type | `cli/internal/chat/render/renderer.go` | +20 |
| `StdoutChatRenderer` impl | `cli/internal/chat/render/stdout.go` | +60 |
| `MockChatRenderer` impl | `cli/internal/chat/render/mock.go` | +20 |
| Loop wiring (Part oneof case) | `cli/internal/chat/repl/loop.go` | +40 |
| Args/result summarizers | `cli/internal/chat/render/summarize.go` (new) | +80 |
| Tests | `cli/internal/chat/render/stdout_test.go`, `loop_test.go` | +120 |

Total: ~340 LOC, single PR. ~1/3 of the smallest Option B estimate.

## 6. Out-of-band consequences

### 6.1 m6-tui-agent-visualization.md §10 status update

The "V1 결정 보류" note in §10 is now resolved: V1 = Option A only;
chat surface gets inline tool narration (this doc), not a multi-pane
redesign. Append a §11 "Decision (P31)" pointing here.

### 6.2 roadmap-post-v0.2.0.md

The Severity-1 "Surface — Option B/C decision" item moves to
"resolved — see [chat-surface-decision.md](../design/chat-surface-decision.md)".
The implementation work (the ~340 LOC outlined above) becomes a new
Severity-2 item: "chat tool-call narration".

### 6.3 Deferred concerns

If, in some future quarter, a strong user signal emerges that the
flat transcript is insufficient (e.g. for very long autonomous turns
where users want a collapsible tree-shape mid-stream), Option B can
be revisited *on top of* the narration layer. The narration layer is
not a barrier — it's a building block.

## 7. Self-review

- **Reversible?** Yes. Adding two Renderer methods is additive.
- **Snapshot impact?** New transcript lines, but the existing
  baselines remain valid for the bytes they currently assert. New
  tests cover the new lines independently.
- **Architectural symmetry preserved?** Yes — chat stays
  conversational, giltui stays dashboard. The dashboard data already
  flows through `RunService.Tail`'s chat-tree bridge so giltui's view
  doesn't change either.
- **Production wiring check?** Per
  [`feedback_check_production_wiring`](../../.claude/projects/-home-ubuntu/memory/feedback_check_production_wiring.md):
  the existing `Prompt` stream already emits ToolCall/ToolResult
  Parts (verified via grep on `gilv1.Part_ToolCall` callsites in
  `tui/internal/app/chat_stream.go`). The chat REPL just doesn't
  consume them today. So this is wiring-up, not new plumbing.
- **Failure-floor link?** None directly. P29/P30 closed verify and
  WorkingSet gaps; this closes the visibility gap.
