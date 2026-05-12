package app

import (
	"strings"
	"time"
)

// agent_tree.go — M6.1 (M6 design doc, docs/design/m6-tui-agent-visualization.md
// §4-§5). Pure data model: every chat-Prompt stream event feeds a turn
// + tool-call tree that the TUI can render as the center pane.
//
// M6.1 ships the model only; M6.2 wires it from chat_stream events;
// M6.3-M6.6 land the visual changes (render code lives in a separate
// file and replaces activity/memory).

// AgentTreeNodeStatus is the lifecycle of a single tool call.
type AgentTreeNodeStatus string

const (
	NodeRunning  AgentTreeNodeStatus = "running"
	NodeOK       AgentTreeNodeStatus = "ok"
	NodeFailed   AgentTreeNodeStatus = "failed"
)

// AgentTreeNode is one tool call within a turn. ID matches the
// tool_call's id on the wire; OnToolResult uses it to find the node
// to transition. ResultPreview is a truncated form of the result body
// for the footer/detail display — full bodies live in raw events.
type AgentTreeNode struct {
	ID            string
	Name          string
	ArgsPreview   string
	Status        AgentTreeNodeStatus
	StartedAt     time.Time
	Duration      time.Duration
	ResultPreview string
}

// AgentTurn groups all tool calls that fired during a single Prompt
// turn. Closed flips true on DonePart. Expanded is the user-controlled
// fold; closed turns default to collapsed so the active turn dominates
// the pane.
type AgentTurn struct {
	Number    int
	StartedAt time.Time
	Closed    bool
	Children  []*AgentTreeNode
	Expanded  bool
}

// AgentTree is the per-session tree the Model carries. We don't tag
// turns with session IDs at this layer — the TUI builds a tree per
// session and the chat model swaps trees on session change.
type AgentTree struct {
	Turns []*AgentTurn
}

// NewAgentTree returns an empty tree.
func NewAgentTree() *AgentTree { return &AgentTree{} }

// OnTurnStart opens a fresh turn root. Called when the user sends a
// new prompt OR when the first tool_call of a new turn arrives without
// an explicit start marker (graceful — Prompt's stream doesn't have a
// dedicated TurnStartPart).
func (t *AgentTree) OnTurnStart(at time.Time) *AgentTurn {
	if at.IsZero() {
		at = time.Now()
	}
	// Collapse the previous turn so the active turn dominates the view.
	if n := len(t.Turns); n > 0 {
		t.Turns[n-1].Expanded = false
	}
	turn := &AgentTurn{
		Number:    len(t.Turns) + 1,
		StartedAt: at,
		Expanded:  true,
	}
	t.Turns = append(t.Turns, turn)
	return turn
}

// OnToolCall appends a node to the active turn. If no turn is open
// (e.g. first tool call after a snapshot), a turn is auto-started.
func (t *AgentTree) OnToolCall(callID, name, inputJSON string) *AgentTreeNode {
	turn := t.activeTurnOrStart(time.Now())
	node := &AgentTreeNode{
		ID:          callID,
		Name:        name,
		ArgsPreview: previewArgs(inputJSON),
		Status:      NodeRunning,
		StartedAt:   time.Now(),
	}
	turn.Children = append(turn.Children, node)
	return node
}

// OnToolResult transitions the matching node. Returns the node (or
// nil when no match — e.g. result for a call from a previous daemon
// session that the TUI doesn't have a tree entry for).
func (t *AgentTree) OnToolResult(callID, content string, isError bool) *AgentTreeNode {
	node := t.findRunningByID(callID)
	if node == nil {
		return nil
	}
	node.Duration = time.Since(node.StartedAt)
	if isError {
		node.Status = NodeFailed
	} else {
		node.Status = NodeOK
	}
	node.ResultPreview = previewResult(content)
	return node
}

// OnTurnDone marks the active turn closed.
func (t *AgentTree) OnTurnDone() {
	if n := len(t.Turns); n > 0 {
		t.Turns[n-1].Closed = true
	}
}

// activeTurnOrStart returns the most recent open turn, opening one if
// the last turn is already closed or none exist.
func (t *AgentTree) activeTurnOrStart(at time.Time) *AgentTurn {
	if n := len(t.Turns); n > 0 && !t.Turns[n-1].Closed {
		return t.Turns[n-1]
	}
	return t.OnTurnStart(at)
}

// findRunningByID locates the matching tool call node, scanning the
// most recent turn first (the common case).
func (t *AgentTree) findRunningByID(callID string) *AgentTreeNode {
	for i := len(t.Turns) - 1; i >= 0; i-- {
		for _, n := range t.Turns[i].Children {
			if n.ID == callID {
				return n
			}
		}
	}
	return nil
}

// ToggleExpand flips Expanded on the turn at the given index. Out-of-
// range indexes are ignored.
func (t *AgentTree) ToggleExpand(turnIdx int) {
	if turnIdx < 0 || turnIdx >= len(t.Turns) {
		return
	}
	t.Turns[turnIdx].Expanded = !t.Turns[turnIdx].Expanded
}

// Reset clears the tree. Used on session change so the new session
// starts with an empty view.
func (t *AgentTree) Reset() { t.Turns = nil }

// previewArgs trims a tool input JSON to a single line capped for the
// node label. The full payload is kept in the raw event stream.
func previewArgs(inputJSON string) string {
	s := strings.TrimSpace(inputJSON)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// previewResult is the same single-line cap for tool results.
func previewResult(content string) string {
	s := strings.TrimSpace(content)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
