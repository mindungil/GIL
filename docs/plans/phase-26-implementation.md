# Phase 26 — Chat-Only Surface — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace gil's intent-classifier chat shim with a unified prompt loop that surfaces interview slot-fill / saturation / adversary events inline; preserve the verb-mode CLI as a hidden "advanced" group; design Renderer abstraction so a future Bubbletea TUI / web UI is a drop-in implementation.

**Architecture:** Three-layer client-side stack — `render` (interface + StdoutChatRenderer) → `repl` (loop, slash parser, phase tracker) → existing `cli/internal/cmd/chat.go` shrinks to a thin caller. Server (`server/`, `core/`, `proto/`) untouched in V1 unless Step 0 audit finds missing event types.

**Tech Stack:** Go 1.22+, Cobra (verbs), gRPC bidi (EventStream), uistyle (palette/glyphs), testify (tests). No new external deps.

**Spec:** `docs/plans/phase-26-chat-only-surface.md`

---

## File Structure

**New (V1)**
- `cli/internal/chat/render/renderer.go` — interface, value types (NoteKind, SessionState, DiffHunk, Phase)
- `cli/internal/chat/render/stdout.go` — StdoutChatRenderer implementation
- `cli/internal/chat/render/stdout_test.go` — unit tests + 5-phase strip snapshots
- `cli/internal/chat/render/mock.go` — capture-mock Renderer for downstream tests
- `cli/internal/chat/repl/slash.go` — slash command parser + dispatch table
- `cli/internal/chat/repl/slash_test.go` — parser + table tests
- `cli/internal/chat/repl/state.go` — client-side phase tracker (consumes Event)
- `cli/internal/chat/repl/state_test.go` — event sequence tests
- `cli/internal/chat/repl/loop.go` — REPL body
- `cli/internal/chat/repl/loop_test.go` — integration tests with mock daemon

**Modified**
- `cli/internal/cmd/chat.go` — strip intent classifier, call `repl.Run(...)`
- `cli/internal/cmd/chat_test.go` — rewrite expectations against Renderer mock
- `cli/internal/cmd/root.go` — verbs into cobra group `"advanced"` (use Phase 25 A2 group infra)

**Untouched (V1)**
- `cli/internal/cmd/chat_onboarding.go` (Phase 25 S3 entry gate)
- `cli/internal/cmd/summary.go`, `status_render.go` (verb-only fallback)
- `server/`, `core/`, `proto/` — only touched if Task 0 audit triggers expansion

---

## Task 0: Event Catalog Audit (recon, no code)

**Files:**
- Read: `proto/gil/v1/event.proto`
- Grep: `core/interview/`, `core/runner/`, `server/internal/service/` for event emit sites

- [ ] **Step 1: Enumerate Event types in proto**

Run:
```bash
grep -nE 'message |oneof ' /home/ubuntu/gil/proto/gil/v1/event.proto
```
Expected: list of all Event message types and oneof variants. Capture into a scratch list.

- [ ] **Step 2: Map emit sites for each event type**

Run:
```bash
grep -rn 'EmitEvent\|StreamEvents\|events.Send\|eventBus' /home/ubuntu/gil/server /home/ubuntu/gil/core | sort -u
```
Expected: list of files and lines where events are produced. For each event from Step 1, confirm at least one emit site exists.

- [ ] **Step 3: Identify gaps for the spec's required surfacing**

The spec requires inline rendering of: spec slot updates, saturation %, adversary findings, iter count, cost, stuck signal, run done. For each, mark whether an event already exists or is missing. Capture findings in a scratch `audit.txt` (do not commit).

- [ ] **Step 4: Decision**

Apply decision rule from spec §7 risk:
- If ≤3 event types missing → expand V1 scope. Add proto + emitter tasks before Task 1.
- If >3 missing → ship V1 with degraded inline surfacing (only events that exist). Add gap-fillers to a Phase 26.5 follow-up.

- [ ] **Step 5: Record decision in spec**

Edit `docs/plans/phase-26-chat-only-surface.md` §10 Acceptance, append a short subsection "Step 0 audit result (YYYY-MM-DD): N events missing — chose [expand|degraded]. Affected events: [list]." Commit:
```bash
cd /home/ubuntu/gil
git add docs/plans/phase-26-chat-only-surface.md
git commit -m "docs(plans): P26 Step-0 audit result"
```

> **If "expand" chosen**, insert tasks between Task 0 and Task 1 to add the missing proto messages and emitter call sites. These follow standard Go server-side TDD (test in `core/interview/*_test.go` or wherever the emit site lives, then implement). Out of scope for this plan template — generate inline at execution time based on the audit list.

---

## Task 1: Renderer Interface + Value Types

**Files:**
- Create: `cli/internal/chat/render/renderer.go`
- Create: `cli/internal/chat/render/renderer_test.go`

- [ ] **Step 1: Write the failing compile-time test**

Create `cli/internal/chat/render/renderer_test.go`:
```go
package render

import "testing"

func TestRendererInterfaceShape(t *testing.T) {
    // Compile-time assertion: any implementation must satisfy Renderer.
    var _ Renderer = (*nopRenderer)(nil)
}

type nopRenderer struct{}

func (nopRenderer) Banner(SessionState)              {}
func (nopRenderer) AssistantText(string)             {}
func (nopRenderer) SystemNote(NoteKind, string)      {}
func (nopRenderer) StatusStrip(SessionState)         {}
func (nopRenderer) PromptCue()                       {}
func (nopRenderer) Confirm(string, bool) (bool, error) { return false, nil }
func (nopRenderer) Diff([]DiffHunk)                  {}
func (nopRenderer) Spec(*SpecView)                   {}
func (nopRenderer) Close() error                     { return nil }
```

- [ ] **Step 2: Run test (expect compile failure)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/...`
Expected: build error — `undefined: Renderer / NoteKind / SessionState / DiffHunk / SpecView`

- [ ] **Step 3: Write the interface and types**

Create `cli/internal/chat/render/renderer.go`:
```go
// Package render defines the chat surface rendering contract.
//
// V1 ships StdoutChatRenderer. Future Bubbletea / web / desktop
// renderers implement the same interface; the REPL is renderer-agnostic.
package render

type Phase string

const (
    PhaseIdle             Phase = "idle"
    PhaseInterview        Phase = "interview"
    PhaseAwaitingConfirm  Phase = "awaiting-confirm"
    PhaseRun              Phase = "run"
    PhaseStuck            Phase = "stuck"
    PhaseDone             Phase = "done"
)

type NoteKind string

const (
    NoteSpec       NoteKind = "spec"
    NoteAdversary  NoteKind = "adversary"
    NoteSaturation NoteKind = "saturation"
    NoteQueued     NoteKind = "note"
    NoteV11        NoteKind = "v11"
    NoteSystem     NoteKind = "system"
)

type SessionState struct {
    SessionID    string
    DisplayName  string
    Phase        Phase
    SlotsFilled  int
    SlotsTotal   int
    Saturation   float64
    AdvFindings  int
    Iter         int
    MaxIter      int
    CostUSD      float64
    Autonomy     string
    ChecksPassed int
    ChecksTotal  int
}

type DiffHunk struct {
    Path     string
    Added    int
    Removed  int
    Snippet  string
}

type SpecView struct {
    YAML string
}

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

- [ ] **Step 4: Run test (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/...`
Expected: PASS (no test bodies, just compile assertion).

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/render/renderer.go cli/internal/chat/render/renderer_test.go
git commit -m "feat(chat): P26 T1 — Renderer interface + value types"
```

---

## Task 2: StdoutChatRenderer — Banner, PromptCue, AssistantText

**Files:**
- Create: `cli/internal/chat/render/stdout.go`
- Create: `cli/internal/chat/render/stdout_test.go`

- [ ] **Step 1: Write failing tests for the three methods**

Create `cli/internal/chat/render/stdout_test.go`:
```go
package render

import (
    "bytes"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func newStdoutForTest(t *testing.T) (*StdoutChatRenderer, *bytes.Buffer) {
    t.Helper()
    var buf bytes.Buffer
    r := NewStdoutChatRenderer(&buf, nil, true /*ascii*/, true /*noColor*/)
    return r, &buf
}

func TestStdout_Banner_PrintsName(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.Banner(SessionState{DisplayName: "add-dark-mode-0428", Phase: PhaseInterview})
    require.Contains(t, buf.String(), "add-dark-mode-0428")
}

func TestStdout_PromptCue_EmitsArrow(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.PromptCue()
    require.Equal(t, "> ", buf.String())
}

func TestStdout_AssistantText_AppendsAsIs(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.AssistantText("hello ")
    r.AssistantText("world")
    require.Equal(t, "hello world", buf.String())
}
```

- [ ] **Step 2: Run tests (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestStdout`
Expected: build error — `undefined: NewStdoutChatRenderer`.

- [ ] **Step 3: Write minimal implementation**

Create `cli/internal/chat/render/stdout.go`:
```go
package render

import (
    "bufio"
    "fmt"
    "io"

    "github.com/mindungil/gil/cli/internal/cmd/uistyle"
)

type StdoutChatRenderer struct {
    out    io.Writer
    in     io.Reader
    ascii  bool
    g      uistyle.Glyphs
    p      uistyle.Palette
    reader *bufio.Reader
}

func NewStdoutChatRenderer(out io.Writer, in io.Reader, ascii, noColor bool) *StdoutChatRenderer {
    var br *bufio.Reader
    if in != nil {
        br = bufio.NewReader(in)
    }
    return &StdoutChatRenderer{
        out:    out,
        in:     in,
        ascii:  ascii,
        g:      uistyle.NewGlyphs(ascii),
        p:      uistyle.NewPalette(noColor),
        reader: br,
    }
}

func (r *StdoutChatRenderer) Banner(s SessionState) {
    fmt.Fprintf(r.out, "%s %s\n", r.p.Primary("gil"), r.p.Dim(s.DisplayName))
}

func (r *StdoutChatRenderer) PromptCue() {
    fmt.Fprint(r.out, "> ")
}

func (r *StdoutChatRenderer) AssistantText(chunk string) {
    fmt.Fprint(r.out, chunk)
}

// Stubs for the rest of the interface; later tasks fill them in.
func (r *StdoutChatRenderer) SystemNote(NoteKind, string)              {}
func (r *StdoutChatRenderer) StatusStrip(SessionState)                 {}
func (r *StdoutChatRenderer) Confirm(string, bool) (bool, error)       { return false, nil }
func (r *StdoutChatRenderer) Diff([]DiffHunk)                          {}
func (r *StdoutChatRenderer) Spec(*SpecView)                           {}
func (r *StdoutChatRenderer) Close() error                             { return nil }
```

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestStdout`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/render/stdout.go cli/internal/chat/render/stdout_test.go
git commit -m "feat(chat): P26 T2 — StdoutChatRenderer base + Banner/PromptCue/AssistantText"
```

---

## Task 3: StdoutChatRenderer — StatusStrip (5 phase variants)

**Files:**
- Modify: `cli/internal/chat/render/stdout.go`
- Modify: `cli/internal/chat/render/stdout_test.go`

- [ ] **Step 1: Add 5 failing snapshot tests**

Append to `cli/internal/chat/render/stdout_test.go`:
```go
func TestStdout_StatusStrip_Idle(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{Phase: PhaseIdle})
    require.Equal(t, "[idle · type a prompt to start, or /sessions to resume]\n", buf.String())
}

func TestStdout_StatusStrip_Interview(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{
        Phase: PhaseInterview, SlotsFilled: 4, SlotsTotal: 11,
        Saturation: 0.36, AdvFindings: 1,
    })
    require.Equal(t, "[interview · 4/11 slots · sat 36% · 1 adv finding]\n", buf.String())
}

