// Package render defines the chat surface rendering contract.
//
// V1 ships StdoutChatRenderer. Future Bubbletea / web / desktop
// renderers implement the same interface; the REPL is renderer-agnostic.
package render

type Phase string

const (
	PhaseIdle  Phase = "idle"
	PhaseRun   Phase = "run"
	PhaseStuck Phase = "stuck"
	PhaseDone  Phase = "done"
)

// PhaseInterview / PhaseAwaitingConfirm removed in iter211 — the
// interview engine that produced those phase transitions was deleted
// in M3; the chat agent's freeze_spec tool now drives spec assembly
// inside the natural-language stream, so the strip never needs to
// surface "interview" or "awaiting-confirm" as distinct phases.

type NoteKind string

const (
	NoteSpec      NoteKind = "spec"
	NoteAdversary NoteKind = "adversary"
	NoteQueued    NoteKind = "note"
	NoteV11       NoteKind = "v11"
	NoteSystem    NoteKind = "system"
)

// NoteSaturation removed in iter211 — was only emitted by the
// interview.ready_to_freeze handler in loop.go, which had no producer
// after the M3 interview-engine deletion.

type SessionState struct {
	SessionID    string
	DisplayName  string
	Phase        Phase
	Iter         int
	MaxIter      int
	CostUSD      float64
	Autonomy     string
	ChecksPassed int
	ChecksTotal  int

	// Tokens accumulates total tokens consumed across the run, summed
	// from EventMetrics.Tokens on each event that carries one. Shown
	// in the strip as "X.Yk toks" alongside CostUSD when non-zero.
	Tokens int64
	// LatencyMs is the most recent provider-call latency reported via
	// EventMetrics.LatencyMs. Snapshot — overwritten by every metric-
	// carrying event so the strip reflects the latest iteration. Zero
	// when the daemon hasn't emitted a metric event yet.
	LatencyMs int64
}

type DiffHunk struct {
	Path     string
	Added    int
	Removed  int
	Snippet  string
}

type SpecView struct {
	YAML string
}

type Renderer interface {
	Banner(state SessionState)
	AssistantText(chunk string)
	// AssistantReasoning surfaces upstream-separated chain-of-thought
	// (e.g. vLLM `reasoning`, DeepSeek `reasoning_content`, Anthropic
	// extended-thinking) with distinct styling from AssistantText so
	// the user can tell the model's internal monologue apart from its
	// final reply. Implementations may dim, indent, or hide reasoning
	// behind a toggle; the contract is "do not let it be mistaken for
	// the actual answer". P33.
	AssistantReasoning(chunk string)
	SystemNote(kind NoteKind, msg string)
	StatusStrip(state SessionState)
	PromptCue()
	Confirm(question string, def bool) (bool, error)
	Diff(hunks []DiffHunk)
	Spec(view *SpecView)
	Close() error
}
