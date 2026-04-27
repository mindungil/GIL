package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	modalBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("11")).
			Padding(1, 2).
			Background(lipgloss.Color("0")).
			Foreground(lipgloss.Color("15"))
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedRow = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
	paneBorder  = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Background(lipgloss.Color("8")).Foreground(lipgloss.Color("15")).Padding(0, 1)
)

// View implements tea.Model.
func (m *Model) View() string {
	base := m.viewBase()
	// Slash prompt + last-output panel sit between the body and the
	// permission modal so users still see them while a command is in
	// flight; permission asks remain on top because they require an
	// immediate y/n response.
	base = overlaySlash(base, m.slash, m.width)
	if m.pendingAsk == nil {
		return base
	}
	return overlayModal(base, m.pendingAsk, m.width)
}

// viewBase renders the normal TUI without any modal overlay.
func (m *Model) viewBase() string {
	if m.width == 0 {
		// First render before WindowSizeMsg
		return "loading…"
	}
	leftWidth := m.width / 3
	if leftWidth < 30 {
		leftWidth = 30
	}
	rightWidth := m.width - leftWidth - 1

	paneHeight := m.height - 2 // reserve status bar + title

	left := m.renderSessionList(leftWidth, paneHeight)
	right := m.renderSessionDetail(rightWidth, paneHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		paneBorder.Width(leftWidth).Height(paneHeight).Render(left),
		right,
	)

	title := titleStyle.Render(" gil ") + lipgloss.NewStyle().Faint(true).Render(" — autonomous coding harness")
	status := m.renderStatus()
	return lipgloss.JoinVertical(lipgloss.Left, title, body, status)
}

// overlaySlash appends the slash-command input prompt and/or the last
// command's output below the base view. nil-safe so unit tests that
// build a Model literal without slash state still render.
func overlaySlash(base string, st *slashState, w int) string {
	if st == nil {
		return base
	}
	out := base
	if st.output != "" {
		// Faint border to distinguish from the permission modal (yellow).
		panel := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1).
			Render(st.output)
		out = lipgloss.JoinVertical(lipgloss.Left, out, panel)
	}
	if st.inputting {
		prompt := lipgloss.NewStyle().
			Background(lipgloss.Color("4")).
			Foreground(lipgloss.Color("15")).
			Padding(0, 1).
			Width(w).
			Render("/" + st.buffer + "_")
		out = lipgloss.JoinVertical(lipgloss.Left, out, prompt)
	}
	return out
}

// overlayModal appends a permission-request dialog below the base view.
// (A true center-overlay would require terminal cell arithmetic; the
// appended-box approach is sufficient — refining the visual placement
// is a future polish pass.)
//
// The six visible options map to permissionKeyToDecision in
// tui/internal/app/permission.go. The phrasing follows codex's
// 3-tier ladder (once/session/always) extended symmetrically to the
// deny side, the way cline's CommandPermissionController organises its
// allow/deny lists.
func overlayModal(base string, ask *pendingAskMsg, w int) string {
	box := modalBorder.Render(fmt.Sprintf(
		"The agent wants to run: %s %s\n\n"+
			"[a] Allow once         [s] Allow session      [A] Always allow\n"+
			"[d] Deny once                                 [D] Always deny\n"+
			"[Esc] Cancel (deny once)",
		ask.Tool, truncate(ask.Key, max(w-30, 10)),
	))
	return lipgloss.JoinVertical(lipgloss.Left, base, "", box)
}

func (m *Model) renderSessionList(w, h int) string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Sessions") + "\n\n")
	if len(m.sessions) == 0 {
		sb.WriteString("(no sessions — run 'gil new' first)")
		return sb.String()
	}
	for i, s := range m.sessions {
		line := fmt.Sprintf("%-12s %s", s.Status, truncate(s.GoalHint, w-16))
		if i == m.selectedIdx {
			line = selectedRow.Render(line)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func (m *Model) renderSessionDetail(w, h int) string {
	if len(m.sessions) == 0 || m.selectedIdx >= len(m.sessions) {
		return "\n  No session selected.\n  Press 'r' to refresh."
	}
	s := m.sessions[m.selectedIdx]
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Session "+s.ID) + "\n\n")
	fmt.Fprintf(&sb, "  Status:       %s\n", s.Status)
	fmt.Fprintf(&sb, "  Working dir:  %s\n", s.WorkingDir)
	fmt.Fprintf(&sb, "  Goal:         %s\n", s.GoalHint)
	if s.CurrentIteration > 0 {
		fmt.Fprintf(&sb, "  Iteration:    %d\n", s.CurrentIteration)
	}
	if s.CurrentTokens > 0 {
		fmt.Fprintf(&sb, "  Tokens:       %d\n", s.CurrentTokens)
	}
	sb.WriteString("\n  Events (live tail):\n")
	if len(m.events) == 0 {
		if s.Status == "RUNNING" {
			sb.WriteString("  (no events yet — waiting for next…)\n")
		} else {
			sb.WriteString("  (session not running; live tail unavailable)\n")
		}
	} else {
		// Show last 20 events that fit in the remaining pane height.
		show := 20
		if h-15 < show {
			show = max(0, h-15)
		}
		start := len(m.events) - show
		if start < 0 {
			start = 0
		}
		for _, line := range m.events[start:] {
			sb.WriteString("  " + line + "\n")
		}
	}
	return sb.String()
}

func (m *Model) renderStatus() string {
	keys := "k/↓: down  j/↑: up  r: refresh  /: command  q: quit"
	if m.err != "" {
		keys = "ERROR: " + m.err + "    " + keys
	}
	return statusStyle.Width(m.width).Render(keys)
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
