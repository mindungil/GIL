// Package intent classifies chat prompts. Per design.md §2.6 the chat
// surface is a single natural-language surface; the slash table is an
// escape hatch only. The router's job is therefore minimal:
//
//   - Slash-prefixed input is dispatched directly by the caller (the
//     router never sees it).
//   - Everything else is forwarded to the daemon. The harness + LLM
//     decide what the input means — greeting, meta-question, task
//     description, verb invocation phrased naturally — not a regex
//     table on the client.
//
// Earlier versions of this router carried client-side verb-pattern
// regex, a "too-vague" length floor, an interrogative regex, and a
// non-Latin script branch. Those all violated two project principles
// the user has stated explicitly:
//
//   - "자연어 단일 surface, 내부 에이전트 라우팅" — every user-facing
//     decision is natural language; slashes are escape hatches; routing
//     happens INSIDE the agent loop, not on the client.
//   - "에이전트 결정 vs 시스템 안전망" — the system holds schemas,
//     limits, objective termination, and persistence; everything else
//     (including "is this a verb invocation?", "is this too vague?",
//     "should we forward?") is the agent's call.
//
// So the router is now a stub. The Verb constants and SessionRef /
// SessionContext types stay so the slash-dispatch tables in cli and
// tui keep working without renaming. The Classify function always
// returns KindForward; verb detection moved to the daemon-side LLM
// loop.
package intent

import (
	"context"
	"strings"
)

// Verb is a canonical action name used by the slash-dispatch tables in
// cli/internal/chat/repl and tui/internal/app. Kept on this package so
// both surfaces share one enum even though the router itself no longer
// emits Verb classifications.
type Verb string

const (
	VerbSessions Verb = "sessions"
	VerbSwitch   Verb = "switch"
	VerbNew      Verb = "new"
	VerbSpec     Verb = "spec"
	VerbStatus   Verb = "status"
	VerbDiff     Verb = "diff"
	VerbMerge    Verb = "merge"
	VerbRun      Verb = "run"
	VerbQuit     Verb = "quit"
	VerbHelp     Verb = "help"
)

// ClassificationKind discriminates the buckets §2.6(b) defines. Only
// KindForward is returned today; the other constants stay on the
// public API for forward compatibility (a future LLM-side router
// could surface ambiguity via a server event, at which point the
// caller already knows how to render KindAmbiguous).
type ClassificationKind int

const (
	KindForward ClassificationKind = iota
	KindVerb
	KindAmbiguous
	KindTooVague
)

// Classification is what the Router returns for a single prompt.
type Classification struct {
	Kind          ClassificationKind
	Verb          Verb
	Args          map[string]string
	Rationale     string
	Clarification string
}

// SessionRef is the bare-bones session fact callers used to pass when
// the router resolved natural-language references. Kept so callers
// don't need an immediate refactor; unused by the current Classify.
type SessionRef struct {
	ID   string
	Slug string
}

// SessionContext is the runtime context callers used to pass. Same
// rationale — kept for API stability.
type SessionContext struct {
	Phase           string
	ActiveSessionID string
	RecentSessions  []SessionRef
}

// Router is now a stub. It exists so callers can stay shaped the way
// they were while the LLM-side router (planned in
// docs/plans/phase-26.6-intent-router.md) takes over the
// natural-language → verb resolution previously done here.
type Router struct{}

// NewRouter constructs the (stub) router.
func NewRouter() *Router { return &Router{} }

// Classify always returns KindForward for non-empty prompts. The
// daemon's interview / run loop is the single decision point for
// what the input means. Empty inputs also forward; the caller drops
// them at the textinput layer.
func (r *Router) Classify(_ context.Context, prompt string, _ SessionContext) Classification {
	_ = strings.TrimSpace(prompt) // intentional no-op — kept for symmetry with the previous signature shape
	return Classification{Kind: KindForward}
}
