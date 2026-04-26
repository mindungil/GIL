// Package exec implements multi-step tool compression: a sequence of tool
// calls is run as a single agent-visible operation; intermediate results
// stay out of the LLM's message history, only a final summary is returned.
//
// Adapted from Hermes's code_execution_tool.py pattern (script in subprocess
// → only stdout returned). gil's version uses a typed JSON Recipe instead
// of Python code so the agent can emit it directly with no eval/RCE risk.
package exec

import "encoding/json"

// Resource limits, lifted from Hermes (DEFAULT_TIMEOUT, DEFAULT_MAX_TOOL_CALLS,
// MAX_STDOUT_BYTES).
const (
	DefaultStepTimeoutSec = 300
	DefaultMaxSteps       = 50
	DefaultMaxOutputBytes = 50_000
)

// Recipe is a declarative sequence of tool calls plus a summary template.
type Recipe struct {
	Steps   []RecipeStep `json:"steps"`
	Summary string       `json:"summary"` // template; uses {{step_N_output}} and {{step_N_status}}
}

// RecipeStep is a single tool call within a Recipe.
type RecipeStep struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// StepResult is the per-step record after Recipe execution.
type StepResult struct {
	Step   int    // 1-indexed
	Tool   string
	Output string // truncated to MaxOutputBytes
	Status string // "ok" | "error" | "skipped" | "timeout"
	ErrMsg string // populated when Status != "ok"
}

// Result is the Runner output.
type Result struct {
	Steps   []StepResult
	Summary string // template-substituted summary
}

// Emitter is the optional event sink. The Runner emits an exec_step event
// per step so observers (e.g., RunService event stream) see what happened
// even though the LLM doesn't.
type Emitter func(eventType string, data map[string]any)
