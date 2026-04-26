package exec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedutools/gil/core/tool"
)

// ExecTool is the agent-callable tool that runs a Recipe (declarative sequence
// of tool calls) and returns ONE summary string. Intermediate tool results
// stay out of the LLM's message history — that's the cache-savings point.
//
// To prevent infinite recursion, Tools should NOT include another ExecTool
// instance (name "exec"). ExecTool.Run defensively filters out any tool named
// "exec" from the inner tool list even if the caller forgets.
type ExecTool struct {
	Tools []tool.Tool                            // tools available inside the recipe
	Emit  func(typ string, data map[string]any) // optional event emitter
}

const execSchema = `{
  "type":"object",
  "properties":{
    "recipe":{
      "type":"object",
      "properties":{
        "steps":{"type":"array","items":{
          "type":"object",
          "properties":{
            "tool":{"type":"string"},
            "args":{"type":"object"}
          },
          "required":["tool","args"]
        }},
        "summary":{"type":"string","description":"summary template using {{step_N_output}}, {{step_N_status}}, {{step_N_err}} placeholders. Empty string → default 'Recipe complete' summary."}
      },
      "required":["steps"]
    }
  },
  "required":["recipe"]
}`

// ExecTool implements tool.Tool.
var _ tool.Tool = (*ExecTool)(nil)

func (e *ExecTool) Name() string { return "exec" }

func (e *ExecTool) Description() string {
	return "Run a sequence of other tool calls (Recipe) as ONE operation. Intermediate tool outputs stay HIDDEN from the LLM context — only the final templated summary returns. Use this when you need 3+ tool calls to accomplish one logical step (e.g., read 3 files + write 1 result). Reduces context burn dramatically. Cannot recurse (no nested exec). Available inner tools: same as the parent agent's, minus exec itself."
}

func (e *ExecTool) Schema() json.RawMessage { return json.RawMessage(execSchema) }

func (e *ExecTool) Run(ctx context.Context, argsJSON json.RawMessage) (tool.Result, error) {
	var args struct {
		Recipe Recipe `json:"recipe"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return tool.Result{}, fmt.Errorf("exec unmarshal: %w", err)
	}
	if len(args.Recipe.Steps) == 0 {
		return tool.Result{Content: "exec: recipe has no steps", IsError: true}, nil
	}

	// Filter out any ExecTool (name "exec") to prevent recursion.
	inner := make([]tool.Tool, 0, len(e.Tools))
	for _, t := range e.Tools {
		if t.Name() == "exec" {
			continue // defensive: skip self even if caller forgot
		}
		inner = append(inner, t)
	}

	runner := &Runner{
		Tools: inner,
		Emit:  e.Emit,
	}
	res, err := runner.Run(ctx, args.Recipe)
	if err != nil {
		return tool.Result{Content: "exec: " + err.Error(), IsError: true}, nil
	}

	// IsError if ANY step errored or timed out.
	var anyErr bool
	for _, s := range res.Steps {
		if s.Status != "ok" && s.Status != "skipped" {
			anyErr = true
			break
		}
	}
	return tool.Result{Content: res.Summary, IsError: anyErr}, nil
}
