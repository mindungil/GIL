package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mindungil/gil/core/version"
)

// View implements tea.Model. The 4-pane mission-control layout is per
// spec §6 (Layout — 4-pane TUI). Modals overlay below the body.
func (m *Model) View() string {
	if m.width == 0 {
		// First render before WindowSizeMsg.
		return styleDim("loading" + Glyphs().Ellipsis)
	}

	// Degraded mode: width < 80 → hide sessions pane, main only.
	narrow := m.width < 80

	header := renderHeader(m.width)
	body := m.renderBody(narrow)
	footer := m.renderFooter()
	base := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)

	// Slash overlay (input prompt + last-output panel) sits between body
	// and modal so the user can still see them while a command is in
	// flight; permission/checkpoint modals stay on top.
	base = overlaySlash(base, m.slash, m.width)

	if m.checkpoints.Open {
		modal := renderCheckpointModal(min(m.width-4, 70), m.checkpoints)
		return lipgloss.JoinVertical(lipgloss.Left, base, "", modal)
	}
	if m.pendingAsk != nil {
		modal := renderPermissionModal(m.pendingAsk, min(m.width-4, 70))
		return lipgloss.JoinVertical(lipgloss.Left, base, "", modal)
	}
	return base
}

// renderHeader composes the single-line top bar per spec §6:
//   ╭─ G I L   ─  v0.1.0-alpha  ─────────────  user  ●  host ─╮
//
// We don't actually wrap the header in box-drawing characters because
// the body already brings its own borders; instead we render a plain
// line that visually groups the same information.
func renderHeader(width int) string {
	g := Glyphs()
	leftLabel := styleHeader("G I L") + "   " + styleDim(version.String())
	uname := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		uname = u.Username
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "local"
	}
	right := styleSurface(uname) + "  " + styleInfo(g.Running) + "  " + styleDim(host)
	return padBetween(leftLabel, right, width)
}