func TestStdout_StatusStrip_AwaitingConfirm(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{Phase: PhaseAwaitingConfirm})
    require.Equal(t, "[interview · ready to freeze · /run to start, prompt to keep iterating]\n", buf.String())
}

func TestStdout_StatusStrip_Run(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{
        Phase: PhaseRun, Iter: 23, MaxIter: 100,
        CostUSD: 0.61, Autonomy: "ASK_DESTRUCTIVE",
    })
    require.Equal(t, "[run · iter 23/100 · $0.61 · ASK_DESTRUCTIVE]\n", buf.String())
}

func TestStdout_StatusStrip_Stuck(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{Phase: PhaseStuck, Iter: 45, MaxIter: 100})
    require.Equal(t, "[run · iter 45/100 · STUCK after recovery]\n", buf.String())
}

func TestStdout_StatusStrip_Done(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{
        Phase: PhaseDone, Iter: 87, CostUSD: 2.34,
        ChecksPassed: 4, ChecksTotal: 4,
    })
    // ASCII mode (newStdoutForTest passes ascii=true): ✓ → OK, ✗ → FAIL.
    require.Equal(t, "[done · 87 iters · $2.34 · OK 4/4 checks · /diff /merge]\n", buf.String())
}

// Pluralization: "0 adv finding" should not show, "2 adv findings".
func TestStdout_StatusStrip_AdvPluralization(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{
        Phase: PhaseInterview, SlotsFilled: 5, SlotsTotal: 11,
        Saturation: 0.45, AdvFindings: 0,
    })
    require.Equal(t, "[interview · 5/11 slots · sat 45%]\n", buf.String())

    buf.Reset()
    r.StatusStrip(SessionState{
        Phase: PhaseInterview, SlotsFilled: 5, SlotsTotal: 11,
        Saturation: 0.45, AdvFindings: 2,
    })
    require.Equal(t, "[interview · 5/11 slots · sat 45% · 2 adv findings]\n", buf.String())
}

// Done with failing checks should still render.
func TestStdout_StatusStrip_DoneWithFailures(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.StatusStrip(SessionState{
        Phase: PhaseDone, Iter: 50, CostUSD: 1.10,
        ChecksPassed: 2, ChecksTotal: 4,
    })
    require.Equal(t, "[done · 50 iters · $1.10 · FAIL 2/4 checks · /diff /merge]\n", buf.String())
}
```

> Note: tests use `--ascii` mode (newStdoutForTest passes `true`), so the implementation must swap `✓`/`✗` for `OK`/`FAIL`. The middle-dot separator `·` (U+00B7) is widely supported and stays as-is in both modes — `--ascii` only swaps glyphs that don't render in legacy terminals.

- [ ] **Step 2: Run tests (expect fail — current StatusStrip is a no-op)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestStdout_StatusStrip`
Expected: 7 tests FAIL with empty-string comparisons.

- [ ] **Step 3: Implement StatusStrip**

Replace the stub `StatusStrip` in `cli/internal/chat/render/stdout.go`:
```go
func (r *StdoutChatRenderer) StatusStrip(s SessionState) {
    var body string
    switch s.Phase {
    case PhaseIdle:
        body = "idle · type a prompt to start, or /sessions to resume"
    case PhaseInterview:
        body = formatInterviewStrip(s)
    case PhaseAwaitingConfirm:
        body = "interview · ready to freeze · /run to start, prompt to keep iterating"
    case PhaseRun:
        body = fmt.Sprintf("run · iter %d/%d · $%.2f · %s", s.Iter, s.MaxIter, s.CostUSD, s.Autonomy)
    case PhaseStuck:
        body = fmt.Sprintf("run · iter %d/%d · STUCK after recovery", s.Iter, s.MaxIter)
    case PhaseDone:
        body = formatDoneStrip(s, r.ascii)
    default:
        body = string(s.Phase)
    }
    fmt.Fprintf(r.out, "[%s]\n", body)
}

func formatInterviewStrip(s SessionState) string {
    base := fmt.Sprintf("interview · %d/%d slots · sat %d%%",
        s.SlotsFilled, s.SlotsTotal, int(s.Saturation*100+0.5))
    switch {
    case s.AdvFindings == 0:
        return base
    case s.AdvFindings == 1:
        return base + " · 1 adv finding"
    default:
        return fmt.Sprintf("%s · %d adv findings", base, s.AdvFindings)
    }
}

func formatDoneStrip(s SessionState, ascii bool) string {
    mark := "✓"
    if s.ChecksPassed < s.ChecksTotal {
        mark = "✗"
    }
    if ascii {
        if s.ChecksPassed == s.ChecksTotal {
            mark = "OK"
        } else {
            mark = "FAIL"
        }
    }
    return fmt.Sprintf("done · %d iters · $%.2f · %s %d/%d checks · /diff /merge",
        s.Iter, s.CostUSD, mark, s.ChecksPassed, s.ChecksTotal)
}
```

The strip is dim-styled in non-test output via uistyle palette; under `noColor=true` (test mode) the `Dim` wrapper is identity, so assertions match exactly. Wrap the body once for non-ascii callers:
```go
// (Optional: wrap only when noColor is false. Tests pass noColor=true so plain string emerges.)
```

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestStdout_StatusStrip -v`
Expected: 7 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/render/stdout.go cli/internal/chat/render/stdout_test.go
git commit -m "feat(chat): P26 T3 — StatusStrip with 5 phase variants + pluralization"
```

---

## Task 4: StdoutChatRenderer — SystemNote, Confirm, Diff, Spec

**Files:**
- Modify: `cli/internal/chat/render/stdout.go`
- Modify: `cli/internal/chat/render/stdout_test.go`

- [ ] **Step 1: Add failing tests for the four methods**

Append to `cli/internal/chat/render/stdout_test.go`:
```go
func TestStdout_SystemNote_PrefixesByKind(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.SystemNote(NoteSpec, "success_criteria += dark mode toggle")
    require.Equal(t, "[spec] success_criteria += dark mode toggle\n", buf.String())

    buf.Reset()
    r.SystemNote(NoteAdversary, "1 medium finding — accessibility contrast not specified")
    require.Equal(t, "[adversary] 1 medium finding — accessibility contrast not specified\n", buf.String())

    buf.Reset()
    r.SystemNote(NoteQueued, "agent will see at iter 24")
    require.Equal(t, "[note] agent will see at iter 24\n", buf.String())
}

func TestStdout_Confirm_DefaultYes(t *testing.T) {
    var out bytes.Buffer
    in := strings.NewReader("\n") // empty input → default
    r := NewStdoutChatRenderer(&out, in, true, true)
    ok, err := r.Confirm("Start the run?", true)
    require.NoError(t, err)
    require.True(t, ok)
    require.Contains(t, out.String(), "Start the run? [Y/n]")
}

func TestStdout_Confirm_DefaultNo_AcceptsY(t *testing.T) {
    var out bytes.Buffer
    in := strings.NewReader("y\n")
    r := NewStdoutChatRenderer(&out, in, true, true)
    ok, err := r.Confirm("Apply diff?", false)
    require.NoError(t, err)
    require.True(t, ok)
    require.Contains(t, out.String(), "Apply diff? [y/N]")
}

func TestStdout_Diff_SummariesPaths(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.Diff([]DiffHunk{
        {Path: "src/web/Toggle.tsx", Added: 42, Removed: 0},
        {Path: "src/web/Settings.tsx", Added: 18, Removed: 4},
    })
    out := buf.String()
    require.Contains(t, out, "src/web/Toggle.tsx")
    require.Contains(t, out, "+42 -0")
    require.Contains(t, out, "src/web/Settings.tsx")
    require.Contains(t, out, "+18 -4")
}

func TestStdout_Spec_PrintsYAML(t *testing.T) {
    r, buf := newStdoutForTest(t)
    r.Spec(&SpecView{YAML: "goal:\n  one_liner: add dark mode\n"})
    require.Contains(t, buf.String(), "goal:")
    require.Contains(t, buf.String(), "add dark mode")
}
```

