# Phase 26.6 — Prompt-Centric TUI Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent-panel chat surface to the existing `tui/` module — header, conversation pane, status strip, and a magenta-bordered prompt panel pinned at the bottom — and route bare `gil` (TTY) into it so prompting is the visible center of the surface.

**Architecture:** Add a new `chat`-mode bubbletea sub-model living alongside the existing watch/monitor model in `tui/internal/app/`. The chat sub-model owns the layout (header / conversation viewport / status strip / prompt input panel / affordance line) and a streaming gRPC adapter that consumes interview/run events from the daemon. Bare `gil` on TTY swaps from `cli.runChat` to a new `tuirun.Chat(socket)` entry point exported from `tui/`. The cli-side `runChat` stays for `--no-chat` and non-TTY scripts.

**Tech Stack:** Go 1.25, charmbracelet/bubbletea v1.1, charmbracelet/bubbles (textinput, viewport), charmbracelet/lipgloss v0.13. SDK: `github.com/mindungil/gil/sdk`. Existing TUI vocabulary in `tui/internal/app/{style,glyph}.go`.

**Reference:** `docs/plans/phase-26.6-prompt-centric-tui.md` — design spec.

---

## File Structure

**New files in `tui/internal/app/`:**
- `chat_model.go` — `chatModel` struct + state transitions + bubbletea Init/Update/View
- `chat_view.go` — `View()` rendering: header, conversation viewport, status strip, prompt panel, affordance line
- `chat_input.go` — textinput pane construction + history navigation + slash fallback parser
- `chat_stream.go` — gRPC adapter: dispatches interview Start/Reply, emits bubbletea Msgs from server stream
- `chat_session.go` — pre-first-turn session list rendering + the prose lead-in
- `chat_test.go` — interaction + snapshot tests for chat mode

**New files in `tui/`:**
- `tui/run/run.go` — `Chat(ctx, socket string) error` exported entry point. Constructs the `chatModel` and runs `tea.NewProgram(...).Run()`. Used by cli routing.

**Modified files:**
- `tui/internal/app/style.go` — add `stylePromptBorder` (magenta accent on the prompt panel) and `stylePromptBorderDim` (when input disabled). Add `Magenta` field to `Palette`.
- `cli/internal/cmd/root.go` — replace `runChat(...)` call in TTY branch with `tuirun.Chat(...)`.
- `cli/go.mod` — add dependency on `github.com/mindungil/gil/tui`.
- `tui/internal/app/glyph.go` — add `BoxHeavy` glyphs (`╔ ═ ╗ ║ ╚ ╝`) and `BoxLight` (`╭ ─ ╮ │ ╰ ╯`) accessors. ASCII fallback uses `+ - + |`.

**Untouched:**
- `tui/internal/app/{model.go, view.go, update.go}` — the watch/monitor surface stays as-is. Chat mode is a parallel sub-model invoked via the new `tui/run` entry point.
- `cli/internal/chat/repl/*` — kept verbatim for non-TTY scripts and `--no-chat`.

---

## Task 1: Palette additions for prompt border

**Files:**
- Modify: `tui/internal/app/style.go`
- Test: `tui/internal/app/style_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/style_test.go`:

```go
func TestPromptBorderStyles(t *testing.T) {
	// Active style is magenta — verifies a foreground SGR code is set.
	got := stylePromptBorder("╔══╗")
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("stylePromptBorder should emit ANSI when colors enabled, got %q", got)
	}
	// Dim style does NOT use the magenta fg.
	dimGot := stylePromptBorderDim("╔══╗")
	if dimGot == got {
		t.Fatalf("dim and active prompt-border styles should differ")
	}
	// NO_COLOR collapses both to plain text.
	SetNoColor(true)
	defer SetNoColor(false)
	plain := stylePromptBorder("╔══╗")
	if plain != "╔══╗" {
		t.Fatalf("NO_COLOR should yield plain text, got %q", plain)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestPromptBorderStyles -v`
Expected: FAIL with `undefined: stylePromptBorder`.

- [ ] **Step 3: Add Magenta to Palette and helpers**

In `tui/internal/app/style.go`, extend `Palette` struct (after `BgFill`):

```go
	Magenta  lipgloss.Color // bright magenta — prompt panel border (single-purpose)
```

Add to `truecolorPalette()` return literal:

```go
		Magenta:  lipgloss.Color("#d946ef"),
```

Add the two helpers near the bottom of `style.go` (before `paneFrame`):

```go
// stylePromptBorder returns text rendered in the magenta accent reserved
// for the chat prompt panel border. Per spec §4.1 this is the single
// magenta moment on screen — no other element calls this helper.
func stylePromptBorder(s string) string {
	if noColor {
		return lipgloss.NewStyle().Bold(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(pal.Magenta).Render(s)
}

// stylePromptBorderDim returns the prompt-panel border in dim/frame
// color, used when the input is temporarily disabled (agent turn in
// flight). The geometry stays identical so the panel doesn't jump.
func stylePromptBorderDim(s string) string {
	if noColor {
		return s
	}
	return lipgloss.NewStyle().Foreground(pal.Frame).Render(s)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestPromptBorderStyles -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all existing tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/style.go tui/internal/app/style_test.go
git commit -m "feat(tui): add magenta prompt-border styles for chat surface"
```

---

## Task 2: Box-drawing glyphs for header and prompt panel

**Files:**
- Modify: `tui/internal/app/glyph.go`
- Test: `tui/internal/app/glyph_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/glyph_test.go`:

```go
func TestBoxGlyphs_UnicodeAndAscii(t *testing.T) {
	prev := IsAsciiMode()
	defer SetAsciiMode(prev)

	SetAsciiMode(false)
	g := Glyphs()
	if g.BoxHeavyTL != "╔" || g.BoxHeavyTR != "╗" || g.BoxHeavyHRule != "═" || g.BoxHeavyVRule != "║" {
		t.Fatalf("unicode heavy box glyphs missing: %+v", g)
	}
	if g.BoxLightTL != "╭" || g.BoxLightHRule != "─" {
		t.Fatalf("unicode light box glyphs missing: %+v", g)
	}

	SetAsciiMode(true)
	a := Glyphs()
	if a.BoxHeavyTL != "+" || a.BoxHeavyHRule != "=" || a.BoxHeavyVRule != "|" {
		t.Fatalf("ascii heavy box fallback wrong: %+v", a)
	}
	if a.BoxLightTL != "+" || a.BoxLightHRule != "-" {
		t.Fatalf("ascii light box fallback wrong: %+v", a)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestBoxGlyphs -v`
Expected: FAIL with `undefined: BoxHeavyTL` (etc).

- [ ] **Step 3: Extend Glyph struct + sets**

In `tui/internal/app/glyph.go`, add to `Glyph` struct (after `Dot`):

```go
	BoxHeavyTL    string // ╔ — top-left corner of prompt panel
	BoxHeavyTR    string // ╗
	BoxHeavyBL    string // ╚
	BoxHeavyBR    string // ╝
	BoxHeavyHRule string // ═
	BoxHeavyVRule string // ║
	BoxLightTL    string // ╭ — header rounded corner (top-left)
	BoxLightTR    string // ╮
	BoxLightBL    string // ╰
	BoxLightBR    string // ╯
	BoxLightHRule string // ─
	BoxLightVRule string // │
```

Add to `unicodeGlyphs()` return literal:

```go
		BoxHeavyTL: "╔", BoxHeavyTR: "╗", BoxHeavyBL: "╚", BoxHeavyBR: "╝",
		BoxHeavyHRule: "═", BoxHeavyVRule: "║",
		BoxLightTL: "╭", BoxLightTR: "╮", BoxLightBL: "╰", BoxLightBR: "╯",
		BoxLightHRule: "─", BoxLightVRule: "│",
```

Add to `asciiGlyphs()` return literal:

```go
		BoxHeavyTL: "+", BoxHeavyTR: "+", BoxHeavyBL: "+", BoxHeavyBR: "+",
		BoxHeavyHRule: "=", BoxHeavyVRule: "|",
		BoxLightTL: "+", BoxLightTR: "+", BoxLightBL: "+", BoxLightBR: "+",
		BoxLightHRule: "-", BoxLightVRule: "|",
```

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestBoxGlyphs -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all existing tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/glyph.go tui/internal/app/glyph_test.go
git commit -m "feat(tui): add heavy/light box-drawing glyphs"
```

---

## Task 3: chatModel skeleton with phase enum + window-size handling

**Files:**
- Create: `tui/internal/app/chat_model.go`
- Test: `tui/internal/app/chat_test.go`

- [ ] **Step 1: Write the failing test**

Create `tui/internal/app/chat_test.go`:

```go
package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChatModel_InitialPhase_IsIdle(t *testing.T) {
	m := newChatModelForTest()
	if m.phase != ChatPhaseIdle {
		t.Fatalf("expected initial phase ChatPhaseIdle, got %v", m.phase)
	}
}

func TestChatModel_WindowSize_StoresDimensions(t *testing.T) {
	m := newChatModelForTest()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	cm := updated.(*chatModel)
	if cm.width != 100 || cm.height != 32 {
		t.Fatalf("expected width=100 height=32, got width=%d height=%d", cm.width, cm.height)
	}
}

// newChatModelForTest constructs a chatModel without dialing the daemon.
// All gRPC-dependent fields are left nil; tests that need them inject
// their own fake stream.
func newChatModelForTest() *chatModel {
	return &chatModel{phase: ChatPhaseIdle}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatModel -v`
Expected: FAIL with `undefined: chatModel` / `undefined: ChatPhaseIdle`.

- [ ] **Step 3: Implement chatModel skeleton**

Create `tui/internal/app/chat_model.go`:

```go
package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// ChatPhase mirrors cli/internal/chat/render.Phase but lives in the
// tui module so we don't pull cli into a tui dependency. Values are
// the same string constants the daemon emits in events, so cross-
// module comparisons stay trivial.
type ChatPhase string

const (
	ChatPhaseIdle             ChatPhase = "idle"
	ChatPhaseInterview        ChatPhase = "interview"
	ChatPhaseAwaitingConfirm  ChatPhase = "awaiting-confirm"
	ChatPhaseRun              ChatPhase = "run"
	ChatPhaseStuck            ChatPhase = "stuck"
	ChatPhaseDone             ChatPhase = "done"
)

// chatModel is the bubbletea root model for the prompt-centric chat
// surface. See docs/plans/phase-26.6-prompt-centric-tui.md.
type chatModel struct {
	socket string
	client *sdk.Client

	width  int
	height int

	phase     ChatPhase
	sessions  []*sdk.Session // pre-first-turn list, scrolls off after first turn
	activeID  string         // current session; empty before first turn

	// Conversation transcript, oldest first. Each entry is a fully
	// pre-rendered line ready for the viewport.
	transcript []string

	// Input state owned by chat_input.go (textinput model + history).
	input chatInputState

	// Streaming state owned by chat_stream.go.
	stream chatStreamState

	// firstTurnDone flips true the moment the user submits the first
	// prompt; that's the cue for chat_view.go to stop rendering the
	// session list above the conversation viewport.
	firstTurnDone bool

	err string
}

func (m *chatModel) Init() tea.Cmd { return nil }

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}
	return m, nil
}

