// Package tool defines the Tool abstraction and built-in tools used by the
// run engine. A Tool is anything the LLM can invoke via Anthropic native
// tool use — its Schema is sent to the model, and Run executes the actual
// side effect.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is implemented by anything the agent can call.
type Tool interface {
	// Name is the unique identifier sent to the LLM (e.g., "bash").
	Name() string
	// Description tells the LLM when to use this tool.
	Description() string
	// Schema is the JSON schema for the tool's arguments (Anthropic native tool use format).
	Schema() json.RawMessage
	// Run executes the tool with the LLM-provided arguments.
	Run(ctx context.Context, argsJSON json.RawMessage) (Result, error)
}

// Result is the outcome of a tool invocation, sent back to the LLM as a tool_result block.
type Result struct {
	Content string // text rendered into the next LLM turn
	IsError bool   // marks the result as an error (LLM may retry or change approach)
}

// CommandWrapper transforms a command + args into an isolated form
// (e.g., a bwrap-wrapped command). Implementations live in the runtime/
// module. A nil wrapper means execute the command as-is.
type CommandWrapper interface {
	Wrap(cmd string, args ...string) []string
}
