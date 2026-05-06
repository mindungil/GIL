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
