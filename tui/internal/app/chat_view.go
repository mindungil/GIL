package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mindungil/gil/core/version"
)

// chatView assembles the five-region chat surface. The layout was
// rebuilt 2026-05-06 to drop the heavy magenta box + 3-row rounded
// header that read like generic dev-tool chrome (#tui-redesign). The
// new design follows docs/design/terminal-aesthetic.md mission-control
// direction more strictly: borderless 1-row header, thin rounded
// magenta prompt (3 rows, single content line), borderless status pill,
// editorial left-rail + timestamps in transcript.
//
// Region heights at full size (height ≥ 24):
//
//	header        = 1
//	conversation  = remaining
//	status strip  = 1
//	prompt panel  = 3
//	affordance    = 1
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

// renderChatHeader is one row, borderless. Per terminal-aesthetic.md §2
// the brand bar is `▏` quote-glyph + letterspaced bold "G  I  L". A
// dim 4-char rule separates brand from context. cwd / model collapse
// from the right when the terminal is narrow.
//
// Pre-redesign this was a 3-row rounded box that wasted vertical space
// for fields the user only glances at. The new shape mirrors the
// `gil status` visual mode header — same brand, same right-side meta,
// no chrome.
func (m *chatModel) renderChatHeader() string {
	cwd := chatCwd()
	if cwd == "" {
		cwd = "?"
	}
	g := Glyphs()
	left := "   " + styleDim(g.QuoteBar) + "  " + styleHeader("G  I  L")
	rule := "      " + styleDim(strings.Repeat(g.HSep, 4)) + "      "
	right := styleDim(cwd) + "   " + styleMeta("claude-opus-4-7  ·  "+version.String())

	leftAndRule := left + rule
	leftAndRule, right = fitTwoColumn(leftAndRule, right, m.width-3)
	return padBetween(leftAndRule, right, m.width)
}

// renderPromptPanel returns (height, rendered). Thin rounded magenta
// frame, 3 rows tall (top + content + bottom). Magenta is reserved
// for this panel per spec §4.1 — it's the single-purpose accent that
// signals "this is where you type."
//
// Pre-redesign this was a 5-row heavy `╔══╗` box with two blank
// padding rows. The heavy double-line + extra padding read as
// alarming/modal-warning rather than "primary input affordance," and
// the magenta treatment alone is enough emphasis.
func (m *chatModel) renderPromptPanel() (int, string) {
	g := Glyphs()
	// Use light rounded frame in magenta — magenta still owns the
	// prompt-panel-singular rule, but the line weight stops shouting.
	tl, tr := g.BoxLightTL, g.BoxLightTR
	bl, br := g.BoxLightBL, g.BoxLightBR
	hr := g.BoxLightHRule
	vr := g.BoxLightVRule

	bordStyle := stylePromptBorder
	if !m.inputEnabled() {
		bordStyle = stylePromptBorderDim
	}

	// 3-col left margin so the prompt sits inside the page rhythm
	// (matches header indentation).
	const indent = 3
	innerW := m.width - 2*indent - 2 // -2 for the two vertical borders
	if innerW < 4 {
		innerW = 4
	}
	leftPad := strings.Repeat(" ", indent)

	top := leftPad + bordStyle(tl+strings.Repeat(hr, innerW)+tr) + leftPad

	cue := lipgloss.NewStyle().Bold(true).Render(g.Arrow)
	inputView := m.input.ti.View()
	inner := " " + cue + "  " + inputView
	innerLen := lipgloss.Width(inner)
	if innerLen < innerW {
		inner += strings.Repeat(" ", innerW-innerLen)
	}
	mid := leftPad + bordStyle(vr) + inner + bordStyle(vr) + leftPad
	bot := leftPad + bordStyle(bl+strings.Repeat(hr, innerW)+br) + leftPad

	return 3, lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
}

// renderAffordanceLine is the single row of helper text below the
// prompt panel. Phase-aware NL subtitle on the left, stable footer
// hints on the right.
func (m *chatModel) renderAffordanceLine() string {
	subtitle := chatSubtitle(m.phase)
	hints := fmt.Sprintf("%s  %s  %s history  /  %s cmds",
		version.String(), Glyphs().Dot, "↑↓", "/")
	left := "      " + styleMeta(subtitle)
	right := styleMeta(hints) + "   "
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

// renderStatusStrip is a single right-aligned status pill on its own
// row. The previous version drew a full-width `─` rule above the body
// row — that rule is now redundant because the prompt panel's top
// border (immediately below) already provides the visual divider, and
// dropping it reclaims a full row of conversation space.
//
// The pill carries terse phase-state. styleAlert when stuck or
// erroring; mint-cyan info accent when actively running; dim surface
// otherwise.
func (m *chatModel) renderStatusStrip() string {
	body := chatStatusBody(m, Glyphs().Dot)
	pill := chatStripPill(m, body)
	left := "   "
	row := padBetween(left, pill+"   ", m.width)
	return row
}

// chatStripPill wraps body in `[ … ]` brackets and applies a phase-
// appropriate accent. Brackets are always dim so the body is the
// emphasised part of the pill.
func chatStripPill(m *chatModel, body string) string {
	br := styleDim("[ ")
	cl := styleDim(" ]")
	switch {
	case m.err != "":
		return br + styleAlert(body) + cl
	case m.phase == ChatPhaseStuck:
		return br + styleAlert(body) + cl
	case m.phase == ChatPhaseRun:
		return br + styleInfo(body) + cl
	case m.phase == ChatPhaseDone:
		return br + styleSuccess(body) + cl
	default:
		return br + styleSurface(body) + cl
	}
}

// chatStatusBody composes the phase-aware right-side strip text.
// Pure — testable without rendering. Errors win over phase so the
// user always sees the most urgent state on top. The leading
// `<phase>  ·` form was redundant once chatStripPill wraps the result —
// the brackets and accent already tell the user "this is the phase
// pill," so we strip the phase-name redundancy in the body and lead
// with the human reading directly.
func chatStatusBody(m *chatModel, dot string) string {
	if m.err != "" {
		return "error  " + dot + "  " + m.err
	}
	switch m.phase {
	case ChatPhaseIdle:
		return "idle  " + dot + "  ready"
	case ChatPhaseInterview:
		return "interview  " + dot + "  gathering context"
	case ChatPhaseAwaitingConfirm:
		return "interview  " + dot + "  ready to freeze"
	case ChatPhaseRun:
		if m.runIter > 0 || m.runCost > 0 {
			return fmt.Sprintf("run  %s  iter %d  %s  $%.4f",
				dot, m.runIter, dot, m.runCost)
		}
		return "run  " + dot + "  agent working"
	case ChatPhaseStuck:
		if m.stuckPattern != "" {
			return "stuck  " + dot + "  " + humanChatStuckPattern(m.stuckPattern)
		}
		return "stuck  " + dot + "  recovery in progress"
	case ChatPhaseDone:
		if m.runIter > 0 {
			return fmt.Sprintf("done  %s  %d iters  %s  $%.4f",
				dot, m.runIter, dot, m.runCost)
		}
		return "done  " + dot + "  finished"
	}
	return string(m.phase)
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
