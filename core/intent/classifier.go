// Package intent classifies a user's natural-language chat input into one of
// the high-level "what does the user want to do?" buckets that drive the
// unified `gil` chat surface (Phase 24).
//
// The classifier has two layers:
//
//  1. A regex/heuristic fast-path that catches the obvious wordings without
//     burning a provider call. Empty input, "status", "continue", "help" —
//     all trivial enough that running them through an LLM would be wasteful.
//  2. An LLM fallback that handles ambiguous phrasings ("can you pick up
//     yesterday's task?", "I want to play with my Go project at ~/foo").
//     The LLM is asked for a strict JSON object so we can parse it
//     deterministically; on parse failure we fall back to NEW_TASK with a
//     low confidence score.
//
// The two layers are arranged so the chat REPL can call Classify with a nil
// provider and still get a useful answer: the regex layer is sufficient for
// the conversation entry-point we ship in Phase 24. The LLM layer matters
// when users phrase things creatively — but it must never be load-bearing
// (a user without configured credentials should still be able to drop into
// chat).
package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mindungil/gil/core/provider"
)

// Kind is the high-level decision the chat REPL routes on. The strings are
// intentionally upper-case so an LLM that echoes them back matches our
// constants without a case-fold.
type Kind string

const (
	// KindNewTask means the user described work to do — start an interview
	// for a fresh session. The classifier extracts the goal one-liner and
	// (if mentioned) a workspace path so the chat REPL can pre-fill the
	// spec slots before the first interview question fires.
	KindNewTask Kind = "NEW_TASK"

	// KindResume means the user wants to continue an existing session.
	// SessionID is best-effort — when set it gives the REPL a fast path
	// to GetSession; when empty the REPL falls back to a fuzzy picker.
	KindResume Kind = "RESUME"

	// KindStatus means the user just wants to see what's running. The REPL
	// hands off to the existing summary renderer.
	KindStatus Kind = "STATUS"

	// KindHelp means the user asked what gil can do. The REPL prints a
	// short capability primer (no need to run the full --help banner —
	// keep the chat surface conversational).
	KindHelp Kind = "HELP"

	// KindExplain means the user is asking a meta question about gil
	// itself ("how does this work?", "what's an interview?"). The REPL
	// can answer narratively instead of dispatching to a subcommand.
	KindExplain Kind = "EXPLAIN"

	// KindUnknown is used when neither the regex nor the LLM produces a
	// confident classification. The REPL falls through to NEW_TASK with
	// a confirmation prompt.
	KindUnknown Kind = "UNKNOWN"
)

// Intent is the structured result of classifying a user message.
//
// Confidence is on [0, 1]. The regex fast-path emits 0.95 for clean matches
// (emptiness, exact "status" keyword, etc.) and 0.6 for soft matches
// ("yesterday" without an explicit "continue"). The LLM layer reports
// whatever the model said, clamped to the unit interval; we never trust a
// model that returns >1 or <0.
//
// GoalText / Workspace / SessionID are only populated for the kinds where
// they make sense:
//   - NEW_TASK: GoalText is the trimmed user message; Workspace is filled
//     when the user mentioned a path-like substring.
//   - RESUME:   SessionID is filled when the user's message contained
//     something that looks like a ULID prefix (6+ alphanumeric chars).
//   - others:   all three are zero.
type Intent struct {
	Kind       Kind
	Confidence float64
	GoalText   string
	Workspace  string
	SessionID  string
}

// regex catalogue. Pre-compiled at package load so Classify is allocation-
// light on the hot path. The patterns are deliberately permissive — false
// positives are cheaper than false negatives because the chat REPL can ask
// "is that right?" before doing anything destructive.
var (
	statusRE  = regexp.MustCompile(`(?i)^(show|list|status|what['']?s\s+running|whats\s+running|sessions)\b`)
	resumeRE  = regexp.MustCompile(`(?i)^(continue|resume|pick\s+up|keep\s+going|finish)\b`)
	helpRE    = regexp.MustCompile(`(?i)^(help|what\s+can\s+you\s+do|how\s+do\s+i|commands)\b`)
	explainRE = regexp.MustCompile(`(?i)^(what\s+is|what['']?s|explain|how\s+does)\b.*\b(gil|interview|session|spec)\b`)

	// pathRE finds the first token that looks like a filesystem path. We
	// accept ~/..., absolute paths, and ./relative — any standalone token
	// containing a slash that the user might mean as a workspace hint.
	pathRE = regexp.MustCompile(`(?:^|\s)(~\/[\S]+|\/[\S]+|\.\/[\S]+)`)

	// idRE finds a ULID-like prefix (Crockford alphabet, 6+ chars) in the
	// message. Used to seed Intent.SessionID for RESUME.
	idRE = regexp.MustCompile(`\b([0-9A-HJ-NP-TV-Za-hj-np-tv-z]{6,26})\b`)
)

