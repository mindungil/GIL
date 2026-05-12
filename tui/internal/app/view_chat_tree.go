package app

import (
	"fmt"
	"strings"
)

// view_chat_tree.go — M6 Option A render. Surfaces the AgentTree
// (built from chat-mode tool_call / tool_result events) in giltui's
// main column so the user sees chat agent activity inside the
// mission-control layout. Kept in its own file so view.go stays
// focused on the 4-pane lifecycle.

// renderChatTreePane returns the body string for an "Agent Tree"
// pane. Empty string when the tree has no turns (caller skips the
// pane). Each turn renders as one summary line + its tool calls
// indented; closed turns dim the timestamp.
//
// width is the inner width budget (after the paneBox border).
func renderChatTreePane(width int, tree *AgentTree, maxRows int) string {
	if tree == nil || len(tree.Turns) == 0 {
		return ""
	}
	if maxRows < 1 {
		maxRows = 1
	}
	g := Glyphs()
	var lines []string
	// Walk newest-first so the active turn dominates the top when the
	// pane is tight on rows.
	for i := len(tree.Turns) - 1; i >= 0; i-- {
		turn := tree.Turns[i]
		head := turnHeadLine(g, turn)
		lines = append(lines, head)
		if !turn.Expanded && turn.Closed {
			// Collapsed closed turns show only the head — keeps history
			// readable when there are many turns.
			continue
		}
		for _, node := range turn.Children {
			lines = append(lines, "  "+nodeLine(g, node))
		}
	}
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	// Truncate per-line to the width budget so long inputs don't blow
	// up the layout.
	for i, line := range lines {
		if len(line) > width {
			lines[i] = takeCells(line, width-1) + g.Ellipsis
		}
	}
	return strings.Join(lines, "\n")
}

// turnHeadLine renders the per-turn summary: "Turn N · ⚒ X · ✓ Y · ✗ Z".
func turnHeadLine(g Glyph, turn *AgentTurn) string {
	running, ok, failed := 0, 0, 0
	for _, n := range turn.Children {
		switch n.Status {
		case NodeRunning:
			running++
		case NodeOK:
			ok++
		case NodeFailed:
			failed++
		}
	}
	tag := "Turn"
	if !turn.Closed {
		tag = styleEmphasis("Turn")
	} else {
		tag = styleDim(tag)
	}
	parts := []string{fmt.Sprintf("%s %d", tag, turn.Number)}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%s %d running", "⚒", running))
	}
	if ok > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", g.Done, ok))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", g.Failed, failed))
	}
	return strings.Join(parts, " "+g.Dot+" ")
}

// nodeLine renders one tool call: "⚒ name args" with a status glyph.
func nodeLine(g Glyph, node *AgentTreeNode) string {
	var statusGlyph string
	switch node.Status {
	case NodeRunning:
		statusGlyph = "⚒"
	case NodeOK:
		statusGlyph = g.Done
	case NodeFailed:
		statusGlyph = g.Failed
	default:
		statusGlyph = "⚒"
	}
	line := fmt.Sprintf("%s %s", statusGlyph, node.Name)
	if node.ArgsPreview != "" {
		line += "  " + styleDim(node.ArgsPreview)
	}
	return line
}
