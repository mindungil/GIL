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
// frame around the textarea. The panel grows vertically with the
// buffer's row count (1..chatInputMaxRows) so multi-line composition
// has somewhere to go without pushing the conversation off-screen.
//
// The cue glyph `›` sits flush in the panel's gutter on every line
// (when multi-line) so the user always sees where their text is going.
// Magenta is reserved for this panel per spec §4.1 — it's the
// single-purpose accent that signals "this is where you type."
func (m *chatModel) renderPromptPanel() (int, string) {
	g := Glyphs()
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

	// Cue gutter: " ›  " on the first line, "    " on continuation
	// lines. Reserve those 4 cells so the textarea's own line width
	// matches the panel inner width and doesn't push the right
	// border out at narrow terminals (also defends against the
	// snapshot-test path that wires m.width without firing
	// WindowSizeMsg, which would otherwise leave the textarea at
	// its default width).
	const cueWidth = 4
	taW := innerW - cueWidth
	if taW < 1 {
		taW = 1
	}
	m.input.ta.SetWidth(taW)
	// Sync textarea visible height with the panel row count so the
	// View() output produces exactly the right number of rendered
	// lines. Update() also calls this on every keystroke; the
	// repeat here keeps snapshot tests (which bypass Update via
	// direct SetValue) in agreement.
	m.input.ta.SetHeight(m.input.rowCount())

	top := leftPad + bordStyle(tl+strings.Repeat(hr, innerW)+tr) + leftPad
	bot := leftPad + bordStyle(bl+strings.Repeat(hr, innerW)+br) + leftPad

	cue := lipgloss.NewStyle().Bold(true).Render(g.Arrow)
	rows := m.input.rowCount()
	view := m.input.ta.View()
	viewLines := strings.Split(view, "\n")
	// Pad to exactly rows so the panel doesn't collapse mid-line on
	// short buffers.
	for len(viewLines) < rows {
		viewLines = append(viewLines, "")
	}
	if len(viewLines) > rows {
		viewLines = viewLines[:rows]
	}

	mids := make([]string, 0, rows)
	for i, line := range viewLines {
		// Only the first row gets the `›` cue; subsequent rows get
		// blank padding so multi-line buffers read as a single
		// continuous text block instead of a list of arrows.
		var prefix string
		if i == 0 {
			prefix = " " + cue + "  "
		} else {
			prefix = "    "
		}
		// Pad / truncate inner content to exactly innerW so the
		// right border aligns even when the textarea's line is
		// shorter (most cases) or longer (edge: textarea internal
		// padding for caret rendering).
		body := prefix + line
		w := lipgloss.Width(body)
		switch {
		case w < innerW:
			body += strings.Repeat(" ", innerW-w)
		case w > innerW:
			body = truncate(body, innerW)
		}
		mids = append(mids,
			leftPad+bordStyle(vr)+body+bordStyle(vr)+leftPad)
	}

	all := append([]string{top}, mids...)
	all = append(all, bot)
	return 2 + rows, lipgloss.JoinVertical(lipgloss.Left, all...)
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
			body := fmt.Sprintf("run  %s  iter %d  %s  $%.4f",
				dot, m.runIter, dot, m.runCost)
			// Tokens / latency only show when the daemon has emitted
			// them — otherwise the pill stays at 3 cells. Tokens
			// compact to k at 1000+, latency to s at 1000ms+.
			if m.runTokens > 0 {
				body += "  " + dot + "  " + formatChatTokens(m.runTokens) + " toks"
			}
			if m.runLatencyMs > 0 {
				body += "  " + dot + "  " + formatChatLatency(m.runLatencyMs)
			}
			return body
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
//
// Each transcript line is decorated with a dim left-rail `▏` glyph at
// render time per terminal-aesthetic.md §2 ("Quote / log line — left
// ▏ margin + dim"). The rail is render-only — it never appears in the
// stored transcript strings — so coalescing logic in Update() that
// checks `strings.HasPrefix(line, "‹")` still matches.
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
	rail := styleDim(Glyphs().QuoteBar) + " "
	decorated := make([]string, len(m.transcript[start:]))
	for i, line := range m.transcript[start:] {
		decorated[i] = "  " + rail + line
	}
	body := strings.Join(decorated, "\n")
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
