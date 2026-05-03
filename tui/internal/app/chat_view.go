package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mindungil/gil/core/version"
)

// chatView assembles the five-region layout described in
// docs/plans/phase-26.6-prompt-centric-tui.md §3.1.
//
// Region heights at full size (height >= 24):
//
//	header        = 3
//	prompt panel  = 5
//	affordance    = 1
//	status strip  = 2
//	conversation  = remaining (>= 8)
func (m *chatModel) chatView() string {
	if m.width == 0 || m.height == 0 {
		return styleDim("loading" + Glyphs().Ellipsis)
	}

	header := m.renderChatHeader()
	promptH, prompt := m.renderPromptPanel()
	affordance := m.renderAffordanceLine()
	statusStrip := m.renderStatusStrip()

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

// chatCwd is indirected through a package var so snapshot tests can
// pin it to a stable value across machines (real cwd length varies and
// would otherwise change where the header right-side gets truncated).
var chatCwd = func() string {
	cwd, _ := os.Getwd()
	return cwd
}

// renderChatHeader is the rounded box at the top per design §3.
func (m *chatModel) renderChatHeader() string {
	g := Glyphs()
	cwd := chatCwd()
	if cwd == "" {
		cwd = "?"
	}
	left := styleDim(g.QuoteBar) + "  " + styleHeader("G  I  L")
	right := styleDim(cwd) + "   " + styleMeta("claude-opus-4-7  "+g.Dot+"  "+version.String())
	left, right = fitTwoColumn(left, right, m.width-4)
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

// renderPromptPanel returns (height, rendered). Heavy magenta box —
// the visual focal point per design §3.
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

	cue := lipgloss.NewStyle().Bold(true).Render(g.Arrow)
	inputView := m.input.ti.View()
	inner := "  " + cue + "  " + inputView
	innerLen := lipgloss.Width(inner)
	if innerLen < m.width-2 {
		inner += strings.Repeat(" ", m.width-2-innerLen)
	}
	pad := bordStyle(vr) + strings.Repeat(" ", m.width-2) + bordStyle(vr)
	mid := bordStyle(vr) + inner + bordStyle(vr)

	if m.height < 16 {
		return 3, lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
	}
	return 5, lipgloss.JoinVertical(lipgloss.Left, top, pad, mid, pad, bot)
}

// renderAffordanceLine is the single row of helper text below the
// prompt panel.
func (m *chatModel) renderAffordanceLine() string {
	subtitle := chatSubtitle(m.phase)
	hints := fmt.Sprintf("%s  %s  %s history  /  %s cmds",
		version.String(), Glyphs().Dot, "↑↓", "/")
	left := "      " + styleMeta(subtitle)
	right := styleMeta(hints)
	left, right = fitTwoColumn(left, right, m.width)
	return padBetween(left, right, m.width)
}

// chatSubtitle maps phase → user-typeable verbs in NL form per design §4.3.
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
	if m.err != "" {
		body = styleAlert("error: " + m.err)
	}
	right := styleMeta(body)
	left := strings.Repeat(" ", 6)
	row := padBetween(left, right, m.width)
	return lipgloss.JoinVertical(lipgloss.Left, rule, row)
}

// renderConversation fills the middle region. Pre-first-turn shows the
// past-session list; after the first turn the transcript replaces it.
func (m *chatModel) renderConversation(convH int) string {
	if !m.firstTurnDone {
		return m.renderPreFirstTurn(convH)
	}
	if len(m.transcript) == 0 {
		return strings.Repeat("\n", convH-1)
	}
	start := 0
	if len(m.transcript) > convH {
		start = len(m.transcript) - convH
	}
	body := strings.Join(m.transcript[start:], "\n")
	lines := strings.Count(body, "\n") + 1
	if lines < convH {
		body += strings.Repeat("\n", convH-lines)
	}
	return body
}

// inputEnabled reports whether the prompt panel should accept input.
// False during run/stuck phases.
func (m *chatModel) inputEnabled() bool {
	return m.phase != ChatPhaseRun && m.phase != ChatPhaseStuck
}

// fitTwoColumn shrinks left/right so their combined visual width plus a
// 1-cell gap fits inside total. Right is sacrificed first; if left
// still doesn't fit, it is truncated and right is dropped.
func fitTwoColumn(left, right string, total int) (string, string) {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 <= total {
		return left, right
	}
	if room := total - lw - 1; room >= 0 {
		if room == 0 {
			return left, ""
		}
		return left, truncate(right, room)
	}
	return truncate(left, total), ""
}
