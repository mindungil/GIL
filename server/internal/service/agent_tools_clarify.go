package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mindungil/gil/core/provider"
)

// agent_tools_clarify.go — P63 request_user_input tool.
//
// The chat agent's only natural "this turn is over" signal was
// "emit no tool calls" (= StopReason="end_turn"). With P63's lifted
// maxAgentTurns=30 the agent has room to keep iterating, but it also
// needs an EXPLICIT way to say "I need the user to answer something
// before I can continue" — without that, the only options are
// (a) keep guessing until budget exhausted, or (b) end_turn and lose
// the in-flight state.
//
// request_user_input is that explicit pause. The agent calls it with
// a question; the tool returns immediately with a synthetic answer
// "[user input pending — end this turn with no further tool calls]"
// and the agent's next response is expected to end the turn (no tools)
// so the user can answer in their next prompt.
//
// Note: this tool does NOT block waiting for a real user reply (that
// would require multi-turn within a Prompt RPC, which the current
// architecture doesn't support without major plumbing). It just lets
// the agent SIGNAL that it wants input — the chat REPL then renders
// the question and yields the prompt back to the user.

type toolRequestUserInput struct{}

func (t *toolRequestUserInput) name() string { return "request_user_input" }

func (t *toolRequestUserInput) description() string {
	return "Pause the autonomous loop and ask the user a focused question. " +
		"Use when the task is ambiguous and you cannot make reasonable " +
		"defaults (e.g. multiple acceptable interpretations, a destructive " +
		"action that needs explicit confirmation, or a missing piece of " +
		"information that no amount of reading the codebase can supply). " +
		"After calling this tool, END YOUR TURN — emit no further tool " +
		"calls. The user will see your question and answer in their next " +
		"prompt. Do NOT use this for things you can figure out by reading " +
		"the code or trying — only for genuine user-only knowledge."
}

func (t *toolRequestUserInput) schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {
				"type": "string",
				"description": "Focused question (≤300 chars). Be specific about what you need to know."
			}
		},
		"required": ["question"]
	}`)
}

func (t *toolRequestUserInput) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "request_user_input: invalid args: " + err.Error(), IsError: true}, nil
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return provider.ToolResult{Content: "request_user_input: question cannot be empty", IsError: true}, nil
	}
	if len(args.Question) > 300 {
		return provider.ToolResult{Content: "request_user_input: question too long (max 300 chars). Focus it.", IsError: true}, nil
	}
	// The tool result tells the AGENT what to do next: end the turn.
	// The HUMAN sees the question text via the chat REPL's normal
	// tool_call rendering (⚒ request_user_input  {"question": "..."}).
	return provider.ToolResult{
		Content: "[question delivered to user — end this turn with no further tool calls; user will answer in their next prompt]",
	}, nil
}
