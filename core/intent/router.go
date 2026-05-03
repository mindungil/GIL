package intent

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// Verb is a canonical action name in the §2.6(b) router. The strings
// match the slash table (`/sessions`, `/spec`, …) so a single dispatch
// table in the cli REPL works for both InputSlash and verb-routed
// InputPrompt.
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

// ClassificationKind discriminates the three buckets §2.6(b) defines.
type ClassificationKind int

const (
	// KindForward means the input is conversational — forward it to the
	// interview / run service unchanged. This is the common case during
	// active sessions.
	KindForward ClassificationKind = iota

	// KindVerb means the input maps cleanly onto a known service call.
	// Verb + Args are populated; the caller dispatches.
	KindVerb

	// KindAmbiguous means the input could be a verb but a needed arg
	// is missing or the target is not unique. Clarification holds the
	// short follow-up question to show the user.
	KindAmbiguous
)

// Classification is what the Router returns for a single prompt.
type Classification struct {
	Kind          ClassificationKind
	Verb          Verb              // populated when Kind == KindVerb
	Args          map[string]string // verb-specific (e.g., {"target": "01KQEP…"} for switch)
	Rationale     string            // shown as a SystemNote — keep <60 chars
	Clarification string            // populated when Kind == KindAmbiguous
}

// SessionRef is the bare-bones session fact the router needs to resolve
// "switch to the dark-mode one"-style natural-language references.
type SessionRef struct {
	ID   string // ULID
	Slug string // GoalHint (may be empty)
}

// SessionContext is the runtime context the router classifies against.
// Built by the caller from chat-surface state — the router does not
// reach into client packages itself.
type SessionContext struct {
	// Phase is the active session phase (idle/interview/awaiting_confirm/run/done/stuck).
	// Empty when no session is active.
	Phase string

	// ActiveSessionID is the currently-selected session, or "" when
	// none.
	ActiveSessionID string

	// RecentSessions is the same top-N list shown in the pre-first-turn
	// disclosure block — used to resolve "switch to X" by slug.
	RecentSessions []SessionRef
}

// Router classifies prompts. V1 is deterministic and regex-based —
// fast, predictable, no network round-trip. The shape (Classify
// signature + Classification output) is forward-compatible with the
// LLM-based router described in `docs/plans/phase-26.6-intent-router.md`,
// so the upgrade is a swap-in without touching callers.
type Router struct{}

// NewRouter constructs the V1 deterministic router.
func NewRouter() *Router { return &Router{} }

// Classify maps prompt+ctx → Classification. The contract is documented
// at length in docs/plans/phase-26.6-intent-router.md §5.
func (r *Router) Classify(_ context.Context, prompt string, ctx SessionContext) Classification {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return Classification{Kind: KindForward}
	}

	lower := strings.ToLower(trimmed)

	// 1. Single-word verbs typed bare ("status", "spec", "diff", "merge", "run", "quit", "help").
	//    The user effectively typed the slash command without the slash.
	if v, ok := bareVerb(lower); ok {
		return verbClassification(v, nil, "you said "+string(v))
	}

	// 2. Phrase patterns per verb.
	for _, p := range verbPatterns {
		if !p.re.MatchString(lower) {
			continue
		}
		switch p.verb {
		case VerbSwitch:
			return resolveSwitch(trimmed, lower, ctx)
		default:
			return verbClassification(p.verb, nil, p.rationale)
		}
	}

	// 3. Nothing fired — forward.
	return Classification{Kind: KindForward}
}

func verbClassification(v Verb, args map[string]string, rationale string) Classification {
	return Classification{
		Kind:      KindVerb,
		Verb:      v,
		Args:      args,
		Rationale: rationale,
	}
}

// bareVerb maps a single-word lowercase prompt to a verb when it
// matches a known canonical name. "show", "list", etc. are handled by
// the phrase patterns below — bareVerb is just for the literal case
// where the user types the verb name itself.
func bareVerb(s string) (Verb, bool) {
	switch s {
	case "sessions", "list":
		return VerbSessions, true
	case "new":
		return VerbNew, true
	case "spec":
		return VerbSpec, true
	case "status":
		return VerbStatus, true
	case "diff":
		return VerbDiff, true
	case "merge", "apply":
		return VerbMerge, true
	case "run", "start":
		return VerbRun, true
	case "quit", "exit", "bye":
		return VerbQuit, true
	case "help":
		return VerbHelp, true
	}
	return "", false
}

type verbPattern struct {
	verb      Verb
	re        *regexp.Regexp
	rationale string
}