// classifierSystemPrompt is the LLM contract. Kept short so the round-trip
// stays under ~500 tokens on the smallest models (haiku, qwen-7b, etc.).
// The schema is enforced with strict JSON; anything else gets parsed into
// KindUnknown by the caller.
const classifierSystemPrompt = `You classify a user's first chat message to a CLI coding agent named "gil".
Output STRICT JSON only — no prose, no markdown fences. Schema:
{"kind":"NEW_TASK|RESUME|STATUS|HELP|EXPLAIN|UNKNOWN","confidence":0.0-1.0,"goal":"string","workspace":"string","session_id":"string"}

Rules:
- NEW_TASK when the user describes work to do (add a feature, fix a bug, refactor, write code).
  goal = a short summary of what they want; workspace = path if mentioned, else "".
- RESUME when the user wants to continue prior work ("continue yesterday", "pick up the OAuth task").
  session_id = any ULID-looking token from the message, else "".
- STATUS when they ask what's running / list sessions / show progress.
- HELP when they ask "what can you do" / "how do I use this".
- EXPLAIN when they ask a meta question about gil itself.
- UNKNOWN if it's truly ambiguous.
Never include trailing commas. All fields required, use empty string when N/A.`

// Classify returns the Intent for userMsg. When prov is nil, only the regex
// layer runs; otherwise an unmatched message is sent to the LLM.
//
// hasSessions is the count-aware short-circuit: when the user has 0
// sessions, RESUME / STATUS make no sense — we re-route to NEW_TASK so the
// chat REPL doesn't dead-end on a fuzzy picker with no candidates.
//
// model is provider-specific (haiku for anthropic, qwen-7b for vllm, etc.).
// The caller picks; this package does not embed credstore knowledge.
func Classify(ctx context.Context, prov provider.Provider, model, userMsg string, hasSessions bool) (Intent, error) {
	trimmed := strings.TrimSpace(userMsg)

	// Empty message → ambiguous. The chat REPL prompts again rather than
	// guessing. We return UNKNOWN with high confidence to make the
	// "what do you want to do?" branch obvious.
	if trimmed == "" {
		return Intent{Kind: KindUnknown, Confidence: 1.0}, nil
	}

	// Fast-path heuristics. These run before any LLM call so the common
	// shapes ("status", "continue") cost zero tokens.
	if it, ok := classifyByRegex(trimmed); ok {
		// Demote STATUS/RESUME when the user has no sessions — those
		// kinds dead-end on an empty list. NEW_TASK is the safer
		// fallback because the user's words still describe their
		// intent ("show me what's running" with 0 sessions reasonably
		// means "I haven't started anything; let's start").
		if !hasSessions && (it.Kind == KindStatus || it.Kind == KindResume) {
			return Intent{Kind: KindNewTask, Confidence: 0.5, GoalText: trimmed}, nil
		}
		return it, nil
	}

	// LLM fallback. Only when a provider was supplied — most tests pass
	// nil and rely on the regex layer alone.
	if prov != nil {
		if it, err := classifyByLLM(ctx, prov, model, trimmed); err == nil {
			if !hasSessions && (it.Kind == KindStatus || it.Kind == KindResume) {
				return Intent{Kind: KindNewTask, Confidence: 0.5, GoalText: trimmed}, nil
			}
			return it, nil
		}
		// Swallow LLM errors: the chat REPL must keep working without
		// network access. The fallthrough below treats it as NEW_TASK.
	}

	// Default: treat as NEW_TASK with low confidence. The chat REPL
	// confirms before committing to an interview.
	return Intent{
		Kind:       KindNewTask,
		Confidence: 0.4,
		GoalText:   trimmed,
		Workspace:  extractWorkspace(trimmed),
	}, nil
}

