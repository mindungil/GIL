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