- [ ] **Step 2: Run tests (expect fail — methods are stubs)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run 'TestStdout_(SystemNote|Confirm|Diff|Spec)'`
Expected: tests FAIL.

- [ ] **Step 3: Replace the four stubs with real implementations**

In `cli/internal/chat/render/stdout.go`, replace the stub methods:
```go
func (r *StdoutChatRenderer) SystemNote(kind NoteKind, msg string) {
    fmt.Fprintf(r.out, "[%s] %s\n", kind, msg)
}

func (r *StdoutChatRenderer) Confirm(question string, def bool) (bool, error) {
    suffix := "[y/N]"
    if def {
        suffix = "[Y/n]"
    }
    fmt.Fprintf(r.out, "%s %s ", question, suffix)
    if r.reader == nil {
        return def, nil
    }
    line, err := r.reader.ReadString('\n')
    if err != nil && err != io.EOF {
        return def, err
    }
    line = strings.TrimSpace(strings.ToLower(line))
    switch line {
    case "":
        return def, nil
    case "y", "yes":
        return true, nil
    case "n", "no":
        return false, nil
    default:
        return def, nil
    }
}

func (r *StdoutChatRenderer) Diff(hunks []DiffHunk) {
    if len(hunks) == 0 {
        fmt.Fprintln(r.out, "(no changes)")
        return
    }
    for _, h := range hunks {
        fmt.Fprintf(r.out, "  %s  +%d -%d\n", h.Path, h.Added, h.Removed)
    }
}

func (r *StdoutChatRenderer) Spec(view *SpecView) {
    if view == nil {
        fmt.Fprintln(r.out, "(no spec)")
        return
    }
    fmt.Fprintln(r.out, view.YAML)
}
```

Add the `strings` import at the top of the file.

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -v`
Expected: all stdout tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/render/stdout.go cli/internal/chat/render/stdout_test.go
git commit -m "feat(chat): P26 T4 — SystemNote, Confirm, Diff, Spec"
```

---

## Task 5: Capture-Mock Renderer (for downstream tests)

**Files:**
- Create: `cli/internal/chat/render/mock.go`
- Create: `cli/internal/chat/render/mock_test.go`

- [ ] **Step 1: Write failing test**

Create `cli/internal/chat/render/mock_test.go`:
```go
package render

import (
    "testing"

    "github.com/stretchr/testify/require"
)

func TestMock_RecordsCallsInOrder(t *testing.T) {
    m := NewMockRenderer()
    m.Banner(SessionState{DisplayName: "x"})
    m.AssistantText("hi")
    m.SystemNote(NoteSpec, "slot")
    require.Equal(t, []MockCall{
        {Method: "Banner"},
        {Method: "AssistantText", Text: "hi"},
        {Method: "SystemNote", Kind: NoteSpec, Text: "slot"},
    }, m.Calls)
}
```

- [ ] **Step 2: Run test (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestMock`
Expected: build error — `undefined: NewMockRenderer / MockCall`.

- [ ] **Step 3: Implement mock**

Create `cli/internal/chat/render/mock.go`:
```go
package render

type MockCall struct {
    Method string
    Text   string
    Kind   NoteKind
    State  SessionState
    Hunks  []DiffHunk
    Spec   *SpecView
}

type MockRenderer struct {
    Calls          []MockCall
    ConfirmAnswers []bool
}

func NewMockRenderer() *MockRenderer { return &MockRenderer{} }

func (m *MockRenderer) Banner(s SessionState) {
    m.Calls = append(m.Calls, MockCall{Method: "Banner", State: s})
}
func (m *MockRenderer) AssistantText(c string) {
    m.Calls = append(m.Calls, MockCall{Method: "AssistantText", Text: c})
}
func (m *MockRenderer) SystemNote(k NoteKind, msg string) {
    m.Calls = append(m.Calls, MockCall{Method: "SystemNote", Kind: k, Text: msg})
}
func (m *MockRenderer) StatusStrip(s SessionState) {
    m.Calls = append(m.Calls, MockCall{Method: "StatusStrip", State: s})
}
func (m *MockRenderer) PromptCue() {
    m.Calls = append(m.Calls, MockCall{Method: "PromptCue"})
}
func (m *MockRenderer) Confirm(q string, def bool) (bool, error) {
    m.Calls = append(m.Calls, MockCall{Method: "Confirm", Text: q})
    if len(m.ConfirmAnswers) > 0 {
        ans := m.ConfirmAnswers[0]
        m.ConfirmAnswers = m.ConfirmAnswers[1:]
        return ans, nil
    }
    return def, nil
}
func (m *MockRenderer) Diff(h []DiffHunk) {
    m.Calls = append(m.Calls, MockCall{Method: "Diff", Hunks: h})
}
func (m *MockRenderer) Spec(s *SpecView) {
    m.Calls = append(m.Calls, MockCall{Method: "Spec", Spec: s})
}
func (m *MockRenderer) Close() error { return nil }

// Reset clears recorded calls but keeps queued Confirm answers.
func (m *MockRenderer) Reset() { m.Calls = nil }

// MethodSequence returns just the method names — handy for sequence assertions.
func (m *MockRenderer) MethodSequence() []string {
    out := make([]string, len(m.Calls))
    for i, c := range m.Calls {
        out[i] = c.Method
    }
    return out
}
```

The `Calls` comparison in TestMock above expects fields zero-valued except those set; `State` is a struct so it'll be zero-valued in these calls. Adjust the test if `require.Equal` complains about zero-value State on Banner:
```go
{Method: "Banner", State: SessionState{DisplayName: "x"}},
```
(already in the test).

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/render/... -run TestMock -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/render/mock.go cli/internal/chat/render/mock_test.go
git commit -m "test(chat): P26 T5 — capture-mock Renderer for downstream tests"
```

---

## Task 6: Slash Command Parser + Dispatch Table

**Files:**
- Create: `cli/internal/chat/repl/slash.go`
- Create: `cli/internal/chat/repl/slash_test.go`

- [ ] **Step 1: Write failing parser tests**

Create `cli/internal/chat/repl/slash_test.go`:
```go
package repl

import (
    "testing"

    "github.com/stretchr/testify/require"
)

func TestParseInput_BarePrompt(t *testing.T) {
    in, cmd, args := ParseInput("add dark mode toggle")
    require.Equal(t, InputPrompt, in)
    require.Equal(t, "", cmd)
    require.Equal(t, "add dark mode toggle", args)
}

func TestParseInput_SlashWithArgs(t *testing.T) {
    in, cmd, args := ParseInput("/switch add-dark-0428")
    require.Equal(t, InputSlash, in)
    require.Equal(t, "switch", cmd)
    require.Equal(t, "add-dark-0428", args)
}

func TestParseInput_SlashNoArgs(t *testing.T) {
    in, cmd, args := ParseInput("/sessions")
    require.Equal(t, InputSlash, in)
    require.Equal(t, "sessions", cmd)
    require.Equal(t, "", args)
}

func TestParseInput_BlankIsBlank(t *testing.T) {
    in, cmd, args := ParseInput("   ")
    require.Equal(t, InputBlank, in)
    require.Equal(t, "", cmd)
    require.Equal(t, "", args)
}

func TestKnownSlash_HasV1Set(t *testing.T) {
    for _, name := range []string{
        "sessions", "switch", "new", "quit",
        "spec", "status", "diff", "merge",
        "run", "help",
    } {
        require.True(t, IsKnownSlash(name), "missing slash: /%s", name)
    }
    require.False(t, IsKnownSlash("interrupt"), "/interrupt is V1.1")
    require.False(t, IsKnownSlash("stop"), "/stop is V1.1")
    require.False(t, IsKnownSlash("bogus"))
}
```

- [ ] **Step 2: Run tests (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/...`
Expected: build error — undefined symbols.

- [ ] **Step 3: Implement parser + table**

Create `cli/internal/chat/repl/slash.go`:
```go
// Package repl is the chat surface REPL — single prompt loop, slash
// commands, and an EventStream consumer that drives the Renderer.
package repl

import "strings"

type InputKind int

const (
    InputBlank InputKind = iota
    InputPrompt
    InputSlash
)

// ParseInput classifies a single line of user input.
//
// Bare text → InputPrompt with args=line.
// "/cmd args..." → InputSlash with cmd=cmd, args=joined remainder.
// Whitespace-only → InputBlank.
func ParseInput(line string) (kind InputKind, cmd string, args string) {
    trimmed := strings.TrimSpace(line)
    if trimmed == "" {
        return InputBlank, "", ""
    }
    if !strings.HasPrefix(trimmed, "/") {
        return InputPrompt, "", trimmed
    }
    rest := strings.TrimPrefix(trimmed, "/")
    parts := strings.SplitN(rest, " ", 2)
    cmd = parts[0]
    if len(parts) == 2 {
        args = strings.TrimSpace(parts[1])
    }
    return InputSlash, cmd, args
}

// V1 slash command set. Update when adding V1.1 (interrupt/stop).
var v1Slash = map[string]bool{
    "sessions": true, "switch": true, "new": true, "quit": true,
    "spec": true, "status": true, "diff": true, "merge": true,
    "run": true, "help": true,
}

func IsKnownSlash(name string) bool { return v1Slash[name] }

// SlashRequiresSession reports whether a known slash command needs an
// active session in client state.
func SlashRequiresSession(name string) bool {
    switch name {
    case "spec", "status", "diff", "merge", "run":
        return true
    }
    return false
}
```

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -v`
Expected: 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/slash.go cli/internal/chat/repl/slash_test.go
git commit -m "feat(chat): P26 T6 — slash parser + V1 command table"
```

