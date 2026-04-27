package permission

// Decision is the outcome of evaluating a tool call against a Ruleset.
type Decision int

const (
	DecisionAsk   Decision = iota // default when no rule matches
	DecisionAllow
	DecisionDeny
)

func (d Decision) String() string {
	switch d {
	case DecisionAsk:
		return "ask"
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	}
	return "unknown"
}

// Rule is one entry in a permission ruleset.
//
// Tool is a wildcard pattern matched against the tool name (e.g., "bash",
// "write_*"). Key is a wildcard pattern matched against a tool-specific
// detail string (e.g., for bash: the command; for write_file: the path).
// Action is what to do when both patterns match.
type Rule struct {
	Tool   string
	Key    string
	Action Decision
}

// Decider is the abstraction the runner uses to gate tool calls. Both
// Evaluator (spec-derived only) and EvaluatorWithStore (spec + on-disk
// + session-scoped) implement it, so the runner can stay ignorant of
// the persistence layer entirely. Callers must accept that any
// implementation may consult disk, locks, or network — Evaluate should
// still return promptly (sub-millisecond for the in-memory cases).
type Decider interface {
	Evaluate(toolName, key string) Decision
}

// Evaluator decides allow/ask/deny per (tool, key) pair. Last-matching rule
// wins (OpenCode pattern: rules.findLast).
//
// Lifted from opencode/packages/opencode/src/permission/evaluate.ts.
type Evaluator struct {
	Rules []Rule
}

// Evaluate returns the action of the LAST matching rule. When no rule
// matches, returns DecisionAsk.
func (e *Evaluator) Evaluate(toolName, key string) Decision {
	for i := len(e.Rules) - 1; i >= 0; i-- {
		r := e.Rules[i]
		if MatchWildcard(toolName, r.Tool) && MatchWildcard(key, r.Key) {
			return r.Action
		}
	}
	return DecisionAsk
}
