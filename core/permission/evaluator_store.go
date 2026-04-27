package permission

import "sync"

// EvaluatorWithStore wraps the spec-derived Evaluator with two extra
// layers of permission state: a per-session in-memory list (decisions
// the user made via "Allow for this session" / "Deny for this session"
// in the TUI prompt) and an on-disk PersistentStore (the
// always-allow / always-deny rules learned across runs).
//
// Priority, highest to lowest:
//
//  1. Persistent always_deny (project-keyed) — deny wins over allow at
//     the same tier, defence-in-depth: an explicit "always deny" must
//     never be overridden by an allow rule the user later forgot about.
//  2. Persistent always_allow (project-keyed)
//  3. In-memory session deny
//  4. In-memory session allow
//  5. Spec-derived rules (the wrapped Evaluator) — autonomy gates
//     destructive bash patterns etc.
//  6. Default DecisionAsk if nothing matched.
//
// Keying both the persistent store and the evaluator by the absolute
// project path is what prevents cross-project leakage: a `rm -rf ./out`
// approved in repo A does not auto-allow in repo B even though the
// command shape is identical.
//
// EvaluatorWithStore is safe for concurrent reads. Mutations to
// SessionAllow/SessionDeny must go through AppendSession (which holds
// the mutex). Direct field access is permitted at construction time
// only.
type EvaluatorWithStore struct {
	// Inner is the spec-derived evaluator (FromAutonomy result). May be
	// nil — when nil, Evaluate falls through to default Ask after
	// consulting the persistent and session lists. nil-Inner is the
	// equivalent of "FULL autonomy with no spec rules" plus whatever
	// the user has persisted.
	Inner *Evaluator

	// Store is the on-disk allow/deny store. Nil disables the
	// persistent layer entirely (used by tests that don't want to
	// touch disk).
	Store *PersistentStore

	// ProjectPath is the absolute workspace path used as the lookup key
	// in Store. Empty = persistent layer is bypassed even when Store is
	// non-nil (defensive: an empty key would silently match the wrong
	// project if the store happened to have an entry for "").
	ProjectPath string

	mu sync.Mutex
	// SessionAllow / SessionDeny are wildcard patterns added by the TUI
	// when the user picks "Allow for session" / "Deny for session". They
	// match against the Key (the tool-specific detail string — e.g., a
	// bash command). The Tool is matched against any tool name; session
	// scope is intentionally tool-agnostic because the user's mental
	// model is "this command", not "this command on this tool".
	SessionAllow []string
	SessionDeny  []string
}

// AppendSession adds a pattern to the session-scoped allow or deny
// list. `list` must be "allow" or "deny" (matching the user's choice in
// the TUI). Duplicates are silently ignored.
func (e *EvaluatorWithStore) AppendSession(list, pattern string) {
	if pattern == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	target := &e.SessionAllow
	if list == "deny" {
		target = &e.SessionDeny
	}
	for _, p := range *target {
		if p == pattern {
			return
		}
	}
	*target = append(*target, pattern)
}

// Evaluate consults the persistent store, the session-scoped lists, and
// then the wrapped spec evaluator in priority order. The first matching
// layer wins; within a layer, deny beats allow.
//
// Errors from PersistentStore.Load are silently ignored — if the on-disk
// file is corrupt the user will hit it via `gil permissions list` or the
// next Append; the evaluator's job is to return a Decision, not surface
// I/O. The fallback (skip the persistent layer when load fails) is the
// safe choice: the rest of the layers still provide gating.
func (e *EvaluatorWithStore) Evaluate(toolName, key string) Decision {
	// 1+2. Persistent layer (project-keyed).
	if e.Store != nil && e.ProjectPath != "" {
		if rules, _ := e.Store.Load(e.ProjectPath); rules != nil {
			for _, p := range rules.AlwaysDeny {
				if MatchWildcard(key, p) {
					return DecisionDeny
				}
			}
			for _, p := range rules.AlwaysAllow {
				if MatchWildcard(key, p) {
					return DecisionAllow
				}
			}
		}
	}

	// 3+4. Session-scoped layer.
	e.mu.Lock()
	for _, p := range e.SessionDeny {
		if MatchWildcard(key, p) {
			e.mu.Unlock()
			return DecisionDeny
		}
	}
	for _, p := range e.SessionAllow {
		if MatchWildcard(key, p) {
			e.mu.Unlock()
			return DecisionAllow
		}
	}
	e.mu.Unlock()

	// 5. Spec-derived (existing) evaluator. Falls through to Ask when
	// no rule matches, which is the correct default for the
	// permission-enforcing autonomy levels.
	if e.Inner != nil {
		return e.Inner.Evaluate(toolName, key)
	}
	// 6. No inner evaluator → behave like an empty Evaluator.
	return DecisionAsk
}