---

## Task 7: Phase Tracker (state.go)

**Files:**
- Create: `cli/internal/chat/repl/state.go`
- Create: `cli/internal/chat/repl/state_test.go`

- [ ] **Step 1: Write failing tests for the state machine**

Create `cli/internal/chat/repl/state_test.go`:
```go
package repl

import (
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeEvent mimics the relevant fields of sdk.Event we care about.
// We declare a minimal local enum so this test stays decoupled from
// the proto package while we wire the real types.
type fakeEvent struct {
    Kind        string
    SlotsFilled int
    SlotsTotal  int
    Saturation  float64
    AdvFindings int
    Iter        int
    MaxIter     int
    CostUSD     float64
    Status      string
    ChecksOK    int
    ChecksTot   int
}

func (f fakeEvent) ToTrackerInput() TrackerInput {
    return TrackerInput{
        Kind:         f.Kind,
        SlotsFilled:  f.SlotsFilled,
        SlotsTotal:   f.SlotsTotal,
        Saturation:   f.Saturation,
        AdvFindings:  f.AdvFindings,
        Iter:         f.Iter,
        MaxIter:      f.MaxIter,
        CostUSD:      f.CostUSD,
        Status:       f.Status,
        ChecksPassed: f.ChecksOK,
        ChecksTotal:  f.ChecksTot,
    }
}

func TestTracker_StartsIdle(t *testing.T) {
    tr := NewTracker()
    require.Equal(t, render.PhaseIdle, tr.State().Phase)
}

func TestTracker_InterviewSlotProgress(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "interview.slot_filled", SlotsFilled: 4, SlotsTotal: 11, Saturation: 0.36}.ToTrackerInput())
    s := tr.State()
    require.Equal(t, render.PhaseInterview, s.Phase)
    require.Equal(t, 4, s.SlotsFilled)
    require.Equal(t, 11, s.SlotsTotal)
    require.InDelta(t, 0.36, s.Saturation, 0.001)
}

func TestTracker_AdversaryFindingAccumulates(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "interview.adversary", AdvFindings: 1}.ToTrackerInput())
    require.Equal(t, 1, tr.State().AdvFindings)
    tr.Apply(fakeEvent{Kind: "interview.adversary", AdvFindings: 3}.ToTrackerInput())
    require.Equal(t, 3, tr.State().AdvFindings, "tracker overwrites with latest count")
}

func TestTracker_SaturationReadyTransitionsToAwaitingConfirm(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "interview.ready_to_freeze"}.ToTrackerInput())
    require.Equal(t, render.PhaseAwaitingConfirm, tr.State().Phase)
}

func TestTracker_RunStartsAndIters(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "run.started", MaxIter: 100}.ToTrackerInput())
    require.Equal(t, render.PhaseRun, tr.State().Phase)
    require.Equal(t, 100, tr.State().MaxIter)

    tr.Apply(fakeEvent{Kind: "run.iter", Iter: 23, CostUSD: 0.61}.ToTrackerInput())
    s := tr.State()
    require.Equal(t, 23, s.Iter)
    require.InDelta(t, 0.61, s.CostUSD, 0.001)
}

func TestTracker_StuckSignal(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "run.started", MaxIter: 100}.ToTrackerInput())
    tr.Apply(fakeEvent{Kind: "run.stuck", Iter: 45, MaxIter: 100}.ToTrackerInput())
    require.Equal(t, render.PhaseStuck, tr.State().Phase)
}

func TestTracker_DoneWithChecks(t *testing.T) {
    tr := NewTracker()
    tr.Apply(fakeEvent{Kind: "run.done", Iter: 87, CostUSD: 2.34, ChecksOK: 4, ChecksTot: 4}.ToTrackerInput())
    s := tr.State()
    require.Equal(t, render.PhaseDone, s.Phase)
    require.Equal(t, 4, s.ChecksPassed)
    require.Equal(t, 4, s.ChecksTotal)
}
```

