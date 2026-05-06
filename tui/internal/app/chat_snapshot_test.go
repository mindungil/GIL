package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mindungil/gil/core/version"
	"github.com/muesli/termenv"
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
	prevCwd := chatCwd
	SetNoColor(true)
	SetAsciiMode(false)
	chatCwd = func() string { return "/snap/cwd" }
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)
	defer func() { chatCwd = prevCwd }()

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

func TestChatView_AsciiSnapshot(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	prevCwd := chatCwd
	SetNoColor(true)
	SetAsciiMode(true)
	chatCwd = func() string { return "/snap/cwd" }
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)
	defer func() { chatCwd = prevCwd }()

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

// TestRenderAgentMarkdown_PassesThroughPlainText verifies the
// markdown renderer leaves prose alone when no markdown markers are
// present. We only want to pay glamour's layout cost when there's
// real structure to style.
func TestRenderAgentMarkdown_PassesThroughPlainText(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(false)
	defer SetNoColor(prevNoColor)

	body := "Sure, I can help with that. Let me start by reading the file."
	if got := renderAgentMarkdown(body); got != body {
		t.Errorf("plain text should pass through unchanged.\nwant: %q\n got: %q", body, got)
	}
}

// TestRenderAgentMarkdown_RendersFencedCode verifies fenced code
// blocks survive glamour rendering and produce multi-line output.
// We don't pin the exact ANSI bytes — chroma styles drift with
// glamour upgrades — but we do check that the rendered output is
// (a) different from the source and (b) contains the code's literal
// payload somewhere inside.
func TestRenderAgentMarkdown_RendersFencedCode(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	SetNoColor(false)
	SetAsciiMode(false)
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)

	body := "Here's the function:\n\n```go\nfunc main() {\n    fmt.Println(\"hi\")\n}\n```"
	got := renderAgentMarkdown(body)
	if got == body {
		t.Fatalf("expected glamour-rendered output, got verbatim source")
	}
	// chroma splits keywords + identifiers into separate ANSI runs
	// (`func` ... `main` ... `()`), so a literal substring assertion
	// on "func main()" fails. Check that the keyword and identifier
	// each appear somewhere in the rendered output instead.
	for _, token := range []string{"func", "main", "fmt", "Println"} {
		if !strings.Contains(got, token) {
			t.Errorf("rendered output missing code token %q: %q", token, got)
		}
	}
	// At minimum the rendered output should be visibly different
	// from the source — non-trivial ANSI volume confirms styling
	// happened.
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("rendered code block should contain ANSI escapes; got %q", got)
	}
}

// TestRenderAgentMarkdown_NoColorNoOps confirms NO_COLOR users see
// raw text, not glamour-rendered ANSI. NO_COLOR's contract is "no
// styling," so even the inline-code highlight should be absent.
func TestRenderAgentMarkdown_NoColorNoOps(t *testing.T) {
	prevNoColor := IsNoColor()
	SetNoColor(true)
	defer SetNoColor(prevNoColor)

	body := "Use `gil status` to list sessions."
	if got := renderAgentMarkdown(body); got != body {
		t.Errorf("NO_COLOR should bypass glamour\nwant: %q\n got: %q", body, got)
	}
}

// TestChatView_MultilineInput pins the prompt panel's vertical
// growth as the user composes a multi-line buffer. The textarea
// reports LineCount → chatInputState.rowCount() → renderPromptPanel
// uses (2 + rows) so the panel grows from 3 rows for a single line
// up to (2 + chatInputMaxRows) rows for a fully-filled buffer.
func TestChatView_MultilineInput(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	prevCwd := chatCwd
	SetNoColor(true)
	SetAsciiMode(false)
	chatCwd = func() string { return "/snap/cwd" }
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)
	defer func() { chatCwd = prevCwd }()

	m := newChatModel("/tmp/test.sock")
	m.width = 80
	m.height = 24
	m.phase = ChatPhaseIdle
	// Prefill a 3-line buffer to exercise the multi-line path.
	m.input.ta.SetValue("first line of a long prompt\nsecond line continues\nand a third")

	got := stripDynamic(m.View())
	path := filepath.Join("testdata", "chat_multiline_80x24.txt")

	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing snapshot %s — run UPDATE_SNAPSHOTS=1", path)
	}
	if got != string(want) {
		t.Errorf("multiline snapshot mismatch:\n--- want ---\n%s\n--- got ---\n%s", string(want), got)
	}
}

// TestChatView_RunPhaseSnapshot pins the look of the chat surface
// mid-run so future aesthetic changes have to consciously rebase the
// transcript treatment (left rail, glyph prefixes, status pill colour).
func TestChatView_RunPhaseSnapshot(t *testing.T) {
	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	prevCwd := chatCwd
	SetNoColor(true)
	SetAsciiMode(false)
	chatCwd = func() string { return "/snap/cwd" }
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)
	defer func() { chatCwd = prevCwd }()

	m := newChatModel("/tmp/test.sock")
	m.width = 100
	m.height = 32
	m.phase = ChatPhaseRun
	m.firstTurnDone = true
	m.runIter = 4
	m.runCost = 0.1834
	m.runTokens = 4231
	m.runLatencyMs = 1240
	m.transcript = []string{
		"›  build me a CLI that scans go files for TODO comments",
		"‹  Sure. I'll start by mapping the repo and then build a single-file scanner.",
		"   ‹  agent run started",
		"   ‹  iter 1 · $0.0042",
		"   ‹  ⚒ Read  main.go",
		"   ‹  ⚒ Read → ok",
		"‹  Reading main.go. Found 12 functions, no TODO scanner yet.",
		"   ‹  iter 2 · $0.0411",
		"   ‹  ⚒ Edit  main.go",
		"   ‹  ⚒ Edit → ok",
		"   ‹  iter 3 · $0.1102",
		"   ‹  ⚒ Bash  go test ./...",
		"   ‹  ⚒ Bash → ok",
	}

	got := stripDynamic(m.View())
	path := filepath.Join("testdata", "chat_run_100x32.txt")

	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing snapshot %s — run UPDATE_SNAPSHOTS=1", path)
	}
	if got != string(want) {
		t.Errorf("run-phase snapshot mismatch:\n--- want ---\n%s\n--- got ---\n%s", string(want), got)
	}
}

func TestChatView_ColorSnapshot(t *testing.T) {
	// Inverse of the NO_COLOR snapshot — verifies magenta SGR is
	// emitted on the prompt panel border when colors are on.
	// Force TrueColor so lipgloss emits ANSI even outside a real TTY
	// (matches the pattern in TestPromptBorderStyles).
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prevProfile)

	prevNoColor := IsNoColor()
	prevAscii := IsAsciiMode()
	prevCwd := chatCwd
	SetNoColor(false)
	SetAsciiMode(false)
	chatCwd = func() string { return "/snap/cwd" }
	defer SetNoColor(prevNoColor)
	defer SetAsciiMode(prevAscii)
	defer func() { chatCwd = prevCwd }()

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
