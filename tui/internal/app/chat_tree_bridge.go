package app

import (
	"encoding/json"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// chat_tree_bridge.go — M6 Option A glue. The daemon's per-session
// event stream (RunService.Tail) carries both run-mode tool_call /
// tool_result events AND chat-mode ones (since SessionService.Prompt
// bridges through ensureSessionStream). giltui's Model passes every
// event through applyEventToChatTree so the chat agent's activity is
// reflected in the same AgentTree shape the chat surface uses.
//
// Provider boundary signals (iteration_start, provider_request) start
// a fresh turn root so each LLM call has its own scope; tool_call
// appends to the active turn; tool_result transitions the matching
// node. Unrecognised event types are no-ops so the bridge stays
// forward-compatible with future event kinds.

// applyEventToChatTree updates tree in-place based on one event from
// the per-session stream. The function is pure with respect to
// everything outside the tree (no I/O, no logging) so unit tests can
// drive it with synthetic events.
func applyEventToChatTree(tree *AgentTree, ev *gilv1.Event) {
	if tree == nil || ev == nil {
		return
	}
	switch ev.GetType() {
	case "iteration_start", "provider_request":
		// Each provider round-trip opens a fresh root so a multi-turn
		// agent loop produces visible nesting in the rendered tree.
		// Only open one if there's no currently-active turn.
		if n := len(tree.Turns); n == 0 || tree.Turns[n-1].Closed {
			tree.OnTurnStart(ev.GetTimestamp().AsTime())
		}
	case "tool_call":
		var payload struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input string `json:"input"`
		}
		if err := json.Unmarshal(ev.GetDataJson(), &payload); err == nil {
			tree.OnToolCall(payload.ID, payload.Name, payload.Input)
		}
	case "tool_result":
		var payload struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
		}
		if err := json.Unmarshal(ev.GetDataJson(), &payload); err == nil {
			tree.OnToolResult(payload.ID, payload.Content, payload.IsError)
		}
	case "run.done", "prompt_done", "iteration_end":
		tree.OnTurnDone()
	}
}
