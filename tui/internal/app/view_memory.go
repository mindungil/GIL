package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mindungil/gil/core/paths"
)

// MemoryExcerpt is a small snapshot of progress.md.
type MemoryExcerpt struct {
	Lines    []string  // up to 6 bullet-worthy non-empty lines
	ModTime  time.Time // file mtime; zero when missing
	NotFound bool      // true when the file isn't on disk
}

// loadMemoryExcerpt reads <SessionsDir>/<sessionID>/memory/progress.md
// and returns up to 6 informative lines (non-empty, non-heading) for the
// memory pane. Returns NotFound=true (no error) when the file simply
// doesn't exist yet — that's the normal state for a brand-new session
// before any milestone has fired.
//
// The path layout matches RunService.sessionDir: <Data>/sessions/<id>/
// memory/progress.md (see core/paths/xdg.go: SessionsDir(), and
// server/internal/service/run.go: bank := memory.New(...)).
func loadMemoryExcerpt(sessionID string) MemoryExcerpt {
	if sessionID == "" {
		return MemoryExcerpt{NotFound: true}
	}
	layout, err := paths.FromEnv()
	if err != nil {
		return MemoryExcerpt{NotFound: true}
	}
	p := filepath.Join(layout.SessionsDir(), sessionID, "memory", "progress.md")
	st, err := os.Stat(p)
	if err != nil {
		return MemoryExcerpt{NotFound: true}
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return MemoryExcerpt{NotFound: true}
	}
	lines := extractInformative(string(body), 6)
	return MemoryExcerpt{Lines: lines, ModTime: st.ModTime()}
}

// extractInformative returns up to maxLines non-blank, non-heading
// lines from a progress.md, stripped of leading list markers. Lines
// like "## Done" / "## In Progress" / "## Blocked" are filtered (they
// are scaffolding, not content); list items "- foo" / "* foo" → "foo".
func extractInformative(body string, maxLines int) []string {
	out := []string{}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Strip list bullet.
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimPrefix(line, "+ ")
		if line == "" || line == "(none)" {
			continue
		}
		out = append(out, line)
		if len(out) >= maxLines {
			break
		}
	}
	return out
}

// renderMemoryPane renders the memory excerpt content (without border).
// Title-line is composed by the caller (paneFrame). width is content
// width.
func renderMemoryPane(width int, m MemoryExcerpt) string {
	g := Glyphs()
	if m.NotFound || len(m.Lines) == 0 {
		return styleDim("(memory bank not yet populated)")
	}
	var sb strings.Builder
	for i, ln := range m.Lines {
		body := truncate(ln, max(width-3, 10))
		sb.WriteString(styleDim(g.Bullet))
		sb.WriteString(" ")
		sb.WriteString(styleSurface(body))
		if i < len(m.Lines)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// memoryPaneTitle returns the dynamic title for the memory pane:
// "Memory (progress.md, 2m ago)" or "Memory (progress.md, missing)".
// When the file exists but ModTime is unset (zero value) we fall back
// to "missing" rather than printing a meaningless huge duration.
func memoryPaneTitle(m MemoryExcerpt) string {
	if m.NotFound || m.ModTime.IsZero() {
		return "Memory (progress.md, missing)"
	}
	return fmt.Sprintf("Memory (progress.md, %s)", relTimeShort(time.Since(m.ModTime)))
}

// relTimeShort returns "2m ago" / "37s ago" / "1h12m ago" — meant for
// section titles where a precise timestamp is overkill.
func relTimeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh%dm ago", h, m)
}