func (m *chatModel) View() string {
	// View body is implemented in chat_view.go (added in Task 5).
	// Returning an empty string here keeps `go vet` happy while early
	// tasks build out the foundation.
	return ""
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatModel -v`
Expected: PASS

Run: `go build ./tui/...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_model.go tui/internal/app/chat_test.go
git commit -m "feat(tui): add chatModel skeleton with ChatPhase enum"
```

---

## Task 4: chat_input.go — textinput pane with magenta border

**Files:**
- Create: `tui/internal/app/chat_input.go`
- Modify: `tui/internal/app/chat_test.go`
- Modify: `tui/internal/app/chat_model.go`
- Modify: `tui/go.mod` (textinput is in `bubbles`, already a dep — verify)

- [ ] **Step 1: Verify dependency is present**

Run: `grep textinput /home/ubuntu/gil/tui/go.sum | head -3`
Expected: at least one line referencing `github.com/charmbracelet/bubbles`. If absent, add `go get github.com/charmbracelet/bubbles/textinput@v0.20.0` and re-run `go mod tidy ./tui/`.

- [ ] **Step 2: Write the failing test**

Append to `tui/internal/app/chat_test.go`:

```go
import (
	textinputbubble "github.com/charmbracelet/bubbles/textinput"
)

func TestChatInput_NewActive_HasFocus(t *testing.T) {
	in := newChatInput()
	if !in.ti.Focused() {
		t.Fatalf("new chat input should be focused")
	}
}

func TestChatInput_SubmitClearsBuffer(t *testing.T) {
	in := newChatInput()
	in.ti.SetValue("hello there")
	got := in.submit()
	if got != "hello there" {
		t.Fatalf("submit returned %q, want %q", got, "hello there")
	}
	if in.ti.Value() != "" {
		t.Fatalf("buffer should be cleared after submit, got %q", in.ti.Value())
	}
	// Submitted text appears in history at the back.
	if len(in.history) != 1 || in.history[0] != "hello there" {
		t.Fatalf("history not appended: %v", in.history)
	}
}

func TestChatInput_HistoryNavigation_UpDown(t *testing.T) {
	in := newChatInput()
	in.ti.SetValue("first")
	in.submit()
	in.ti.SetValue("second")
	in.submit()
	// Up once → "second".
	in.historyUp()
	if v := in.ti.Value(); v != "second" {
		t.Fatalf("up once → want \"second\", got %q", v)
	}
	// Up twice → "first".
	in.historyUp()
	if v := in.ti.Value(); v != "first" {
		t.Fatalf("up twice → want \"first\", got %q", v)
	}
	// Down → "second".
	in.historyDown()
	if v := in.ti.Value(); v != "second" {
		t.Fatalf("down → want \"second\", got %q", v)
	}
	// Down past end → empty buffer.
	in.historyDown()
	if v := in.ti.Value(); v != "" {
		t.Fatalf("down past end → want empty, got %q", v)
	}
}

// silence unused-import warning when textinputbubble is imported but
// only used via in.ti.* across helpers.
var _ = textinputbubble.New
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatInput -v`
Expected: FAIL with `undefined: newChatInput`.

- [ ] **Step 4: Implement chatInputState**

Create `tui/internal/app/chat_input.go`:

```go
package app

import (
	"github.com/charmbracelet/bubbles/textinput"
)

// chatInputState wraps a bubbles textinput with prompt-history
// navigation (↑/↓). The textinput owns the cursor + buffer; this
// wrapper adds the recall-history affordance described in design §3.1.
type chatInputState struct {
	ti      textinput.Model
	history []string // submitted prompts, oldest first
	histIdx int      // current cursor: -1 == "below history" (empty buffer)
}

// newChatInput returns a focused chatInputState ready for bubbletea
// Update routing. The placeholder text matches the affordance line
// subtitle for the idle phase (design §4.3).
func newChatInput() chatInputState {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.Focus()
	ti.Prompt = "" // we draw the › arrow in the panel chrome, not the input itself
	ti.CharLimit = 4096
	return chatInputState{ti: ti, histIdx: -1}
}

// submit returns the current buffer, clears it, and appends to history.
// The caller is responsible for sending the returned text downstream.
func (in *chatInputState) submit() string {
	v := in.ti.Value()
	in.ti.SetValue("")
	in.histIdx = -1
	if v == "" {
		return ""
	}
	in.history = append(in.history, v)
	return v
}

// historyUp walks back one entry. Idempotent at the oldest entry.
func (in *chatInputState) historyUp() {
	if len(in.history) == 0 {
		return
	}
	if in.histIdx == -1 {
		in.histIdx = len(in.history) - 1
	} else if in.histIdx > 0 {
		in.histIdx--
	}
	in.ti.SetValue(in.history[in.histIdx])
	in.ti.SetCursor(len(in.history[in.histIdx]))
}

// historyDown walks forward one entry. Stepping past the last entry
// returns the buffer to empty.
func (in *chatInputState) historyDown() {
	if len(in.history) == 0 || in.histIdx == -1 {
		return
	}
	in.histIdx++
	if in.histIdx >= len(in.history) {
		in.histIdx = -1
		in.ti.SetValue("")
		return
	}
	in.ti.SetValue(in.history[in.histIdx])
	in.ti.SetCursor(len(in.history[in.histIdx]))
}
```

In `chat_model.go`, replace the `chatInputState` field and initialise in a `newChatModel()` constructor — append at the bottom of `chat_model.go`:

```go
// newChatModel constructs a chatModel ready for tea.NewProgram.
// socket is dialed lazily by the stream layer (Task 8).
func newChatModel(socket string) *chatModel {
	return &chatModel{
		socket: socket,
		phase:  ChatPhaseIdle,
		input:  newChatInput(),
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatInput -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add tui/internal/app/chat_input.go tui/internal/app/chat_model.go tui/internal/app/chat_test.go
git commit -m "feat(tui): chat input pane with submit + history navigation"
```

---

## Task 5: chat_view.go — header, conversation viewport, status strip, prompt panel

**Files:**
- Create: `tui/internal/app/chat_view.go`
- Modify: `tui/internal/app/chat_model.go` (View() delegates here)
- Modify: `tui/internal/app/chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/chat_test.go`:

```go
func TestChatView_RendersAllRegions_AtFullSize(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.phase = ChatPhaseIdle

	out := m.View()

	// All five regions should produce identifiable text:
	if !strings.Contains(out, "G I L") {
		t.Errorf("header missing logo: %q", out)
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("status strip missing phase: %q", out)
	}
	if !strings.Contains(out, "describe a task") {
		t.Errorf("affordance subtitle missing: %q", out)
	}
	// The prompt panel's heavy border. NO_COLOR mode strips magenta
	// SGR but the box-drawing chars remain.
	if !strings.Contains(out, "═") && !strings.Contains(out, "=") {
		t.Errorf("prompt panel border missing: %q", out)
	}
}

func TestChatView_NarrowMode_DegradesGracefully(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 50 // < 60 col threshold per design §7
	m.height = 18
	m.phase = ChatPhaseIdle

	out := m.View()
	// Layout still produces output, doesn't panic.
	if out == "" {
		t.Fatalf("narrow mode produced empty view")
	}
}
```

(Top of file already has `import "strings"`; if not, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatView -v`
Expected: FAIL — view returns empty string from Task 3 placeholder.

- [ ] **Step 3: Implement chat_view.go**

Create `tui/internal/app/chat_view.go`:

```go
package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mindungil/gil/core/version"
)

// chatView assembles the five-region layout described in
// docs/plans/phase-26.6-prompt-centric-tui.md §3.1. Returns the full
// frame ready for tea.Program output.
//
// Region heights at full size (height ≥ 24):
//
//	header        = 3
//	prompt panel  = 5
//	affordance    = 1
//	status strip  = 2
//	conversation  = remaining (≥ 8)
//
// At height < 16 the prompt panel collapses to 3 rows (no inner
// padding). At width < 60 the session list pre-first-turn drops to a
// one-line summary ("3 past sessions — type /sessions to list").
func (m *chatModel) chatView() string {
	if m.width == 0 || m.height == 0 {
		return styleDim("loading" + Glyphs().Ellipsis)
	}

	header := m.renderChatHeader()

	promptH, prompt := m.renderPromptPanel()
	affordance := m.renderAffordanceLine()
	statusStrip := m.renderStatusStrip()

	// Conversation gets whatever's left.
	chromeH := lipgloss.Height(header) + promptH + lipgloss.Height(affordance) + lipgloss.Height(statusStrip)
	convH := m.height - chromeH
	if convH < 4 {
		convH = 4
	}
	conversation := m.renderConversation(convH)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		conversation,
		statusStrip,
		prompt,
		affordance,
	)
}

// renderChatHeader is the rounded box at the top per design §3.
//
//	╭──────────────────────────────────────────────────────────────────╮
//	│   ▏  G  I  L          /home/ubuntu/proj      claude-opus-4-7  ·  │
//	╰──────────────────────────────────────────────────────────────────╯
func (m *chatModel) renderChatHeader() string {
	g := Glyphs()
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "?"
	}
	left := styleDim(g.QuoteBar) + "  " + styleHeader("G  I  L")
	right := styleDim(cwd) + "   " + styleMeta("claude-opus-4-7  "+g.Dot+"  "+version.String())
	body := padBetween(left, right, m.width-4)

	tl, tr := g.BoxLightTL, g.BoxLightTR
	bl, br := g.BoxLightBL, g.BoxLightBR
	hr := g.BoxLightHRule
	vr := g.BoxLightVRule

	top := styleDim(tl + strings.Repeat(hr, m.width-2) + tr)
	mid := styleDim(vr) + " " + body + " " + styleDim(vr)
	bot := styleDim(bl + strings.Repeat(hr, m.width-2) + br)
	return lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
}

// renderPromptPanel returns (height, rendered). The panel is the
// heavy magenta box per design §3 — the visual focal point.
func (m *chatModel) renderPromptPanel() (int, string) {
	g := Glyphs()
	tl, tr := g.BoxHeavyTL, g.BoxHeavyTR
	bl, br := g.BoxHeavyBL, g.BoxHeavyBR
	hr := g.BoxHeavyHRule
	vr := g.BoxHeavyVRule

	bordStyle := stylePromptBorder
	if !m.inputEnabled() {
		bordStyle = stylePromptBorderDim
	}

	top := bordStyle(tl + strings.Repeat(hr, m.width-2) + tr)
	bot := bordStyle(bl + strings.Repeat(hr, m.width-2) + br)

	// Inner content. The `›` is bold-white chrome (not magenta).
	cue := styled().Bold(true).Render(g.Arrow)
	inputView := m.input.ti.View()
	inner := "  " + cue + "  " + inputView
	// pad inner to width-2 so the right border lands flush
	innerLen := lipgloss.Width(inner)
	if innerLen < m.width-2 {
		inner += strings.Repeat(" ", m.width-2-innerLen)
	}
	pad := bordStyle(vr) + strings.Repeat(" ", m.width-2) + bordStyle(vr)
	mid := bordStyle(vr) + inner + bordStyle(vr)

	if m.height < 16 {
		// 3-row form: no inner padding.
		return 3, lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
	}
	return 5, lipgloss.JoinVertical(lipgloss.Left, top, pad, mid, pad, bot)
}

// renderAffordanceLine is the single row of helper text below the
// prompt panel. Subtitle on the left changes with phase; right side
// shows the version + key hints.
func (m *chatModel) renderAffordanceLine() string {
	subtitle := chatSubtitle(m.phase)
	hints := fmt.Sprintf("%s  %s  %s history  /  %s cmds",
		version.String(), Glyphs().Dot, "↑↓", "/")
	left := "      " + styleMeta(subtitle)
	right := styleMeta(hints)
	return padBetween(left, right, m.width)
}

// chatSubtitle maps phase → the user-typeable verbs in NL form per
// design §4.3.
func chatSubtitle(p ChatPhase) string {
	switch p {
	case ChatPhaseIdle:
		return "describe a task, resume by slug, or ask what's running"
	case ChatPhaseInterview:
		return "answer the question above, or ask gil to clarify"
	case ChatPhaseAwaitingConfirm:
		return `type "freeze" to start the run, or keep iterating`
	case ChatPhaseRun:
		return "run in progress · type to queue follow-ups"
	case ChatPhaseStuck:
		return `recovery exhausted · "stop" to halt or "retry" to continue`
	case ChatPhaseDone:
		return `run complete · "diff" to review · "merge" to apply`
	default:
		return "describe a task, resume by slug, or ask what's running"
	}
}

// renderStatusStrip is the divider rule + one phase-state line above
// the prompt panel. Two rows total (rule + body).
func (m *chatModel) renderStatusStrip() string {
	g := Glyphs()
	rule := styleDim(strings.Repeat(g.HSep, m.width))
	body := fmt.Sprintf("%s  %s  agent ready", string(m.phase), g.Dot)
	right := styleMeta(body)
	left := strings.Repeat(" ", 6)
	row := padBetween(left, right, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, rule, row)
}

// renderConversation fills the middle region. Pre-first-turn shows
// the past-session list; after the first turn the transcript replaces
// it. Both forms are clipped to convH rows.
func (m *chatModel) renderConversation(convH int) string {
	if !m.firstTurnDone {
		// Pre-first-turn: session disclosure (chat_session.go in Task 6).
		return m.renderPreFirstTurn(convH)
	}
	// Post-first-turn: tail of transcript.
	if len(m.transcript) == 0 {
		return strings.Repeat("\n", convH-1)
	}
	start := 0
	if len(m.transcript) > convH {
		start = len(m.transcript) - convH
	}
	body := strings.Join(m.transcript[start:], "\n")
	// Pad to convH so chrome below stays pinned.
	lines := strings.Count(body, "\n") + 1
	if lines < convH {
		body += strings.Repeat("\n", convH-lines)
	}
	return body
}

// inputEnabled reports whether the prompt panel should accept new
// input. False during the run/stuck phases; the cli REPL uses the
// same rule.
func (m *chatModel) inputEnabled() bool {
	return m.phase != ChatPhaseRun && m.phase != ChatPhaseStuck
}
```

In `chat_model.go`, change the View() method body:

```go
func (m *chatModel) View() string { return m.chatView() }
```

Add a stub for `renderPreFirstTurn` so the package builds (Task 6 fills it in):

```go
// renderPreFirstTurn is implemented in chat_session.go (Task 6).
func (m *chatModel) renderPreFirstTurn(convH int) string {
	return strings.Repeat("\n", convH-1)
}
```

Wait — that's a duplicate definition with chat_session.go later. Instead, add the stub here as a TEMPORARY implementation and DELETE it in Task 6. Mark with a comment:

```go
// TEMPORARY stub — replaced in Task 6 (chat_session.go).
func (m *chatModel) renderPreFirstTurn(convH int) string {
	return strings.Repeat("\n", convH-1)
}
```

Add `import "strings"` to chat_model.go if missing.

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatView -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_view.go tui/internal/app/chat_model.go tui/internal/app/chat_test.go
git commit -m "feat(tui): chat layout — header, viewport, status strip, prompt panel"
```

---

## Task 6: chat_session.go — pre-first-turn session disclosure

**Files:**
- Create: `tui/internal/app/chat_session.go`
- Modify: `tui/internal/app/chat_model.go` (delete the temporary `renderPreFirstTurn` stub)
- Modify: `tui/internal/app/chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/chat_test.go`:

```go
import (
	"time"

	"github.com/mindungil/gil/sdk"
)

func TestChatSession_PreFirstTurn_RendersList(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.sessions = []*sdk.Session{
		{ID: "01HQXYZ001", GoalHint: "add dark mode", Status: "INTERVIEWING", CreatedAt: time.Now().Add(-4 * time.Minute)},
		{ID: "01HQXYZ002", GoalHint: "fix oauth", Status: "DONE", CreatedAt: time.Now().Add(-2 * time.Hour)},
	}

	got := m.renderPreFirstTurn(20)

	if !strings.Contains(got, "2 past sessions") {
		t.Errorf("missing lead-in: %q", got)
	}
	if !strings.Contains(got, "add dark mode") {
		t.Errorf("session 1 not rendered: %q", got)
	}
	if !strings.Contains(got, "fix oauth") {
		t.Errorf("session 2 not rendered: %q", got)
	}
}

func TestChatSession_NoSessions_FallsBackToInvite(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32

	got := m.renderPreFirstTurn(20)
	if !strings.Contains(got, "no past sessions") {
		t.Errorf("expected empty-state invite, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatSession -v`
Expected: FAIL — temporary stub returns blank lines, doesn't include "past sessions" text.

- [ ] **Step 3: Delete the stub in chat_model.go**

Open `tui/internal/app/chat_model.go` and remove:

```go
// TEMPORARY stub — replaced in Task 6 (chat_session.go).
func (m *chatModel) renderPreFirstTurn(convH int) string {
	return strings.Repeat("\n", convH-1)
}
```

- [ ] **Step 4: Implement chat_session.go**

Create `tui/internal/app/chat_session.go`:

```go
package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/mindungil/gil/sdk"
)

// renderPreFirstTurn returns the session-list-and-invite block shown
// in the conversation region until the user submits their first prompt.
// Total height is pinned to convH rows by trailing-newline padding so
// the chrome below stays in its row.
func (m *chatModel) renderPreFirstTurn(convH int) string {
	const topN = 5

	var b strings.Builder
	b.WriteString("\n") // 1 row of breathing space

	if len(m.sessions) == 0 {
		b.WriteString("      ")
		b.WriteString(styleSurface("no past sessions"))
		b.WriteString("  ")
		b.WriteString(styleMeta("— describe what you want to build below"))
		b.WriteString("\n")
		return padToHeight(b.String(), convH)
	}

	lead := chatLeadIn(len(m.sessions), topN)
	b.WriteString("      ")
	b.WriteString(styleSurface(lead))
	b.WriteString("\n\n")

	shown := m.sessions
	if len(shown) > topN {
		shown = shown[:topN]
	}
	for _, s := range shown {
		b.WriteString("         ")
		b.WriteString(formatChatSessionRow(s, m.width))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("         ")
	b.WriteString(styleMeta("›  describe a new task, or resume one above by name"))
	b.WriteString("\n")
	return padToHeight(b.String(), convH)
}

// chatLeadIn produces the prose lead-in line above the session rows.
func chatLeadIn(total, topN int) string {
	if total == 1 {
		return "1 past session — pick it below or describe a new task"
	}
	if total <= topN {
		return fmt.Sprintf("%d past sessions — pick one below or describe a new task", total)
	}
	return fmt.Sprintf("%d past sessions — most recent %d below, describe a new task or resume one",
		total, topN)
}

// formatChatSessionRow renders one row: glyph, slug (truncated), age,
// phase. Width-aware so narrow terminals don't tear.
func formatChatSessionRow(s *sdk.Session, termWidth int) string {
	g := Glyphs()
	glyph := g.Idle
	if strings.EqualFold(s.Status, "RUNNING") {
		glyph = g.Running
	}
	slug := s.GoalHint
	if slug == "" {
		// fall back to ID prefix
		slug = strings.ToLower(s.ID)
		if len(slug) > 10 {
			slug = slug[:10]
		}
	}
	if len(slug) > 32 {
		slug = slug[:31] + g.Ellipsis
	}
	age := relAgeShort(s.CreatedAt)
	phase := strings.ToLower(s.Status)

	// Fixed columns: glyph(1) + 2sp + slug(32) + 4sp + age(5) + 4sp + phase
	row := fmt.Sprintf("%s  %-32s    %-5s    %s",
		styleDim(glyph), styleSurface(slug), styleMeta(age), styleMeta(phase))
	if termWidth < 60 {
		// Narrow: drop phase, shorten slug.
		shortSlug := slug
		if len(shortSlug) > 18 {
			shortSlug = shortSlug[:17] + g.Ellipsis
		}
		row = fmt.Sprintf("%s  %-18s  %s",
			styleDim(glyph), styleSurface(shortSlug), styleMeta(age))
	}
	return row
}

func relAgeShort(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return "—"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// padToHeight ensures s ends with enough newlines to span exactly h
// rows. If s is already taller than h, returns s unchanged.
func padToHeight(s string, h int) string {
	have := strings.Count(s, "\n")
	if have >= h-1 {
		return s
	}
	return s + strings.Repeat("\n", h-1-have)
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatSession -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add tui/internal/app/chat_session.go tui/internal/app/chat_model.go tui/internal/app/chat_test.go
git commit -m "feat(tui): pre-first-turn session disclosure block"
```

---

## Task 7: Key dispatch — Enter submits, ↑↓ history, Ctrl+C / `/quit` exit

**Files:**
- Modify: `tui/internal/app/chat_model.go` (Update method)
- Modify: `tui/internal/app/chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/chat_test.go`:

```go
import (
	tea "github.com/charmbracelet/bubbletea"
)

func TestChatUpdate_EnterSubmitsAndAppendsToTranscript(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.input.ti.SetValue("hi there")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*chatModel)
	// Buffer cleared.
	if cm.input.ti.Value() != "" {
		t.Fatalf("buffer should clear on submit, got %q", cm.input.ti.Value())
	}
	// Transcript has the user line.
	if len(cm.transcript) == 0 || !strings.Contains(cm.transcript[len(cm.transcript)-1], "hi there") {
		t.Fatalf("transcript missing user line: %v", cm.transcript)
	}
	// firstTurnDone flips on first submit.
	if !cm.firstTurnDone {
		t.Fatalf("expected firstTurnDone=true after first submit")
	}
}

func TestChatUpdate_CtrlC_ReturnsQuitCmd(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("Ctrl+C should return a Quit cmd")
	}
}

func TestChatUpdate_SlashQuit_ReturnsQuitCmd(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.ti.SetValue("/quit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("/quit should return a Quit cmd")
	}
}

func TestChatUpdate_Up_RecallsHistory(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.input.history = []string{"first", "second"}
	m.input.histIdx = -1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	cm := updated.(*chatModel)
	if cm.input.ti.Value() != "second" {
		t.Fatalf("up → want \"second\", got %q", cm.input.ti.Value())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatUpdate -v`
Expected: FAIL — current Update only handles WindowSizeMsg.

- [ ] **Step 3: Implement key handlers in chat_model.go**

Replace the `Update` method body:

```go
func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.ti.Width = msg.Width - 8 // panel inner width minus › prefix gutter
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyUp:
			m.input.historyUp()
			return m, nil
		case tea.KeyDown:
			m.input.historyDown()
			return m, nil
		case tea.KeyEnter:
			text := m.input.submit()
			if text == "" {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				return m, tea.Quit
			}
			// Echo to transcript so the user sees what they sent.
			m.transcript = append(m.transcript, "›  "+text)
			m.firstTurnDone = true
			// Stream dispatch wired in Task 8.
			return m, nil
		default:
			// Forward to the textinput so typing works.
			var cmd tea.Cmd
			m.input.ti, cmd = m.input.ti.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}
```

Add `tea "github.com/charmbracelet/bubbletea"` to the import block of `chat_model.go` if not present.

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatUpdate -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_model.go tui/internal/app/chat_test.go
git commit -m "feat(tui): chat key dispatch — submit, history, quit"
```

---

## Task 8: chat_stream.go — gRPC bidi adapter (interview Start/Reply → bubbletea Msgs)

**Files:**
- Create: `tui/internal/app/chat_stream.go`
- Modify: `tui/internal/app/chat_model.go` (initial dial + Init() + stream Msg handling)
- Modify: `tui/internal/app/chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tui/internal/app/chat_test.go`:

```go
func TestChatStream_AssistantChunkAppendsToTranscript(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	m.firstTurnDone = true

	updated, _ := m.Update(chatAssistantChunkMsg{text: "hello "})
	cm := updated.(*chatModel)
	if len(cm.transcript) == 0 || !strings.HasSuffix(cm.transcript[len(cm.transcript)-1], "hello ") {
		t.Fatalf("expected last transcript line to end in \"hello \", got %v", cm.transcript)
	}

	updated, _ = cm.Update(chatAssistantChunkMsg{text: "world"})
	cm = updated.(*chatModel)
	last := cm.transcript[len(cm.transcript)-1]
	if !strings.Contains(last, "hello world") {
		t.Fatalf("expected coalesced \"hello world\", got %q", last)
	}
}

func TestChatStream_PhaseChangeMsg_UpdatesPhase(t *testing.T) {
	m := newChatModel("/tmp/test.sock")
	updated, _ := m.Update(chatPhaseMsg{phase: ChatPhaseRun})
	cm := updated.(*chatModel)
	if cm.phase != ChatPhaseRun {
		t.Fatalf("expected phase=run, got %v", cm.phase)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/internal/app/ -run TestChatStream -v`
Expected: FAIL — `undefined: chatAssistantChunkMsg`.

- [ ] **Step 3: Implement chat_stream.go**

Create `tui/internal/app/chat_stream.go`:

```go
package app

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// chatStreamState carries the cursor for an in-flight interview
// stream. The bubbletea pattern (see tui/internal/app/tail.go for
// the watch surface's analogue) is:
//
//   1. submit → startInterviewCmd → chatStreamStartedMsg{stream}
//   2. Update stores the stream handle, returns nextChatEventCmd
//   3. nextChatEventCmd → stream.Recv() → chatStreamEventMsg{event}
//   4. Update processes the event AND returns nextChatEventCmd again
//   5. on EOF / error, emits chatStreamDoneMsg / chatStreamErrMsg
//
// Storing the stream on the model lets the user's next Enter cancel
// the previous turn cleanly via cancel().
type chatStreamState struct {
	stream gilv1.InterviewService_StartClient
	cancel context.CancelFunc
}

// chatAssistantChunkMsg carries one streamed text chunk from the
// daemon. View appends to the open assistant line.
type chatAssistantChunkMsg struct{ text string }

// chatPhaseMsg signals a phase transition derived from a daemon
// event. Drives status strip + affordance subtitle + border color.
type chatPhaseMsg struct{ phase ChatPhase }

// chatStreamStartedMsg hands the freshly-opened stream handle and
// its cancel function to the Update loop so the model can pump it
// with nextChatEventCmd.
type chatStreamStartedMsg struct {
	stream gilv1.InterviewService_StartClient
	cancel context.CancelFunc
}

// chatStreamDoneMsg signals graceful EOF on the stream — usually
// after a stage transition to ready_to_freeze. The model resets the
// stream cursor so the next Enter starts a fresh leg.
type chatStreamDoneMsg struct{}

// chatStreamErrMsg surfaces stream errors to the status strip.
type chatStreamErrMsg struct{ err string }

// startInterviewCmd opens the bidi interview stream and returns the
// handle via chatStreamStartedMsg. The stream is then drained by
// repeated nextChatEventCmd calls scheduled from Update.
func startInterviewCmd(client *sdk.Client, sessionID, firstInput string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.StartInterview(ctx, sessionID, firstInput, "", "", sdk.InterviewModels{})
		if err != nil {
			cancel()
			return chatStreamErrMsg{err: err.Error()}
		}
		return chatStreamStartedMsg{stream: stream, cancel: cancel}
	}
}

// nextChatEventCmd reads one event from the active stream and
// converts it to the appropriate Msg. Re-issued by Update after
// each event so the stream drains continuously.
func nextChatEventCmd(stream gilv1.InterviewService_StartClient) tea.Cmd {
	return func() tea.Msg {
		ev, err := stream.Recv()
		if err != nil {
			// Stream closed cleanly OR errored. The chatModel's
			// Update treats both as turn-end; if the daemon emits
			// a richer signal we'll switch on err here.
			return chatStreamDoneMsg{}
		}
		switch ev.GetType() {
		case "assistant_chunk":
			return chatAssistantChunkMsg{text: extractAssistantChunk(ev.GetDataJson())}
		case "stage":
			return chatPhaseMsg{phase: extractStagePhase(ev.GetDataJson())}
		default:
			// Unknown events become "skip" — return another
			// nextChatEventCmd by emitting a sentinel that Update
			// converts back into a pump call. V1 just drops them.
			return chatStreamEventSkipMsg{}
		}
	}
}

// chatStreamEventSkipMsg is the "drop and re-pump" sentinel for
// events the chat surface doesn't yet render. Keeps the drain loop
// going without the chat code having to know every event type the
// daemon might emit.
type chatStreamEventSkipMsg struct{}

// extractAssistantChunk pulls the "text" field from an
// assistant_chunk event payload. V1 uses a thin JSON unmarshal; the
// existing tail.go helper does the same for the watch surface.
func extractAssistantChunk(dataJSON []byte) string {
	var p struct {
		Text string `json:"text"`
	}
	_ = jsonUnmarshalQuiet(dataJSON, &p)
	return p.Text
}

// extractStagePhase maps an interview stage event to the local
// ChatPhase. The wire stages are "sensing" / "conversation" /
// "ready_to_freeze" — V1 collapses sensing+conversation to interview
// and ready_to_freeze to awaiting-confirm.
func extractStagePhase(dataJSON []byte) ChatPhase {
	var p struct {
		Stage string `json:"stage"`
	}
	_ = jsonUnmarshalQuiet(dataJSON, &p)
	switch p.Stage {
	case "ready_to_freeze":
		return ChatPhaseAwaitingConfirm
	case "sensing", "conversation":
		return ChatPhaseInterview
	default:
		return ChatPhaseIdle
	}
}
```

In `chat_model.go`, extend the `tea.KeyEnter` branch in Update to dispatch the stream after the user's first prompt is submitted:

```go
		case tea.KeyEnter:
			text := m.input.submit()
			if text == "" {
				return m, nil
			}
			if text == "/quit" || text == "/exit" {
				if m.stream.cancel != nil {
					m.stream.cancel()
				}
				return m, tea.Quit
			}
			m.transcript = append(m.transcript, "›  "+text)
			m.firstTurnDone = true
			if m.client == nil {
				return m, nil // test mode — no stream dispatch
			}
			if m.activeID == "" {
				m.err = "no active session — daemon must allocate one before chat"
				return m, nil
			}
			// Cancel any in-flight stream from a previous turn so the
			// new submit takes priority.
			if m.stream.cancel != nil {
				m.stream.cancel()
				m.stream = chatStreamState{}
			}
			return m, startInterviewCmd(m.client, m.activeID, text)
```

Add the new Msg cases to the Update switch (before the `default` of the outer switch):

```go
	case chatStreamStartedMsg:
		m.stream.stream = msg.stream
		m.stream.cancel = msg.cancel
		return m, nextChatEventCmd(msg.stream)

	case chatAssistantChunkMsg:
		// Coalesce consecutive assistant chunks onto the same line so
		// the user sees a flowing reply rather than one `‹` header
		// per chunk.
		if len(m.transcript) > 0 && strings.HasPrefix(m.transcript[len(m.transcript)-1], "‹") {
			m.transcript[len(m.transcript)-1] += msg.text
		} else {
			m.transcript = append(m.transcript, "‹  "+msg.text)
		}
		// Keep draining.
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatPhaseMsg:
		m.phase = msg.phase
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatStreamEventSkipMsg:
		if m.stream.stream != nil {
			return m, nextChatEventCmd(m.stream.stream)
		}
		return m, nil

	case chatStreamDoneMsg:
		// Turn complete. Reset stream cursor; keep cancel so quit
		// path can call it harmlessly.
		m.stream.stream = nil
		return m, nil

	case chatStreamErrMsg:
		m.err = msg.err
		m.stream.stream = nil
		return m, nil
```

Note the `‹` prefix marks assistant lines (mirror of the user's `›`). The renderStatusStrip should also show `m.err` when non-empty — patch `chat_view.go`:

```go
func (m *chatModel) renderStatusStrip() string {
	g := Glyphs()
	rule := styleDim(strings.Repeat(g.HSep, m.width))
	body := fmt.Sprintf("%s  %s  agent ready", string(m.phase), g.Dot)
	if m.err != "" {
		body = styleAlert("error: " + m.err)
	}
	right := styleMeta(body)
	left := strings.Repeat(" ", 6)
	row := padBetween(left, right, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, rule, row)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./tui/internal/app/ -run TestChatStream -v`
Expected: PASS

Run: `go test ./tui/internal/app/`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_stream.go tui/internal/app/chat_model.go tui/internal/app/chat_view.go tui/internal/app/chat_test.go
git commit -m "feat(tui): gRPC interview stream → chatModel msg pump"
```

---

## Task 9: tui/run package — Chat() entry point

**Files:**
- Create: `tui/run/run.go`
- Create: `tui/run/run_test.go`

- [ ] **Step 1: Write the failing test**

Create `tui/run/run_test.go`:

```go
package run

import (
	"context"
	"testing"
	"time"
)

func TestChat_DialFails_ReturnsErr(t *testing.T) {
	// /tmp/nonexistent.sock should fail to dial.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := Chat(ctx, "/tmp/gil-nonexistent-12345.sock")
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tui/run/ -v`
Expected: FAIL — `package github.com/mindungil/gil/tui/run is not in std`. (Build error.)

- [ ] **Step 3: Implement tui/run/run.go**

Create `tui/run/run.go`:

```go
// Package run is the public entry point for launching the gil chat
// TUI from outside the tui module. The cli module imports this
// instead of reaching into tui/internal/app, keeping the bubbletea
// surface of the tui module fully internal.
package run

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
	"github.com/mindungil/gil/tui/internal/app"
)

// Chat dials the gild socket and runs the prompt-centric chat TUI
// until the user exits or ctx is cancelled. Returns the program
// error, if any.
func Chat(ctx context.Context, socket string) error {
	cli, err := sdk.Dial(socket)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer cli.Close()

	m := app.NewChatModelForRun(socket, cli)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}
```

The internal `chatModel` constructor needs a public exported wrapper. Add to `tui/internal/app/chat_model.go`:

```go
// NewChatModelForRun is the public constructor used by tui/run.Chat.
// Returns a tea.Model so the unexported chatModel type doesn't leak
// across the package boundary.
func NewChatModelForRun(socket string, client *sdk.Client) tea.Model {
	m := newChatModel(socket)
	m.client = client
	return m
}
```

Add `tea "github.com/charmbracelet/bubbletea"` to chat_model.go imports if not already present (it should be from Task 7).

- [ ] **Step 4: Run tests**

Run: `go test ./tui/run/ -v`
Expected: PASS — dial fails, error returned.

Run: `go build ./tui/...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add tui/run/run.go tui/run/run_test.go tui/internal/app/chat_model.go
git commit -m "feat(tui): tui/run.Chat entry point for cross-module invocation"
```

---

## Task 10: cli routing — bare gil (TTY) → tui/run.Chat

**Files:**
- Modify: `cli/internal/cmd/root.go`
- Modify: `cli/go.mod` + `cli/go.sum` (add tui dependency)
- Modify: `cli/internal/cmd/chat_test.go` (update assertions to expect new routing)

- [ ] **Step 1: Wire the dependency**

The cli already lives in the same go workspace as tui. Add the import edge to `cli/go.mod`:

Run from `/home/ubuntu/gil/cli`:

```bash
go get github.com/mindungil/gil/tui@v0.0.0
```

If go workspace handles the local replace already, this should be a no-op edit on go.mod adding the require line. Verify by running `go build ./cli/...` afterward.

- [ ] **Step 2: Update root.go**

Open `cli/internal/cmd/root.go`. Find the RunE shim (around line 124–129):

```go
RunE: func(cmd *cobra.Command, _ []string) error {
    if !noChat && stdoutIsTTY() {
        return runChat(cmd, defaultSocket(), "", "")
    }
    return runSummary(cmd.OutOrStdout(), defaultSocket(), defaultBase(), asciiMode)
},
```

Replace with:

```go
RunE: func(cmd *cobra.Command, _ []string) error {
    if !noChat && stdoutIsTTY() {
        // Phase 26.6: TTY chat surface lives in the tui module —
        // a persistent panel layout with a magenta-bordered prompt
        // panel as the visual focal point. Non-TTY and --no-chat
        // continue to fall through to the line-based summary.
        return tuirun.Chat(cmd.Context(), defaultSocket())
    }
    return runSummary(cmd.OutOrStdout(), defaultSocket(), defaultBase(), asciiMode)
},
```

Add the import:

```go
import (
    // ... existing ...
    tuirun "github.com/mindungil/gil/tui/run"
)
```

- [ ] **Step 3: Update root_test.go assertions**

If `cli/internal/cmd/chat_test.go` previously asserted that bare `gil` (TTY) calls `runChat`, those assertions need to change to expect a tuirun.Chat call. In test envs there's no real TTY, so these tests likely cover the non-TTY path already; verify by running:

Run: `go test ./cli/... -run TestRoot -v`
Expected: PASS.

If a test needs adjusting, change its expectation from "runChat invoked" to "tuirun.Chat invoked". For test environments lacking a TTY, the runSummary branch fires and the new code path is unaffected.

- [ ] **Step 4: Build the whole workspace**

Run from `/home/ubuntu/gil`:

```bash
go build ./cli/... ./core/... ./server/... ./sdk/... ./tui/... ./mcp/...
```

Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add cli/go.mod cli/go.sum cli/internal/cmd/root.go cli/internal/cmd/chat_test.go
git commit -m "feat(cli): route bare gil (TTY) → tui/run.Chat"
```

---

## Task 11: Snapshot tests for the layout at four reference sizes

**Files:**
- Create: `tui/internal/app/chat_snapshot_test.go`
- Create: `tui/internal/app/testdata/chat_idle_100x32.txt`
- Create: `tui/internal/app/testdata/chat_idle_80x24.txt`
- Create: `tui/internal/app/testdata/chat_idle_60x18.txt`
- Create: `tui/internal/app/testdata/chat_idle_40x14.txt`

- [ ] **Step 1: Write the failing test**

Create `tui/internal/app/chat_snapshot_test.go`:

```go
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/version"
)

var snapshotSizes = []struct {
	w, h int
	name string
}{
	{100, 32, "chat_idle_100x32"},
	{80, 24, "chat_idle_80x24"},
	{60, 18, "chat_idle_60x18"},
	{40, 14, "chat_idle_40x14"},
}

func TestChatView_Snapshots(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	SetNoColor(true)
	SetAsciiMode(false)
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)

	for _, sz := range snapshotSizes {
		t.Run(sz.name, func(t *testing.T) {
			m := newChatModel("/tmp/test.sock")
			m.width = sz.w
			m.height = sz.h
			m.phase = ChatPhaseIdle

			got := stripDynamic(m.View())
			path := filepath.Join("testdata", sz.name+".txt")

			if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing snapshot %s — run with UPDATE_SNAPSHOTS=1 to create. err=%v", path, err)
			}
			if got != string(want) {
				t.Errorf("snapshot mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
					sz.name, string(want), got)
			}
		})
	}
}

// stripDynamic removes machine/build-dependent values from the View
// output so snapshots are stable across machines.
func stripDynamic(s string) string {
	if v := version.String(); v != "" {
		s = strings.ReplaceAll(s, v, "<version>")
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		s = strings.ReplaceAll(s, cwd, "<cwd>")
	}
	return s
}
```

- [ ] **Step 2: Run test to verify it fails (no snapshots yet)**

Run: `go test ./tui/internal/app/ -run TestChatView_Snapshots -v`
Expected: FAIL — "missing snapshot testdata/...".

- [ ] **Step 3: Generate snapshots**

Run: `UPDATE_SNAPSHOTS=1 go test ./tui/internal/app/ -run TestChatView_Snapshots`
Expected: 4 files created in `tui/internal/app/testdata/`.

- [ ] **Step 4: Verify snapshots are stable**

Run: `go test ./tui/internal/app/ -run TestChatView_Snapshots -v`
Expected: PASS.

Visually inspect one snapshot:

Run: `cat tui/internal/app/testdata/chat_idle_100x32.txt`
Expected: shows the rounded header, "no past sessions" or session list, status strip, prompt panel with `╔══╗`, affordance line.

If the layout is wrong, FIX chat_view.go and regenerate.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_snapshot_test.go tui/internal/app/testdata/
git commit -m "test(tui): snapshot tests for chat layout at 4 reference sizes"
```

---

## Task 12: ASCII + NO_COLOR snapshot variants

**Files:**
- Modify: `tui/internal/app/chat_snapshot_test.go`
- Create: `tui/internal/app/testdata/chat_idle_80x24_ascii.txt`
- Create: `tui/internal/app/testdata/chat_idle_80x24_color.txt`

- [ ] **Step 1: Add the two new tests**

Append to `tui/internal/app/chat_snapshot_test.go`:

```go
func TestChatView_AsciiSnapshot(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	SetNoColor(true)
	SetAsciiMode(true)
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)

	m := newChatModel("/tmp/test.sock")
	m.width = 80
	m.height = 24
	m.phase = ChatPhaseIdle

	got := stripDynamic(m.View())
	path := filepath.Join("testdata", "chat_idle_80x24_ascii.txt")

	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing snapshot %s — run UPDATE_SNAPSHOTS=1", path)
	}
	if got != string(want) {
		t.Errorf("ASCII snapshot mismatch:\n--- want ---\n%s\n--- got ---\n%s", string(want), got)
	}
	// Verify no Unicode box chars leaked through.
	for _, ch := range []string{"╔", "╗", "═", "╭", "╮", "─"} {
		if strings.Contains(got, ch) {
			t.Errorf("ASCII output contained unicode box char %q", ch)
		}
	}
}

func TestChatView_ColorSnapshot(t *testing.T) {
	// Inverse of the NO_COLOR snapshot — verifies magenta SGR is
	// emitted on the prompt panel border when colors are on.
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	SetNoColor(false)
	SetAsciiMode(false)
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)

	m := newChatModel("/tmp/test.sock")
	m.width = 80
	m.height = 24
	m.phase = ChatPhaseIdle

	got := stripDynamic(m.View())
	// Magenta SGR is `\x1b[38;2;...` (truecolor) — at least once on the
	// prompt panel.
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("color mode produced no ANSI escapes")
	}
	path := filepath.Join("testdata", "chat_idle_80x24_color.txt")

	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing snapshot %s — run UPDATE_SNAPSHOTS=1", path)
	}
	if got != string(want) {
		t.Errorf("color snapshot mismatch")
	}
}
```

- [ ] **Step 2: Verify they fail without snapshots**

Run: `go test ./tui/internal/app/ -run TestChatView_AsciiSnapshot -v`
Expected: FAIL — missing snapshot.

- [ ] **Step 3: Generate snapshots**

Run: `UPDATE_SNAPSHOTS=1 go test ./tui/internal/app/ -run "TestChatView_(Ascii|Color)Snapshot"`
Expected: 2 new files in testdata/.

- [ ] **Step 4: Verify they pass**

Run: `go test ./tui/internal/app/ -run "TestChatView_(Ascii|Color)Snapshot" -v`
Expected: PASS.

Visually inspect ASCII:

Run: `head -30 tui/internal/app/testdata/chat_idle_80x24_ascii.txt`
Expected: `+`, `=`, `|` characters in place of `╔ ═ ║`.

- [ ] **Step 5: Commit**

```bash
git add tui/internal/app/chat_snapshot_test.go tui/internal/app/testdata/chat_idle_80x24_ascii.txt tui/internal/app/testdata/chat_idle_80x24_color.txt
git commit -m "test(tui): ASCII + color snapshot variants for chat surface"
```

---

## Task 13: End-to-end dogfood test

**Files:**
- Manual test, no new files.

- [ ] **Step 1: Build + start daemon**

Run from `/home/ubuntu/gil`:

```bash
go build -o /tmp/gild ./server/cmd/gild
go build -o /tmp/gil  ./cli/cmd/gil
```

Expected: clean builds, no errors.

- [ ] **Step 2: Verify daemon is running**

```bash
pgrep -af gild | head -1
```

Expected: at least one running gild process. If none, start with `/tmp/gild --foreground &` in another shell.

- [ ] **Step 3: Run gil on a TTY (interactive)**

Open an interactive terminal and run:

```bash
/tmp/gil
```

Expected:
- alt-screen activates (terminal scrollback hidden)
- rounded `G I L` header at top
- session list pre-first-turn
- magenta `╔══╗` prompt panel pinned at bottom
- affordance subtitle below the panel
- typing into the panel echoes characters
- Enter sends the prompt; transcript scrolls; first-turn-done flips
- Ctrl+C exits cleanly back to the shell

- [ ] **Step 4: Test resize**

Inside the running TUI:
- shrink terminal width below 60 columns → session-list rows degrade to short form
- shrink height below 16 rows → prompt panel collapses to 3 rows
- restore size → full layout returns

Expected: no panics, no garbled redraws.

- [ ] **Step 5: Test ASCII mode**

```bash
GIL_ASCII=1 /tmp/gil
```

Expected: layout uses `+ - | =` instead of `╔ ═ ╮ ─`. Magenta border still present (single ANSI color works without Unicode).

- [ ] **Step 6: Test NO_COLOR**

```bash
NO_COLOR=1 /tmp/gil
```

Expected: layout intact, all colors stripped, prompt panel border bold instead of magenta.

- [ ] **Step 7: Test non-TTY fallback**

```bash
echo "" | /tmp/gil
```

Expected: home-summary one-shot output (existing behavior, unchanged by Phase 26.6).

- [ ] **Step 8: No commit; if anything from steps 3–6 failed, file a fix and re-run the affected step**

If a regression is found, fix it in the relevant chat_*.go file, regenerate snapshots if their expected output changed (`UPDATE_SNAPSHOTS=1 go test ./tui/internal/app/`), and commit the fix.

---

## Self-Review Notes (for the implementer reading this)

- **Spec coverage**: Each design.md §3 region maps to one chat_view.go helper (Task 5). §4.1 color budget enforced by isolating magenta to `stylePromptBorder` (Task 1). §4.2 glyphs added in Task 2. §4.3 subtitle table is `chatSubtitle()` in Task 5. §5 module placement is Task 9. §6 routing is Task 10. §7 resize is partly in Task 5 (renderPromptPanel collapse) and partly in Task 6 (renderPreFirstTurn narrow-mode). §8 phase ↔ layout is `inputEnabled()` + `chatSubtitle()` + status strip (Tasks 5+8). §9 stays out of scope (no router; `/quit` slash kept). §10 testing is Tasks 11+12+13. §11 acceptance is verified in Task 13.
- **Type consistency**: `chatModel`, `chatInputState`, `chatStreamState`, `ChatPhase` are the canonical names; no method renames mid-plan.
- **No placeholders**: every code block is the actual code to write or paste.
- **Frequent commits**: each task ends in a single commit; test → impl → verify → commit at the task boundary.