// padBetween renders left and right separated by exactly enough spaces
// that their concatenation has visual width `total`. Strips ANSI from
// the visual length calculation so the math works on styled strings.
func padBetween(left, right string, total int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := total - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// renderBody composes the 4-pane (or 1-pane in narrow mode) body.
func (m *Model) renderBody(narrow bool) string {
	// Header and footer each consume 1 row; the body fills the rest.
	bodyHeight := m.height - 2
	if bodyHeight < 8 {
		bodyHeight = 8
	}
	if narrow {
		mainWidth := m.width
		return m.renderMainColumn(mainWidth, bodyHeight)
	}
	leftWidth := m.width / 4
	if leftWidth < 22 {
		leftWidth = 22
	}
	rightWidth := m.width - leftWidth - 1

	left := m.renderSessionsPane(leftWidth, bodyHeight)
	right := m.renderMainColumn(rightWidth, bodyHeight)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// renderMainColumn stacks the three right-side sub-panes (Spec/Progress,
// Activity, Memory) inside a single column.
func (m *Model) renderMainColumn(width, height int) string {
	// Allocate height: progress=10, activity=fills, memory=8.
	progressH := 10
	memoryH := 8
	activityH := height - progressH - memoryH
	if activityH < 4 {
		activityH = 4
	}

	pd := m.progress
	if pd.Goal == "" && len(m.sessions) > 0 && m.selectedIdx < len(m.sessions) {
		s := m.sessions[m.selectedIdx]
		pd.Goal = s.GoalHint
		pd.Iter = s.CurrentIteration
		if pd.MaxIter == 0 {
			pd.MaxIter = 100
		}
		pd.TokensIn = s.CurrentTokens
		if pd.Autonomy == "" {
			pd.Autonomy = "ASK_DESTRUCTIVE"
		}
	}
	progress := paneBox("Spec & Progress", width, progressH,
		renderProgressPane(width-4, pd))

	rows := activityFromEvents(m.rawEvents, m.activityFilter, max(activityH-2, 1))
	activityTitle := fmt.Sprintf("Activity (%s)", m.activityFilter.String())
	activity := paneBox(activityTitle, width, activityH,
		renderActivityPane(width-4, activityH-2, rows, m.spinFrame))

	memory := paneBox(memoryPaneTitle(m.memory), width, memoryH,
		renderMemoryPane(width-4, m.memory))

	return lipgloss.JoinVertical(lipgloss.Left, progress, activity, memory)
}

// renderSessionsPane is the left 25% pane.
func (m *Model) renderSessionsPane(width, height int) string {
	g := Glyphs()
	var sb strings.Builder
	if len(m.sessions) == 0 {
		sb.WriteString(styleDim("(no sessions — run 'gil new' first)"))
	}
	for i, s := range m.sessions {
		shortID := s.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		statusWord := s.Status
		if len(statusWord) > 7 {
			statusWord = statusWord[:7]
		}
		row1 := fmt.Sprintf("%s  %s", statusGlyph(g, s.Status), styleSurface(shortID))
		row2 := fmt.Sprintf("    %s  %s",
			styleDim(statusWord),
			styleDim(fmt.Sprintf("iter %d", s.CurrentIteration)))
		if i == m.selectedIdx {
			row1 = styleSelectedBg(stripPrefixSpace(row1, width-4))
			row2 = styleSelectedBg(stripPrefixSpace(row2, width-4))
		}
		sb.WriteString(row1)
		sb.WriteString("\n")
		sb.WriteString(row2)
		sb.WriteString("\n\n")
	}
	return paneBox("Sessions", width, height, sb.String())
}

// stripPrefixSpace pads (or truncates) a styled string to the given
// visual width so background-color highlight covers the full row.
func stripPrefixSpace(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// paneBox wraps content in the canonical rounded light frame with the
// given title baked into the top border. The frame has 0×1 padding (no
// vertical pad — the content already provides spacing).
func paneBox(title string, width, height int, content string) string {
	frame := paneFrame(title)
	// Title strip glued onto top border: "╭─ Title ─...─╮"
	// We render the frame normally (lipgloss doesn't support inline
	// titles), then overlay the title onto the top border line via a
	// post-hoc replace.
	rendered := frame.Width(width - 2).Height(height - 2).Render(content)
	if title == "" {
		return rendered
	}
	return injectTitle(rendered, title)
}

// injectTitle overwrites the top border of a pre-rendered pane with
// "╭─ <title> ─...─╮" so the section title sits inline. Operates on the
// first line only; falls through if the line doesn't look like a
// rounded-border top.
func injectTitle(box, title string) string {
	idx := strings.Index(box, "\n")
	if idx < 0 {
		return box
	}
	first := box[:idx]
	rest := box[idx:]
	// Find the unicode column count of the original line.
	origW := lipgloss.Width(first)
	// Build replacement: "╭─ <title> " then dashes then "╮".
	g := Glyphs()
	left := "╭" + g.HSep + " " + styleHeader(title) + " "
	if asciiMode {
		left = "+" + "- " + styleHeader(title) + " "
	}
	leftW := lipgloss.Width(left)
	if leftW >= origW-1 {
		return box // not enough room — leave untouched.
	}
	dashes := strings.Repeat(g.HSep, origW-leftW-1)
	if asciiMode {
		dashes = strings.Repeat("-", origW-leftW-1)
	}
	corner := "╮"
	if asciiMode {
		corner = "+"
	}
	newLine := left + styleDim(dashes) + styleDim(corner)
	return newLine + rest
}

// renderFooter is the dim keymap line per spec §6.
func (m *Model) renderFooter() string {
	g := Glyphs()
	keys := []string{
		"q quit",
		"r refresh",
		"/ commands",
		"c checkpoints",
		"t toggle activity",
	}
	if m.checkpoints.Open {
		keys = []string{"↑/↓ select", "enter restore", "esc close"}
	}
	if m.pendingAsk != nil {
		keys = []string{"a/s/A allow", "d/D deny", "esc cancel"}
	}
	body := strings.Join(keys, "  "+g.Dot+"  ")
	if m.err != "" {
		body = styleAlert(g.Failed+" "+m.err) + "  " + g.Dot + "  " + body
	}
	return styleDim(body)
}

// renderPermissionModal is the restyled 6-tier permission ask per
// spec §11. Light rounded frame, no bg fill, accent on the headline.
func renderPermissionModal(ask *pendingAskMsg, width int) string {
	header := styleCritical("Permission")
	intro := styleSurface("The agent wants to run")
	cmd := styleEmphasis(ask.Tool) + " " + styleSurface(truncate(ask.Key, max(width-12, 10)))
	row1 := strings.Join([]string{
		"[a] allow once",
		"[s] allow session",
		"[A] allow always",
	}, "    ")
	row2 := strings.Join([]string{
		"[d] deny once",
		"                ",
		"[D] deny always",
	}, "    ")
	footer := styleDim("[esc] cancel (= deny once)")
	body := strings.Join([]string{
		header,
		"",
		intro,
		"",
		"   " + cmd,
		"",
		row1,
		row2,
		"",
		footer,
	}, "\n")
	frame := paneFrame("").Padding(1, 2)
	return frame.Render(body)
}

// overlaySlash appends the slash-command input prompt and/or the last
// command's output below the base view. nil-safe so unit tests that
// build a Model literal without slash state still render.
//
// Per spec §13: rounded light frame, no bg fill on the prompt; accent
// only on the leading `›` arrow.
func overlaySlash(base string, st *slashState, w int) string {
	if st == nil {
		return base
	}
	out := base
	g := Glyphs()
	if st.output != "" {
		panel := paneFrame("").Render(st.output)
		out = lipgloss.JoinVertical(lipgloss.Left, out, panel)
	}
	if st.inputting {
		prompt := styleEmphasis(g.Arrow) + " " + styleSurface("/"+st.buffer+"_")
		out = lipgloss.JoinVertical(lipgloss.Left, out, prompt)
	}
	return out
}

// truncate fits s into max visual columns, replacing the tail with the
// active ellipsis glyph (Unicode `…` or ASCII `...`). Returns s
// unchanged when it already fits. For very small max (<= 3) we hard-
// truncate without an ellipsis since the visible savings disappear.
func truncate(s string, max int) string {
	if max <= 0 || lipgloss.Width(s) <= max {
		return s
	}
	if max <= 3 {
		// Hard truncate; matches the pre-Phase-14 contract used by
		// existing callers / tests that pass tiny widths.
		if max > len(s) {
			max = len(s)
		}
		return s[:max]
	}
	g := Glyphs()
	ellipsisW := lipgloss.Width(g.Ellipsis)
	if max <= ellipsisW {
		return s[:max]
	}
	cut := max - ellipsisW
	if cut > len(s) {
		cut = len(s)
	}
	return s[:cut] + g.Ellipsis
}

// max / min helpers (Go 1.21+ has builtins; we keep the explicit forms
// for clarity in long view-code expressions).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// itoa is a tiny strconv shortcut used by the checkpoint notice text.
func itoa(n int) string { return strconv.Itoa(n) }

// jsonUnmarshalQuiet ignores parse errors — useful for "best-effort"
// inspection of event payloads where a malformed message must not
// crash the TUI.
func jsonUnmarshalQuiet(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
