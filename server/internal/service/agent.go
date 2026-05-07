package service

import (
	"fmt"
	"strings"
)

// agent.go defines the Agent abstraction (M4 of the chat-architecture
// migration — docs/design/chat-architecture.md). Each Agent bundles a
// system prompt + a tool subset (whitelist of names from the registry
// in agent_tools.go). PromptRequest.agent picks one; empty falls
// through to "default".
//
// V1 ships built-in agents only. ~/.config/gil/agents/<name>.toml
// loading is intentionally deferred: it adds config-surface complexity
// without a clear use-case yet, and we can add it once a real user
// asks for it.

// Agent is a single agent profile. tools is a whitelist of tool names
// from the chatToolRegistry; an empty list means "all tools available
// to the registry". The whitelist is enforced in
// buildChatToolRegistryFor — the LLM only sees tools the agent is
// allowed to call.
type Agent struct {
	Name         string
	Description  string
	SystemPrompt string // template string; %s is filled with provider, model, sessionID
	Tools        []string
}

// builtinAgents is the daemon's fixed registry of agent profiles. Keys
// are case-insensitive; lookup() lower-cases the input.
var builtinAgents = map[string]*Agent{
	"default": {
		Name:         "default",
		Description:  "General-purpose autonomous coding agent. Read, edit, run, verify.",
		SystemPrompt: defaultChatSystemPrompt,
		// Empty Tools = full registry.
	},
	"explore": {
		Name:         "explore",
		Description:  "Read-only investigator. Use for codebase questions where edits are not yet wanted.",
		SystemPrompt: exploreChatSystemPrompt,
		Tools:        []string{"read_file", "grep", "glob", "show_diff", "show_spec", "show_status", "list_sessions"},
	},
	"plan": {
		Name:         "plan",
		Description:  "Planner. Investigates and produces an actionable plan without modifying the workspace.",
		SystemPrompt: planChatSystemPrompt,
		Tools:        []string{"read_file", "grep", "glob", "todowrite", "show_diff", "show_spec", "show_status", "list_sessions"},
	},
}

// resolveAgent returns the agent profile for the given name. Empty
// string maps to "default". Unknown names are an InvalidArgument
// surface so the user gets a clear error rather than a silent
// fallback to default. Lookup is case-insensitive.
func resolveAgent(name string) (*Agent, error) {
	if name == "" {
		return builtinAgents["default"], nil
	}
	a, ok := builtinAgents[strings.ToLower(name)]
	if !ok {
		known := make([]string, 0, len(builtinAgents))
		for k := range builtinAgents {
			known = append(known, k)
		}
		return nil, fmt.Errorf("unknown agent %q (available: %s)", name, strings.Join(known, ", "))
	}
	return a, nil
}

// exploreChatSystemPrompt is the read-only investigator. The agent
// has read_file/grep/glob but cannot write/edit/run; the system
// prompt nudges it to deliver findings, not actions.
const exploreChatSystemPrompt = `You are gil's explore agent — a read-only codebase investigator.

The user types in natural language. Use read_file, grep, and glob to
answer questions about the codebase. Do NOT propose or perform edits.
Do NOT run shell commands. Your job is to investigate and report.

When you've gathered enough evidence, summarise: where the relevant
code lives (file:line), what it does, and any caveats you noticed.

If the user asks for an edit, point out that you're the explore agent
and they should switch to the default agent (or ask the harness to
escalate) for write actions.

System context: provider=%s model=%s session=%s
`

// planChatSystemPrompt produces an actionable plan without making
// edits. todowrite is included so the plan can be persisted as a
// session todo list.
const planChatSystemPrompt = `You are gil's plan agent — an investigator that produces concrete plans, not edits.

The user types in natural language. Investigate with read_file/grep/
glob until you understand the relevant code. Then write a plan as a
todo list (todowrite tool) and present it back to the user with:
- numbered steps
- which files each step touches
- a short verification step at the end

Do NOT make edits. Do NOT run commands. Your output is a plan the
user (or another agent) will execute.

If the task is small enough that a plan would be over-engineering,
say so plainly and suggest the user run it through the default agent.

System context: provider=%s model=%s session=%s
`
