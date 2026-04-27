package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SubagentRunner is the read-only contract the Subagent tool needs from
// the parent AgentLoop. core/runner.AgentLoop satisfies it natively via
// RunSubagentWithConfig — the interface lives here so this package doesn't
// import core/runner (which would cycle: runner → tool → runner).
type SubagentRunner interface {
	RunSubagentWithConfig(ctx context.Context, cfg SubagentRunConfig) (SubagentRunResult, error)
}

// SubagentRunConfig mirrors core/runner.SubagentConfig field-for-field.
// We re-declare it here (rather than import the runner type) for the
// same anti-cycle reason — the runner adapts between its own type and
// this one in a tiny shim where the tool is constructed.
type SubagentRunConfig struct {
	Goal          string
	AllowedTools  []string
	MaxIterations int
	MaxTokens     int64
	Model         string
}

// SubagentRunResult is the parent-visible outcome.
type SubagentRunResult struct {
	Summary      string
	Status       string
	Iterations   int
	InputTokens  int64
	OutputTokens int64
	Tokens       int64
}

// Subagent is the agent-callable read-only research subloop. The agent
// hands it a single-sentence goal; we spawn a tightly-budgeted sub-loop
// using a restricted tool set (default: read_file, repomap, memory_load,
// web_fetch, lsp) and return the sub-loop's last assistant text.
//
// Lifted patterns:
//   - OpenHands "delegation": parent gives subagent a goal + budget, gets
//     back a result. We run in-process (no new session) which matches
//     gil's existing stuck-recovery SubagentBranch pattern.
//   - Cline use_subagents: scoped read-only research subloop spawned by
//     the agent itself. We diverge by enforcing the read-only default in
//     the runner rather than the prompt.
//
// Constraints:
//   - Default tool set is conservative (read-only). The optional tools[]
//     argument is an escape hatch but the parent's permission gate still
//     evaluates the subagent call itself — at PLAN_ONLY / ASK_PER_ACTION
//     dials the call surfaces an Ask before any sub-loop spawns.
//   - Hard ceilings on iterations (20) and tokens (handled in runner).
//   - Result truncated to ~2 KB so a verbose sub-loop can't crowd the
//     parent's tool_result block.
type Subagent struct {
	Runner SubagentRunner
}

const subagentSchema = `{
  "type":"object",
  "properties":{
    "goal":{
      "type":"string",
      "description":"Single-sentence goal for the subagent. Be specific."
    },
    "max_iterations":{
      "type":"integer",
      "description":"Hard cap (default 8, max 20) — don't waste tokens on long sub-runs"
    },
    "tools":{
      "type":"array",
      "description":"Optional override of allowed tools (default: read-only set: read_file, repomap, memory_load, web_fetch, lsp)",
      "items":{"type":"string"}
    }
  },
  "required":["goal"]
}`

func (s *Subagent) Name() string { return "subagent" }

func (s *Subagent) Description() string {
	return "Spawn a focused, time-boxed subagent with read-only tools (read_file, repomap, web_fetch, lsp) to investigate a question or scout an unfamiliar area. Returns a 1-3 paragraph finding. Use when you need exploratory research without polluting your own context. Subagent CANNOT modify files or run shell commands."
}

func (s *Subagent) Schema() json.RawMessage { return json.RawMessage(subagentSchema) }

type subagentArgs struct {
	Goal          string   `json:"goal"`
	MaxIterations int      `json:"max_iterations"`
	Tools         []string `json:"tools"`
}

const subagentResultMaxBytes = 2048

// Run dispatches the sub-loop via the wired SubagentRunner. It returns a
// non-error tool.Result even when the underlying sub-loop errored — the
// agent should see the failure in the tool_result content (IsError=true)
// and decide what to do next, rather than the runner aborting the whole
// session on a sub-call hiccup.
func (s *Subagent) Run(ctx context.Context, argsJSON json.RawMessage) (Result, error) {
	if s.Runner == nil {
		return Result{Content: "subagent: not configured (no runner wired)", IsError: true}, nil
	}
	var a subagentArgs
	if err := json.Unmarshal(argsJSON, &a); err != nil {
		return Result{}, fmt.Errorf("subagent unmarshal: %w", err)
	}
	if strings.TrimSpace(a.Goal) == "" {
		return Result{Content: "subagent: goal is required", IsError: true}, nil
	}

	res, err := s.Runner.RunSubagentWithConfig(ctx, SubagentRunConfig{
		Goal:          a.Goal,
		AllowedTools:  a.Tools, // empty → runner falls back to read-only default set
		MaxIterations: a.MaxIterations,
	})
	if err != nil {
		// Surface the error AND any partial summary the sub-loop produced
		// so the agent can see what (if anything) was learned before
		// failure.
		var partial string
		if res.Summary != "" {
			partial = "\n\nPartial finding before error:\n" + truncateForSubagent(res.Summary)
		}
		return Result{
			Content: "subagent error: " + err.Error() + partial,
			IsError: true,
		}, nil
	}

	summary := strings.TrimSpace(res.Summary)
	if summary == "" {
		// Status=="max_iterations" with no FinalText is a real outcome
		// (the sub-loop ran out of iters mid-tool-loop). Tell the agent
		// what happened so it can decide whether to retry with a
		// different goal / more iters.
		msg := "subagent returned no finding"
		if res.Status != "" {
			msg = fmt.Sprintf("subagent returned no finding (status=%s, iterations=%d, tokens=%d)",
				res.Status, res.Iterations, res.Tokens)
		}
		return Result{Content: msg, IsError: true}, nil
	}
	body := truncateForSubagent(summary)
	header := fmt.Sprintf("Subagent finding (status=%s, iterations=%d, tokens=%d):\n",
		res.Status, res.Iterations, res.Tokens)
	return Result{Content: header + body}, nil
}

func truncateForSubagent(s string) string {
	if len(s) <= subagentResultMaxBytes {
		return s
	}
	return s[:subagentResultMaxBytes] + "\n\n... (truncated; sub-loop output exceeded 2 KB)"
}

// Compile-time guarantee that Subagent satisfies the Tool interface even
// when no concrete runner is wired (constructor / wiring tests).
var _ Tool = (*Subagent)(nil)

// ErrSubagentNotConfigured is returned via a tool.Result when the tool
// runs without a Runner. Callers (mostly tests) can match on it to assert
// wiring rather than scrape the message.
var ErrSubagentNotConfigured = errors.New("subagent: not configured")