- [ ] **Step 2: Run tests (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -run TestTracker`
Expected: build error.

- [ ] **Step 3: Implement Tracker**

Create `cli/internal/chat/repl/state.go`:
```go
package repl

import "github.com/mindungil/gil/cli/internal/chat/render"

// TrackerInput is the renderer-agnostic shape of a single event the
// tracker consumes. The REPL adapts sdk.Event → TrackerInput at the
// gRPC boundary (Task 8) so this module stays free of proto deps and
// is trivially unit-testable.
type TrackerInput struct {
    Kind         string
    SessionID    string
    DisplayName  string
    SlotsFilled  int
    SlotsTotal   int
    Saturation   float64
    AdvFindings  int
    Iter         int
    MaxIter      int
    CostUSD      float64
    Status       string
    ChecksPassed int
    ChecksTotal  int
    Autonomy     string
}

type Tracker struct {
    s render.SessionState
}

func NewTracker() *Tracker {
    return &Tracker{s: render.SessionState{Phase: render.PhaseIdle}}
}

func (t *Tracker) State() render.SessionState { return t.s }

// Apply mutates state in-place based on event kind. The kind strings
// are gil-internal event names; if Step 0 audit added new event types,
// extend this switch.
func (t *Tracker) Apply(in TrackerInput) {
    if in.SessionID != "" {
        t.s.SessionID = in.SessionID
    }
    if in.DisplayName != "" {
        t.s.DisplayName = in.DisplayName
    }
    if in.Autonomy != "" {
        t.s.Autonomy = in.Autonomy
    }

    switch in.Kind {
    case "interview.slot_filled":
        t.s.Phase = render.PhaseInterview
        t.s.SlotsFilled = in.SlotsFilled
        t.s.SlotsTotal = in.SlotsTotal
        t.s.Saturation = in.Saturation

    case "interview.adversary":
        // Phase stays whatever it was; only update count.
        t.s.AdvFindings = in.AdvFindings

    case "interview.ready_to_freeze":
        t.s.Phase = render.PhaseAwaitingConfirm

    case "run.started":
        t.s.Phase = render.PhaseRun
        t.s.Iter = 0
        if in.MaxIter > 0 {
            t.s.MaxIter = in.MaxIter
        }

    case "run.iter":
        t.s.Phase = render.PhaseRun
        t.s.Iter = in.Iter
        if in.CostUSD > 0 {
            t.s.CostUSD = in.CostUSD
        }

    case "run.stuck":
        t.s.Phase = render.PhaseStuck
        if in.Iter > 0 {
            t.s.Iter = in.Iter
        }
        if in.MaxIter > 0 {
            t.s.MaxIter = in.MaxIter
        }

    case "run.done":
        t.s.Phase = render.PhaseDone
        if in.Iter > 0 {
            t.s.Iter = in.Iter
        }
        if in.CostUSD > 0 {
            t.s.CostUSD = in.CostUSD
        }
        t.s.ChecksPassed = in.ChecksPassed
        t.s.ChecksTotal = in.ChecksTotal
    }
}
```

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -run TestTracker -v`
Expected: 7 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/state.go cli/internal/chat/repl/state_test.go
git commit -m "feat(chat): P26 T7 — phase tracker (idle/interview/awaiting-confirm/run/stuck/done)"
```

---

## Task 8: REPL Loop Skeleton

**Files:**
- Create: `cli/internal/chat/repl/loop.go`
- Create: `cli/internal/chat/repl/loop_test.go`

This task wires the REPL together with a fake session client. We
isolate the network boundary behind a `SessionClient` interface so
loop tests don't need a live daemon.

- [ ] **Step 1: Write failing test using mock renderer + fake client**

Create `cli/internal/chat/repl/loop_test.go`:
```go
package repl

import (
    "context"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeClient simulates the SessionClient interface. Tests pre-load
// AssistantText chunks the loop should emit and event sequences the
// tracker should apply.
type fakeClient struct {
    assistantChunks []string
    events          []TrackerInput
    sentPrompts     []string
}

func (f *fakeClient) SendPrompt(_ context.Context, prompt string) error {
    f.sentPrompts = append(f.sentPrompts, prompt)
    return nil
}
func (f *fakeClient) NextAssistantChunk(_ context.Context) (string, bool, error) {
    if len(f.assistantChunks) == 0 {
        return "", false, nil
    }
    c := f.assistantChunks[0]
    f.assistantChunks = f.assistantChunks[1:]
    return c, len(f.assistantChunks) > 0, nil
}
func (f *fakeClient) NextEvent(_ context.Context) (TrackerInput, bool, error) {
    if len(f.events) == 0 {
        return TrackerInput{}, false, nil
    }
    e := f.events[0]
    f.events = f.events[1:]
    return e, true, nil
}
func (f *fakeClient) Close() error { return nil }

func TestLoop_BarePrompt_SendsAndRendersAssistant(t *testing.T) {
    mock := render.NewMockRenderer()
    fc := &fakeClient{
        assistantChunks: []string{"hello ", "world"},
    }
    in := strings.NewReader("hi there\n/quit\n")

    err := Run(context.Background(), Config{
        In:       in,
        Renderer: mock,
        Client:   fc,
    })
    require.NoError(t, err)

    require.Equal(t, []string{"hi there"}, fc.sentPrompts)
    seq := mock.MethodSequence()
    require.Contains(t, seq, "AssistantText")
    require.Contains(t, seq, "PromptCue")
}

func TestLoop_SlashHelp_RoutesToHelp(t *testing.T) {
    mock := render.NewMockRenderer()
    in := strings.NewReader("/help\n/quit\n")
    err := Run(context.Background(), Config{
        In:       in,
        Renderer: mock,
        Client:   &fakeClient{},
    })
    require.NoError(t, err)
    // /help should emit at least one SystemNote listing commands.
    var foundHelp bool
    for _, c := range mock.Calls {
        if c.Method == "SystemNote" && strings.Contains(c.Text, "/sessions") {
            foundHelp = true
            break
        }
    }
    require.True(t, foundHelp, "expected /help to emit a SystemNote listing slash commands")
}

func TestLoop_UnknownSlash_EmitsHint(t *testing.T) {
    mock := render.NewMockRenderer()
    in := strings.NewReader("/bogus\n/quit\n")
    err := Run(context.Background(), Config{
        In:       in,
        Renderer: mock,
        Client:   &fakeClient{},
    })
    require.NoError(t, err)
    var foundHint bool
    for _, c := range mock.Calls {
        if c.Method == "SystemNote" && strings.Contains(c.Text, "unknown") {
            foundHint = true
            break
        }
    }
    require.True(t, foundHint)
}

func TestLoop_SessionScopedSlashWithoutSession_Errors(t *testing.T) {
    mock := render.NewMockRenderer()
    in := strings.NewReader("/spec\n/quit\n")
    err := Run(context.Background(), Config{
        In:       in,
        Renderer: mock,
        Client:   &fakeClient{},
    })
    require.NoError(t, err)
    var found bool
    for _, c := range mock.Calls {
        if c.Method == "SystemNote" && strings.Contains(c.Text, "no active session") {
            found = true
            break
        }
    }
    require.True(t, found)
}
```

- [ ] **Step 2: Run tests (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -run TestLoop`
Expected: build error — `undefined: Run / Config / SessionClient`.

- [ ] **Step 3: Implement loop skeleton**

Create `cli/internal/chat/repl/loop.go`:
```go
package repl

import (
    "bufio"
    "context"
    "fmt"
    "io"

    "github.com/mindungil/gil/cli/internal/chat/render"
)

// SessionClient abstracts the gRPC session boundary so loop tests
// don't need a live daemon. The cmd/chat.go integration provides a
// real implementation backed by sdk.Client.
type SessionClient interface {
    SendPrompt(ctx context.Context, prompt string) error
    NextAssistantChunk(ctx context.Context) (chunk string, more bool, err error)
    NextEvent(ctx context.Context) (in TrackerInput, ok bool, err error)
    Close() error
}

type Config struct {
    In       io.Reader
    Renderer render.Renderer
    Client   SessionClient
}

// Run executes the chat REPL until the user types /quit, EOF, or an
// unrecoverable client error.
func Run(ctx context.Context, cfg Config) error {
    if cfg.Renderer == nil {
        return fmt.Errorf("repl.Run: Renderer required")
    }
    tr := NewTracker()
    cfg.Renderer.Banner(tr.State())

    scanner := bufio.NewScanner(cfg.In)
    for {
        // Drain any pending events into the tracker before drawing strip.
        drainEvents(ctx, cfg, tr)

        cfg.Renderer.StatusStrip(tr.State())
        cfg.Renderer.PromptCue()

        if !scanner.Scan() {
            return nil // EOF
        }
        line := scanner.Text()

        kind, cmd, args := ParseInput(line)
        switch kind {
        case InputBlank:
            continue

        case InputSlash:
            if !IsKnownSlash(cmd) {
                cfg.Renderer.SystemNote(render.NoteSystem,
                    fmt.Sprintf("unknown slash: /%s — try /help", cmd))
                continue
            }
            if cmd == "quit" {
                return nil
            }
            if SlashRequiresSession(cmd) && tr.State().SessionID == "" {
                cfg.Renderer.SystemNote(render.NoteSystem,
                    "no active session — start one with a prompt or /sessions")
                continue
            }
            if err := dispatchSlash(ctx, cfg, tr, cmd, args); err != nil {
                cfg.Renderer.SystemNote(render.NoteSystem,
                    fmt.Sprintf("/%s failed: %v", cmd, err))
            }

        case InputPrompt:
            if err := cfg.Client.SendPrompt(ctx, args); err != nil {
                cfg.Renderer.SystemNote(render.NoteSystem,
                    fmt.Sprintf("send failed: %v", err))
                continue
            }
            // Stream assistant chunks until the client signals done.
            for {
                chunk, more, err := cfg.Client.NextAssistantChunk(ctx)
                if err != nil {
                    cfg.Renderer.SystemNote(render.NoteSystem,
                        fmt.Sprintf("stream error: %v", err))
                    break
                }
                if chunk != "" {
                    cfg.Renderer.AssistantText(chunk)
                }
                if !more {
                    break
                }
            }
            cfg.Renderer.AssistantText("\n")
        }
    }
}

func drainEvents(ctx context.Context, cfg Config, tr *Tracker) {
    for {
        in, ok, err := cfg.Client.NextEvent(ctx)
        if err != nil || !ok {
            return
        }
        prev := tr.State()
        tr.Apply(in)
        emitDeltaNotes(cfg.Renderer, prev, tr.State(), in)
    }
}

// emitDeltaNotes turns tracker state changes into one-line system
// notes so the user sees what shifted between strips.
func emitDeltaNotes(r render.Renderer, prev, cur render.SessionState, ev TrackerInput) {
    switch ev.Kind {
    case "interview.slot_filled":
        if cur.SlotsFilled > prev.SlotsFilled {
            r.SystemNote(render.NoteSpec,
                fmt.Sprintf("slot filled (%d/%d, sat %d%%)",
                    cur.SlotsFilled, cur.SlotsTotal, int(cur.Saturation*100+0.5)))
        }
    case "interview.adversary":
        if cur.AdvFindings != prev.AdvFindings {
            r.SystemNote(render.NoteAdversary,
                fmt.Sprintf("%d finding(s)", cur.AdvFindings))
        }
    case "interview.ready_to_freeze":
        r.SystemNote(render.NoteSaturation, "ready to freeze — /run to start")
    case "run.stuck":
        r.SystemNote(render.NoteSystem,
            "stuck after recovery — V1.1 will offer /interrupt; for now `gil stop <id>` from another shell")
    case "run.done":
        r.SystemNote(render.NoteSystem, "done — /diff to review, /merge to apply")
    }
}

// dispatchSlash is a stub at this stage; Task 9 fills in real
// behaviors. We return nil so unknown-but-valid slashes don't crash.
func dispatchSlash(_ context.Context, cfg Config, _ *Tracker, cmd, _ string) error {
    switch cmd {
    case "help":
        cfg.Renderer.SystemNote(render.NoteSystem,
            "slash commands: /sessions /switch /new /spec /status /diff /merge /run /quit /help")
    }
    return nil
}
```

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -v`
Expected: TestLoop_* tests PASS plus all earlier tests.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/loop.go cli/internal/chat/repl/loop_test.go
git commit -m "feat(chat): P26 T8 — REPL loop skeleton + SessionClient interface"
```

---

## Task 9: REPL — Slash Dispatch (real handlers)

**Files:**
- Modify: `cli/internal/chat/repl/loop.go`
- Modify: `cli/internal/chat/repl/loop_test.go`

Wire each session-bound slash command to a thin SessionClient call.
Extend the SessionClient interface with the calls we need.

- [ ] **Step 1: Extend tests to cover dispatch behaviors**

Append to `cli/internal/chat/repl/loop_test.go`:
```go
func TestLoop_SlashSpec_RendersSpec(t *testing.T) {
    mock := render.NewMockRenderer()
    fc := &fakeClient{
        spec: &render.SpecView{YAML: "goal:\n  one_liner: x\n"},
        sessionID: "01HQ",
    }
    in := strings.NewReader("/spec\n/quit\n")
    require.NoError(t, Run(context.Background(), Config{
        In: in, Renderer: mock, Client: fc,
    }))
    var found bool
    for _, c := range mock.Calls {
        if c.Method == "Spec" && c.Spec != nil && strings.Contains(c.Spec.YAML, "one_liner") {
            found = true
        }
    }
    require.True(t, found)
}

func TestLoop_SlashDiff_RendersHunks(t *testing.T) {
    mock := render.NewMockRenderer()
    fc := &fakeClient{
        diffHunks: []render.DiffHunk{{Path: "a.go", Added: 3, Removed: 1}},
        sessionID: "01HQ",
    }
    in := strings.NewReader("/diff\n/quit\n")
    require.NoError(t, Run(context.Background(), Config{
        In: in, Renderer: mock, Client: fc,
    }))
    var found bool
    for _, c := range mock.Calls {
        if c.Method == "Diff" && len(c.Hunks) == 1 && c.Hunks[0].Path == "a.go" {
            found = true
        }
    }
    require.True(t, found)
}

func TestLoop_SlashMerge_PromptsConfirm(t *testing.T) {
    mock := render.NewMockRenderer()
    mock.ConfirmAnswers = []bool{true}
    fc := &fakeClient{sessionID: "01HQ"}
    in := strings.NewReader("/merge\n/quit\n")
    require.NoError(t, Run(context.Background(), Config{
        In: in, Renderer: mock, Client: fc,
    }))
    var foundConfirm bool
    for _, c := range mock.Calls {
        if c.Method == "Confirm" && strings.Contains(c.Text, "Apply") {
            foundConfirm = true
        }
    }
    require.True(t, foundConfirm)
    require.True(t, fc.merged, "client.Merge should have been called after Y")
}

func TestLoop_SlashRun_RequiresAwaitingConfirmPhase(t *testing.T) {
    mock := render.NewMockRenderer()
    fc := &fakeClient{sessionID: "01HQ"}
    in := strings.NewReader("/run\n/quit\n")
    require.NoError(t, Run(context.Background(), Config{
        In: in, Renderer: mock, Client: fc,
    }))
    // Phase is idle (no awaiting-confirm event), so /run should refuse.
    var found bool
    for _, c := range mock.Calls {
        if c.Method == "SystemNote" && strings.Contains(c.Text, "spec is not ready") {
            found = true
        }
    }
    require.True(t, found)
    require.False(t, fc.runStarted)
}
```

Update `fakeClient` to back the new SessionClient methods:
```go
type fakeClient struct {
    assistantChunks []string
    events          []TrackerInput
    sentPrompts     []string
    sessionID       string
    spec            *render.SpecView
    statusText      string
    diffHunks       []render.DiffHunk
    merged          bool
    runStarted      bool
}

func (f *fakeClient) ActiveSessionID() string                                   { return f.sessionID }
func (f *fakeClient) Spec(_ context.Context) (*render.SpecView, error)          { return f.spec, nil }
func (f *fakeClient) Status(_ context.Context) (string, error)                  { return f.statusText, nil }
func (f *fakeClient) Diff(_ context.Context) ([]render.DiffHunk, error)         { return f.diffHunks, nil }
func (f *fakeClient) Merge(_ context.Context) error                             { f.merged = true; return nil }
func (f *fakeClient) StartRun(_ context.Context) error                          { f.runStarted = true; return nil }
func (f *fakeClient) ListSessions(_ context.Context) ([]SessionSummary, error)  { return nil, nil }
func (f *fakeClient) SwitchSession(_ context.Context, _ string) error           { f.sessionID = "switched"; return nil }
func (f *fakeClient) NewSession(_ context.Context) error                        { f.sessionID = "new"; return nil }
```

- [ ] **Step 2: Run tests (expect fail — interface methods missing)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/...`
Expected: build error — undefined SessionSummary / interface methods.

- [ ] **Step 3: Extend SessionClient + dispatchSlash**

Update `cli/internal/chat/repl/loop.go`. Replace the `SessionClient` interface and `dispatchSlash`:
```go
type SessionSummary struct {
    ID, Name, Phase string
}

type SessionClient interface {
    SendPrompt(ctx context.Context, prompt string) error
    NextAssistantChunk(ctx context.Context) (chunk string, more bool, err error)
    NextEvent(ctx context.Context) (in TrackerInput, ok bool, err error)

    ActiveSessionID() string
    Spec(ctx context.Context) (*render.SpecView, error)
    Status(ctx context.Context) (string, error)
    Diff(ctx context.Context) ([]render.DiffHunk, error)
    Merge(ctx context.Context) error
    StartRun(ctx context.Context) error
    ListSessions(ctx context.Context) ([]SessionSummary, error)
    SwitchSession(ctx context.Context, idOrName string) error
    NewSession(ctx context.Context) error

    Close() error
}

func dispatchSlash(ctx context.Context, cfg Config, tr *Tracker, cmd, args string) error {
    r := cfg.Renderer
    c := cfg.Client
    switch cmd {
    case "help":
        r.SystemNote(render.NoteSystem,
            "slash commands: /sessions /switch /new /spec /status /diff /merge /run /quit /help")
    case "sessions":
        list, err := c.ListSessions(ctx)
        if err != nil {
            return err
        }
        if len(list) == 0 {
            r.SystemNote(render.NoteSystem, "no sessions — type a prompt to start one")
            return nil
        }
        for i, s := range list {
            r.SystemNote(render.NoteSystem,
                fmt.Sprintf("%d. %s  %s  [%s]", i+1, s.ID[:6], s.Name, s.Phase))
        }
    case "switch":
        if args == "" {
            r.SystemNote(render.NoteSystem, "/switch <id|name>")
            return nil
        }
        return c.SwitchSession(ctx, args)
    case "new":
        return c.NewSession(ctx)
    case "spec":
        v, err := c.Spec(ctx)
        if err != nil {
            return err
        }
        r.Spec(v)
    case "status":
        s, err := c.Status(ctx)
        if err != nil {
            return err
        }
        r.SystemNote(render.NoteSystem, s)
    case "diff":
        h, err := c.Diff(ctx)
        if err != nil {
            return err
        }
        r.Diff(h)
    case "merge":
        ok, err := r.Confirm("Apply diff to working tree?", false)
        if err != nil || !ok {
            return err
        }
        return c.Merge(ctx)
    case "run":
        if tr.State().Phase != render.PhaseAwaitingConfirm {
            r.SystemNote(render.NoteSystem,
                "spec is not ready to freeze yet — keep iterating with prompts")
            return nil
        }
        return c.StartRun(ctx)
    }
    return nil
}
```

Also update the existing `fakeClient` test stub to include the new methods (already done in Step 1).

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -v`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/loop.go cli/internal/chat/repl/loop_test.go
git commit -m "feat(chat): P26 T9 — slash dispatch (sessions/switch/new/spec/status/diff/merge/run)"
```

---

## Task 10: REPL — Run-Phase Prompt Echo (V1.1 placeholder)

**Files:**
- Modify: `cli/internal/chat/repl/loop.go`
- Modify: `cli/internal/chat/repl/loop_test.go`

When a run is active, bare prompts must NOT be sent to the model
(no agent input channel exists in V1). Echo a V1.1 placeholder note.

- [ ] **Step 1: Add failing test**

Append to `cli/internal/chat/repl/loop_test.go`:
```go
func TestLoop_RunPhase_PromptEchoesV11(t *testing.T) {
    mock := render.NewMockRenderer()
    fc := &fakeClient{
        sessionID: "01HQ",
        events: []TrackerInput{
            {Kind: "run.started", MaxIter: 100, SessionID: "01HQ"},
        },
    }
    in := strings.NewReader("hey, also add a tooltip\n/quit\n")
    require.NoError(t, Run(context.Background(), Config{
        In: in, Renderer: mock, Client: fc,
    }))
    require.Empty(t, fc.sentPrompts, "run-phase prompt must not be sent in V1")
    var foundEcho bool
    for _, c := range mock.Calls {
        if c.Method == "SystemNote" && c.Kind == render.NoteV11 {
            foundEcho = true
        }
    }
    require.True(t, foundEcho)
}
```

- [ ] **Step 2: Run test (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -run TestLoop_RunPhase`
Expected: FAIL — sentPrompts contains the prompt.

- [ ] **Step 3: Guard the prompt path**

In `loop.go`, modify the `InputPrompt` case in `Run`:
```go
case InputPrompt:
    if tr.State().Phase == render.PhaseRun || tr.State().Phase == render.PhaseStuck {
        cfg.Renderer.SystemNote(render.NoteV11,
            "run-time prompts are V1.1; for now wait for done, or `gil stop <id>` from another shell")
        continue
    }
    if err := cfg.Client.SendPrompt(ctx, args); err != nil {
        cfg.Renderer.SystemNote(render.NoteSystem,
            fmt.Sprintf("send failed: %v", err))
        continue
    }
    // ... existing chunk-stream loop unchanged
```

- [ ] **Step 4: Run test (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/chat/repl/... -run TestLoop_RunPhase -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/loop.go cli/internal/chat/repl/loop_test.go
git commit -m "feat(chat): P26 T10 — run-phase prompt echoes V1.1 placeholder"
```

---

## Task 11: Real SessionClient (gRPC adapter)

**Files:**
- Create: `cli/internal/chat/repl/grpc_client.go`

This is the production adapter implementing `SessionClient` against
the existing `sdk.Client`. Tests live at the integration level (Task
14) since unit tests for thin adapters are low-value.

- [ ] **Step 1: Write the adapter**

Create `cli/internal/chat/repl/grpc_client.go`:
```go
package repl

import (
    "context"
    "fmt"

    "github.com/mindungil/gil/cli/internal/chat/render"
    "github.com/mindungil/gil/sdk"
)

// GRPCClient adapts sdk.Client to the SessionClient interface.
type GRPCClient struct {
    sdk         *sdk.Client
    activeSess  string
    workingDir  string

    // Channels populated by the bidi event stream goroutine.
    chunkCh chan string
    chunkDone chan struct{}
    eventCh chan TrackerInput
}

func NewGRPCClient(s *sdk.Client, workingDir string) *GRPCClient {
    return &GRPCClient{
        sdk:        s,
        workingDir: workingDir,
        chunkCh:    make(chan string, 64),
        chunkDone:  make(chan struct{}, 1),
        eventCh:    make(chan TrackerInput, 64),
    }
}

func (g *GRPCClient) ActiveSessionID() string { return g.activeSess }

func (g *GRPCClient) NewSession(ctx context.Context) error {
    sess, err := g.sdk.SessionCreate(ctx, sdk.CreateOptions{WorkingDir: g.workingDir})
    if err != nil {
        return err
    }
    g.activeSess = sess.ID
    return g.subscribe(ctx)
}

func (g *GRPCClient) SwitchSession(ctx context.Context, idOrName string) error {
    sess, err := g.sdk.SessionResolve(ctx, idOrName)
    if err != nil {
        return err
    }
    g.activeSess = sess.ID
    return g.subscribe(ctx)
}

func (g *GRPCClient) ListSessions(ctx context.Context) ([]SessionSummary, error) {
    list, err := g.sdk.SessionList(ctx)
    if err != nil {
        return nil, err
    }
    out := make([]SessionSummary, 0, len(list))
    for _, s := range list {
        out = append(out, SessionSummary{
            ID: s.ID, Name: displayNameOrShort(s), Phase: s.Status,
        })
    }
    return out, nil
}

func (g *GRPCClient) SendPrompt(ctx context.Context, prompt string) error {
    if g.activeSess == "" {
        // Auto-create on first prompt.
        if err := g.NewSession(ctx); err != nil {
            return err
        }
    }
    return g.sdk.InterviewReply(ctx, g.activeSess, prompt)
}

func (g *GRPCClient) NextAssistantChunk(ctx context.Context) (string, bool, error) {
    select {
    case <-ctx.Done():
        return "", false, ctx.Err()
    case <-g.chunkDone:
        return "", false, nil
    case chunk := <-g.chunkCh:
        // Peek if more is buffered without blocking.
        more := len(g.chunkCh) > 0
        return chunk, more, nil
    }
}

func (g *GRPCClient) NextEvent(ctx context.Context) (TrackerInput, bool, error) {
    select {
    case <-ctx.Done():
        return TrackerInput{}, false, ctx.Err()
    case ev := <-g.eventCh:
        return ev, true, nil
    default:
        return TrackerInput{}, false, nil
    }
}

func (g *GRPCClient) Spec(ctx context.Context) (*render.SpecView, error) {
    yaml, err := g.sdk.SpecYAML(ctx, g.activeSess)
    if err != nil {
        return nil, err
    }
    return &render.SpecView{YAML: yaml}, nil
}

func (g *GRPCClient) Status(ctx context.Context) (string, error) {
    s, err := g.sdk.SessionGet(ctx, g.activeSess)
    if err != nil {
        return "", err
    }
    return fmt.Sprintf("%s · %s · iter %d/%d · $%.2f",
        s.ID[:6], s.Status, s.CurrentIteration, s.MaxIterations, s.TotalCostUSD), nil
}

func (g *GRPCClient) Diff(ctx context.Context) ([]render.DiffHunk, error) {
    raw, err := g.sdk.SessionDiff(ctx, g.activeSess)
    if err != nil {
        return nil, err
    }
    out := make([]render.DiffHunk, 0, len(raw))
    for _, h := range raw {
        out = append(out, render.DiffHunk{
            Path: h.Path, Added: h.Added, Removed: h.Removed,
        })
    }
    return out, nil
}

func (g *GRPCClient) Merge(ctx context.Context) error {
    return g.sdk.SessionMerge(ctx, g.activeSess)
}

func (g *GRPCClient) StartRun(ctx context.Context) error {
    return g.sdk.RunStart(ctx, g.activeSess, sdk.RunOptions{})
}

func (g *GRPCClient) Close() error {
    if g.sdk == nil {
        return nil
    }
    return g.sdk.Close()
}

func (g *GRPCClient) subscribe(ctx context.Context) error {
    stream, err := g.sdk.EventsSubscribe(ctx, g.activeSess)
    if err != nil {
        return err
    }
    go func() {
        defer close(g.chunkDone)
        for {
            ev, err := stream.Recv()
            if err != nil {
                return
            }
            switch v := ev.GetPayload().(type) {
            case *sdk.AssistantChunk:
                g.chunkCh <- v.Text
            default:
                in := mapEventToTracker(ev)
                if in.Kind != "" {
                    g.eventCh <- in
                }
            }
        }
    }()
    return nil
}

func mapEventToTracker(ev *sdk.Event) TrackerInput {
    // The exact mapping depends on the proto message catalog. Step 0
    // audit produced a list of available events; this function is the
    // single point that owns the proto→TrackerInput translation.
    // Keep it small and switch-based.
    in := TrackerInput{SessionID: ev.SessionID}
    switch ev.Kind {
    case "interview.slot_filled":
        in.Kind = "interview.slot_filled"
        in.SlotsFilled = int(ev.GetSlot().Filled)
        in.SlotsTotal = int(ev.GetSlot().Total)
        in.Saturation = ev.GetSlot().Saturation
    case "interview.adversary":
        in.Kind = "interview.adversary"
        in.AdvFindings = int(ev.GetAdversary().FindingCount)
    case "interview.ready_to_freeze":
        in.Kind = "interview.ready_to_freeze"
    case "run.started":
        in.Kind = "run.started"
        in.MaxIter = int(ev.GetRun().MaxIter)
    case "run.iter":
        in.Kind = "run.iter"
        in.Iter = int(ev.GetRun().Iter)
        in.CostUSD = ev.GetRun().CostUSD
    case "run.stuck":
        in.Kind = "run.stuck"
    case "run.done":
        in.Kind = "run.done"
        in.Iter = int(ev.GetRun().Iter)
        in.CostUSD = ev.GetRun().CostUSD
        in.ChecksPassed = int(ev.GetRun().ChecksPassed)
        in.ChecksTotal = int(ev.GetRun().ChecksTotal)
    }
    return in
}

// displayNameOrShort mirrors cmd.displayName but lives here to avoid
// import cycles. Keep in sync with summary.go displayName().
func displayNameOrShort(s *sdk.Session) string {
    if s == nil {
        return ""
    }
    if len(s.ID) >= 6 {
        return s.ID[:6]
    }
    return s.ID
}
```

> **Note:** The exact `sdk.Event` payload accessor names (`GetSlot`, `GetAdversary`, `GetRun`) depend on the proto. Verify against the actual proto-generated code; rename accessors here if they differ. If Step 0 audit found missing events, this switch shrinks accordingly.

- [ ] **Step 2: Build to verify it compiles against current sdk**

Run: `cd /home/ubuntu/gil && go build ./cli/internal/chat/repl/...`
Expected: build PASS. If accessor names mismatch, fix `mapEventToTracker` to use the real proto getters.

- [ ] **Step 3: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/chat/repl/grpc_client.go
git commit -m "feat(chat): P26 T11 — gRPC SessionClient adapter"
```

---

## Task 12: chat.go Integration — Replace Intent Classifier

**Files:**
- Modify: `cli/internal/cmd/chat.go`

- [ ] **Step 1: Read current chat.go to identify the entry point**

Run: `grep -n 'func runChat\|classifyIntent\|intent\\.' /home/ubuntu/gil/cli/internal/cmd/chat.go`
Capture line numbers of `runChat`, the intent classifier call site, and the intent-branch dispatch (NEW_TASK / RESUME / STATUS / HELP / EXPLAIN).

- [ ] **Step 2: Replace post-onboarding body with REPL invocation**

In `cli/internal/cmd/chat.go`, locate the section AFTER `detectPreDaemonState` (which stays — Phase 25 onboarding gate) and BEFORE the intent-classifier loop. Replace from "post-onboarding intent classifier" through the end of `runChat` with:

```go
    // P26: chat surface is a single prompt loop. The intent classifier,
    // verb-routing, and per-stage branches that lived here have been
    // moved to slash commands inside repl.Run.
    sdkClient, err := newSDKClient(cmd)
    if err != nil {
        return err
    }
    defer sdkClient.Close()

    workingDir, _ := cmd.Flags().GetString("working-dir")
    grpc := repl.NewGRPCClient(sdkClient, workingDir)

    asciiMode, _ := cmd.Flags().GetBool("ascii")
    noColor := os.Getenv("NO_COLOR") != ""
    renderer := render.NewStdoutChatRenderer(out, in, asciiMode, noColor)
    defer renderer.Close()

    return repl.Run(cmd.Context(), repl.Config{
        In:       in,
        Renderer: renderer,
        Client:   grpc,
    })
```

Add imports at the top of the file:
```go
"github.com/mindungil/gil/cli/internal/chat/render"
"github.com/mindungil/gil/cli/internal/chat/repl"
```

Delete the now-unused `intent.Classify(...)` call site, the NEW_TASK / RESUME / STATUS / HELP / EXPLAIN branches, and the helpers that only those branches called. **Do not** delete `detectPreDaemonState` or `runOnboardingNoInit` / `runOnboardingNoCreds` — those stay.

- [ ] **Step 3: Build (expect failures pointing at deleted-but-still-imported helpers)**

Run: `cd /home/ubuntu/gil && go build ./cli/internal/cmd/...`
Expected: errors about unused imports (intent package) or unused functions. Remove them.

- [ ] **Step 4: Build clean**

Run: `cd /home/ubuntu/gil && go build ./cli/internal/cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/cmd/chat.go
git commit -m "refactor(cli): P26 T12 — chat.go calls repl.Run; intent classifier removed"
```

---

## Task 13: chat_test.go Rewrite

**Files:**
- Modify: `cli/internal/cmd/chat_test.go`

The old tests asserted on stdout text produced by the intent classifier
and per-stage branches. Those branches are gone. New tests must assert
that `runChat` correctly hands off to `repl.Run` with the right inputs.

- [ ] **Step 1: Survey current tests**

Run: `grep -n '^func Test' /home/ubuntu/gil/cli/internal/cmd/chat_test.go`
Capture the test list. Each test that asserted on intent-classifier output is now obsolete.

- [ ] **Step 2: Delete obsolete tests, keep onboarding tests**

Open `cli/internal/cmd/chat_test.go`. Remove:
- TestClassifyIntent_* (entire functions)
- TestRenderChatStatus (already updated in P25 A3 — may need second pass)
- TestRenderChatHelp_* (the chat-side help is now /help slash; helper is gone)
- TestRunChat_NewTaskBranch / TestRunChat_ResumeBranch / TestRunChat_StatusBranch (intent branches removed)

Keep:
- TestRunChat_NoInitGate (Phase 25 S3, still valid)
- TestRunChat_NoCredsGate (Phase 25 S3, still valid)

- [ ] **Step 3: Add a new integration test that exercises chat→repl handoff**

Append:
```go
func TestRunChat_HandsOffToREPL_AtSessionReady(t *testing.T) {
    home := withGilHomeForOnboard(t) // existing P25 helper
    seedConfigAndCreds(t, home)      // helper that satisfies hasInit+hasAnyCred
    seedOneSession(t, home)          // helper that satisfies "≥1 session"

    cmd := newRootCmd()
    cmd.SetArgs([]string{"chat"})
    var out bytes.Buffer
    cmd.SetOut(&out)
    cmd.SetIn(strings.NewReader("/quit\n")) // immediate quit so REPL exits

    require.NoError(t, cmd.Execute())
    // Banner must appear (proves repl.Run was reached, not an old branch).
    require.Contains(t, out.String(), "gil")
}
```

If `seedConfigAndCreds` / `seedOneSession` helpers don't exist, write
minimal versions that touch the right files under `home`.

- [ ] **Step 4: Run all cli tests**

Run: `cd /home/ubuntu/gil && go test ./cli/...`
Expected: PASS. If a failure references a deleted intent helper, finish removing it.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/cmd/chat_test.go
git commit -m "test(cli): P26 T13 — chat_test.go reflects REPL handoff"
```

---

## Task 14: root.go — Verb Cobra Grouping

**Files:**
- Modify: `cli/internal/cmd/root.go`

P25 A2 added `cobra.Group` infrastructure (5 groups: setup / session /
diag / tools / maint). Add one more group "advanced" and re-tag the
verbs that should be hidden behind it.

- [ ] **Step 1: Write a failing test for the grouping**

Create `cli/internal/cmd/root_groups_test.go`:
```go
package cmd

import (
    "testing"

    "github.com/stretchr/testify/require"
)

func TestRoot_HasAdvancedGroup(t *testing.T) {
    root := newRootCmd()
    var found bool
    for _, g := range root.Groups() {
        if g.ID == "advanced" {
            found = true
        }
    }
    require.True(t, found, "expected 'advanced' cobra group registered")
}

func TestRoot_VerbsHaveGroupID(t *testing.T) {
    root := newRootCmd()
    expectAdvanced := []string{
        "interview", "run", "spec", "watch", "events", "stop",
        "fork", "import", "export", "stats", "merge", "diff",
    }
    advancedSet := map[string]bool{}
    for _, c := range root.Commands() {
        if c.GroupID == "advanced" {
            advancedSet[c.Use] = true
        }
    }
    for _, name := range expectAdvanced {
        // Use prefix-match because Use can be "interview <id>".
        var found bool
        for k := range advancedSet {
            if k == name || strings.HasPrefix(k, name+" ") {
                found = true
            }
        }
        require.True(t, found, "verb %q should be GroupID=advanced", name)
    }
}
```
Add `"strings"` import.

- [ ] **Step 2: Run test (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/cmd/... -run TestRoot_`
Expected: FAIL — group not registered or verbs lack GroupID.

- [ ] **Step 3: Register group + tag verbs**

In `cli/internal/cmd/root.go`, locate the existing `root.AddGroup(...)` calls (Phase 25 A2). Append:
```go
root.AddGroup(&cobra.Group{
    ID:    "advanced",
    Title: "Advanced (headless / scripting):",
})
```

Find the registration of the listed verbs (e.g., `addCmd(c, "session")`). For each verb in the test's `expectAdvanced` list, change the group to `"advanced"`:
```go
addCmd(newInterviewCmd(), "advanced")
addCmd(newRunCmd(),       "advanced")
addCmd(newSpecCmd(),      "advanced")
addCmd(newWatchCmd(),     "advanced")
addCmd(newEventsCmd(),    "advanced")
addCmd(newStopCmd(),      "advanced")
addCmd(newForkCmd(),      "advanced")
addCmd(newImportCmd(),    "advanced")
addCmd(newExportCmd(),    "advanced")
addCmd(newStatsCmd(),     "advanced")
addCmd(newMergeCmd(),     "advanced")
addCmd(newDiffCmd(),      "advanced")
```

Keep these in the existing top-level groups (not advanced):
- `init`, `auth`, `doctor`, `daemon`, `chat`, `status`, `cost`, `mcp`, `permissions`

- [ ] **Step 4: Run tests (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./cli/internal/cmd/... -run TestRoot_ -v`
Expected: PASS.

Also verify `gil --help` looks right:
```bash
cd /home/ubuntu/gil && go build -o /tmp/gil ./cli/cmd/gil && /tmp/gil --help
```
Expected: "Advanced (headless / scripting):" group present, contains the 12 verbs above; primary groups still show init/auth/doctor/daemon/chat/status/cost/mcp/permissions.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add cli/internal/cmd/root.go cli/internal/cmd/root_groups_test.go
git commit -m "feat(cli): P26 T14 — verb-mode CLI moved to 'advanced' cobra group"
```

---

## Task 15: Full Test Sweep

- [ ] **Step 1: Run all packages**

Run: `cd /home/ubuntu/gil && go test ./...`
Expected: 0 failures.

- [ ] **Step 2: If any failures, fix in place and re-run**

Common likely failures:
- Snapshot tests in `cli/internal/cmd/` that assert on old chat output → update.
- Tests that imported `intent.*` → delete or rewrite.

- [ ] **Step 3: Commit any test-only fixes**

```bash
cd /home/ubuntu/gil
git add -A
git commit -m "test: P26 T15 — sweep fixes after chat surface refactor"
```

---

## Task 16: E2E Dogfood

**Goal:** Verify a real interview→run→done cycle through the new chat surface.

- [ ] **Step 1: Build a fresh binary**

```bash
cd /home/ubuntu/gil && go build -o /tmp/gil ./cli/cmd/gil
```

- [ ] **Step 2: Start the daemon if not running**

```bash
/tmp/gil daemon --detach
```
Expected: `daemon listening on ~/.gil/gild.sock` (or already-running notice).

- [ ] **Step 3: Run a small mission via chat**

```bash
/tmp/gil
```
- At the prompt, type a small mission (e.g. `add a hello-world endpoint to /tmp/gil-dogfood-26`).
- Watch the strip update through interview phase: `[interview · 1/11 slots · sat 9%]` → climbing.
- Verify slot/adversary/saturation system notes appear inline.
- At `[interview · ready to freeze ...]`, type `/run`.
- Watch the strip switch to `[run · iter 1/100 · $0.00 · ASK_DESTRUCTIVE]` and tick up.
- On done, see `[done · ... · /diff /merge]`. Run `/diff`, then `/merge`.
- Type `/quit` to leave chat.

- [ ] **Step 4: Verify acceptance criteria from spec §10**

Check each of the 6 acceptance bullets manually:
1. Onboarding gate visible (or N/A if already initialized) ✓
2. Slot/saturation/adversary surface inline during interview ✓
3. Saturation→confirm dialog reached ✓
4. /run starts, strip updates ✓
5. [done] strip with /diff /merge ✓
6. Verb-mode `gil status` still works in another shell ✓

- [ ] **Step 5: Capture any UX bugs as follow-up tasks**

Anything jarring (jitter, off-by-one in strip counts, lost events) → file in `docs/plans/phase-26-implementation.md` under a new "## Followups" section (no commit needed yet; address before declaring V1 done).

- [ ] **Step 6: Commit a phase-completion marker**

```bash
cd /home/ubuntu/gil
git commit --allow-empty -m "chore: P26 V1 dogfood passed — chat surface lands"
```

---

## V1.1 (Deferred — separate phase)

The following require server-side changes and are NOT in this plan:

- `RunService.AddNote(sessionID, text)` proto + handler + agent loop injection at next-turn boundary.
- `RunService.Interrupt(sessionID)` + `RunService.Stop(sessionID)` — coordinated cancellation with mid-tool rollback.
- Wire `/interrupt`, `/stop` slash commands in repl/loop.go (the IsKnownSlash table just needs the two added).
- Replace the V1.1 prompt-echo placeholder with real note-queueing.

Track as `docs/plans/phase-26.5-runtime-control.md` once V1 ships.

---

## Self-Review Checklist (engineer should verify before merging)

1. **Spec coverage** — every requirement in `phase-26-chat-only-surface.md` §3-§7 maps to a task in this plan.
2. **No `fmt.Println` outside `render/stdout.go`** — `grep -n 'fmt\\.Print' cli/internal/chat/` should show only stdout.go.
3. **Verb CLI still works** — `/tmp/gil status`, `/tmp/gil interview <id>`, `/tmp/gil run <id>` all function (only --help grouping changed).
4. **All tests pass** — `go test ./...` clean.
5. **NO_COLOR honored** — set NO_COLOR=1, run chat, confirm no ANSI escapes in output.
6. **--ascii honored** — pass --ascii, confirm `OK` / `FAIL` instead of `✓` / `✗`.