// Patterns are tested in order. More specific (multi-word) patterns
// come before shorter ones so "show me the diff" doesn't match the
// generic "show" prefix and route to sessions.
var verbPatterns = []verbPattern{
	{VerbDiff, regexp.MustCompile(`\b(show|see|view|preview|review)\b.*\b(diff|changes|edits|patch)\b`), "preview the run's diff"},
	{VerbDiff, regexp.MustCompile(`^(diff|changes|edits)$`), "diff"},
	{VerbMerge, regexp.MustCompile(`\b(apply|save|merge|commit|accept|approve)\b.*\b(diff|changes|edits|patch|it)\b`), "apply the diff"},
	{VerbMerge, regexp.MustCompile(`^(save it|apply it|merge it|commit it|approve it|accept it|merge|apply|approve|accept)$`), "apply the diff"},
	{VerbSpec, regexp.MustCompile(`\b(show|see|view|read)\b.*\b(spec|specification|frozen)\b`), "show the spec"},
	{VerbSpec, regexp.MustCompile(`\bwhat'?s?\s+the\s+spec\b`), "show the spec"},
	{VerbStatus, regexp.MustCompile(`\b(show|see)\b.*\b(status|progress|health)\b`), "show status"},
	{VerbStatus, regexp.MustCompile(`\b(what'?s?\s+(running|happening|the\s+status)|how'?s?\s+it\s+going|progress)\b`), "show status"},
	{VerbSessions, regexp.MustCompile(`\b(show|list|see|view)\b.*\b(sessions?|tasks?|missions?|jobs?|past\s+work)\b`), "list sessions"},
	{VerbSessions, regexp.MustCompile(`\b(what\s+do\s+i\s+have|what'?s?\s+going\s+on|history)\b`), "list sessions"},
	{VerbNew, regexp.MustCompile(`\b(start|create|begin|open)\b.*\b(new|fresh|another)\b.*\b(session|task|mission|job|one)\b`), "start a new session"},
	{VerbNew, regexp.MustCompile(`\b(new\s+(session|task|mission)|fresh\s+(session|task|mission)|start\s+over)\b`), "start a new session"},
	{VerbRun, regexp.MustCompile(`\b(start\s+the\s+run|begin\s+the\s+run|kick\s+off|run\s+it|let'?s\s+(go|run)|freeze\s+(it|and\s+run))\b`), "start the run"},
	{VerbSwitch, regexp.MustCompile(`\b(switch|resume|reopen|jump|pick\s+up)\s+(to|back\s+to|into|on)?\b`), "switch sessions"},
	{VerbSwitch, regexp.MustCompile(`\bcontinue\s+(the|that|with|on|where)\b`), "switch sessions"},
	{VerbSwitch, regexp.MustCompile(`\b(use|open|load)\b.*\b(session|task|mission|the\s+\w+\s+one)\b`), "switch sessions"},
	{VerbHelp, regexp.MustCompile(`\b(help|what\s+can\s+you\s+do|how\s+do\s+i)\b`), "help"},
	{VerbQuit, regexp.MustCompile(`\b(quit|exit|leave|bye|goodbye|see\s+ya)\b`), "exit"},
}

// resolveSwitch tries to extract the target session from the prompt.
// Strategies in order:
//  1. ULID-like token in the prompt → exact ID match.
//  2. Slug substring match against RecentSessions — unique hit returns
//     KindVerb with the matched ID; multiple hits return KindAmbiguous.
//  3. No identifiable target → KindAmbiguous asking which one.
func resolveSwitch(orig, lower string, ctx SessionContext) Classification {
	if id := extractSessionIDLoose(orig); id != "" {
		// Exact ID provided — trust it.
		return verbClassification(VerbSwitch, map[string]string{"target": id}, "switch by id")
	}

	// Slug match.
	type hit struct {
		id   string
		slug string
	}
	var hits []hit
	for _, s := range ctx.RecentSessions {
		if s.Slug == "" {
			continue
		}
		if substringMatch(lower, strings.ToLower(s.Slug)) {
			hits = append(hits, hit{s.ID, s.Slug})
		}
	}
	switch len(hits) {
	case 1:
		return verbClassification(VerbSwitch, map[string]string{"target": hits[0].id}, "switch to "+hits[0].slug)
	case 0:
		// No slug match — ambiguous.
		return Classification{
			Kind:          KindAmbiguous,
			Verb:          VerbSwitch,
			Clarification: "which session — name a slug or paste an id",
		}
	default:
		// Multiple hits — ask user to disambiguate.
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].slug < hits[j].slug })
		var names []string
		for _, h := range hits {
			names = append(names, `"`+h.slug+`"`)
		}
		return Classification{
			Kind:          KindAmbiguous,
			Verb:          VerbSwitch,
			Clarification: "which one — " + strings.Join(names, " or ") + "?",
		}
	}
}

// substringMatch returns true when any whitespace-delimited token of
// candidate appears as a whole word in haystack. Avoids "the" matching
// every prompt while still letting "dark mode" hit on "the dark one".
func substringMatch(haystack, candidate string) bool {
	for _, tok := range strings.Fields(candidate) {
		if len(tok) < 3 {
			continue // skip short connectors
		}
		if strings.Contains(haystack, tok) {
			return true
		}
	}
	return false
}

var ulidLooseRE = regexp.MustCompile(`(?i)\b[0-9A-HJKMNP-TV-Z]{6,26}\b`)

// extractSessionIDLoose returns the first ULID-shaped token in s.
// Crockford alphabet — no I, L, O, U.
func extractSessionIDLoose(s string) string {
	return ulidLooseRE.FindString(s)
}