// classifyByRegex tries the heuristic patterns in priority order. The order
// matters — STATUS triggers on "what's running" which would also match the
// EXPLAIN pattern; we want the action verb to win over the meta-question.
//
// Returns (intent, true) on a match, (zero, false) when no pattern fires.
func classifyByRegex(msg string) (Intent, bool) {
	switch {
	case statusRE.MatchString(msg):
		return Intent{Kind: KindStatus, Confidence: 0.95}, true
	case resumeRE.MatchString(msg):
		return Intent{
			Kind:       KindResume,
			Confidence: 0.9,
			SessionID:  extractSessionID(msg),
		}, true
	case helpRE.MatchString(msg):
		return Intent{Kind: KindHelp, Confidence: 0.95}, true
	case explainRE.MatchString(msg):
		return Intent{Kind: KindExplain, Confidence: 0.85}, true
	}
	return Intent{}, false
}

// classifyByLLM round-trips a single small completion. The temperature is
// pinned to 0 so the same input always classifies the same way — important
// for chat surfaces that users learn by repetition.
func classifyByLLM(ctx context.Context, prov provider.Provider, model, msg string) (Intent, error) {
	resp, err := prov.Complete(ctx, provider.Request{
		Model:       model,
		System:      classifierSystemPrompt,
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: msg}},
		MaxTokens:   200,
		Temperature: 0.0,
	})
	if err != nil {
		return Intent{}, fmt.Errorf("intent.Classify provider: %w", err)
	}
	return parseLLMResponse(resp.Text, msg)
}

// parseLLMResponse extracts the first JSON object from text and validates
// the shape. Models occasionally wrap the JSON in code fences despite the
// "no markdown" instruction, so we strip a leading ```json prefix if
// present. Anything else that fails the schema falls back to UNKNOWN.
func parseLLMResponse(text, originalMsg string) (Intent, error) {
	trimmed := strings.TrimSpace(text)
	// Strip leading/trailing fences if the model added them.
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var raw struct {
		Kind       string  `json:"kind"`
		Confidence float64 `json:"confidence"`
		Goal       string  `json:"goal"`
		Workspace  string  `json:"workspace"`
		SessionID  string  `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return Intent{
			Kind:       KindNewTask,
			Confidence: 0.3,
			GoalText:   originalMsg,
			Workspace:  extractWorkspace(originalMsg),
		}, nil
	}

	kind := normalizeKind(raw.Kind)
	conf := raw.Confidence
	if conf < 0 {
		conf = 0
	}
	if conf > 1 {
		conf = 1
	}

	it := Intent{
		Kind:       kind,
		Confidence: conf,
		GoalText:   raw.Goal,
		Workspace:  raw.Workspace,
		SessionID:  raw.SessionID,
	}

	// Belt-and-suspenders: if the LLM said NEW_TASK but didn't echo the
	// goal back, use the user's original message. Saves a round trip.
	if it.Kind == KindNewTask && strings.TrimSpace(it.GoalText) == "" {
		it.GoalText = originalMsg
	}
	if it.Workspace == "" {
		it.Workspace = extractWorkspace(originalMsg)
	}
	return it, nil
}

// normalizeKind maps the LLM's string into one of our Kind constants.
// Unknown values become KindUnknown so the REPL re-prompts.
func normalizeKind(s string) Kind {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case string(KindNewTask):
		return KindNewTask
	case string(KindResume):
		return KindResume
	case string(KindStatus):
		return KindStatus
	case string(KindHelp):
		return KindHelp
	case string(KindExplain):
		return KindExplain
	default:
		return KindUnknown
	}
}

// extractWorkspace pulls a path-like token out of msg. We expand `~/...` to
// an absolute path only when the caller is going to use it for spec.workspace
// — but the chat REPL handles that resolution; we keep the literal here.
//
// Returns "" when no path-like substring is found. Bare relative names
// (e.g. "myapp") are not heuristically promoted to paths because that
// would too often misfire on goal verbs ("update myapp").
func extractWorkspace(msg string) string {
	m := pathRE.FindStringSubmatch(msg)
	if len(m) < 2 {
		return ""
	}
	candidate := strings.TrimSpace(m[1])
	// Drop trailing punctuation a user typed naturally ("at ~/foo,").
	candidate = strings.TrimRight(candidate, ".,;:!?)")
	if candidate == "" {
		return ""
	}
	return filepath.Clean(candidate)
}

// extractSessionID returns the first ULID-like token in msg, or "" when
// none is present. Used as a fast-path for RESUME so users who paste a
// short id ("continue 01h2j3") skip the picker.
func extractSessionID(msg string) string {
	m := idRE.FindStringSubmatch(msg)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
