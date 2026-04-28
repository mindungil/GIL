// Package render defines the chat surface rendering contract.
//
// V1 ships StdoutChatRenderer. Future Bubbletea / web / desktop
// renderers implement the same interface; the REPL is renderer-agnostic.
package render

type Phase string

const (
	PhaseIdle            Phase = "idle"
	PhaseInterview       Phase = "interview"
	PhaseAwaitingConfirm Phase = "awaiting-confirm"
	PhaseRun             Phase = "run"
	PhaseStuck           Phase = "stuck"
	PhaseDone            Phase = "done"
)

type NoteKind string

const (
	NoteSpec       NoteKind = "spec"
	NoteAdversary  NoteKind = "adversary"
	NoteSaturation NoteKind = "saturation"
	NoteQueued     NoteKind = "note"
	NoteV11        NoteKind = "v11"
	NoteSystem     NoteKind = "system"
)

type SessionState struct {
	SessionID    string
	DisplayName  string
	Phase        Phase
	SlotsFilled  int
	SlotsTotal   int
	Saturation   float64
	AdvFindings  int
	Iter         int
	MaxIter      int
	CostUSD      float64
	Autonomy     string
	ChecksPassed int
	ChecksTotal  int
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
	SystemNote(kind NoteKind, msg string)
	StatusStrip(state SessionState)
	PromptCue()
	Confirm(question string, def bool) (bool, error)
	Diff(hunks []DiffHunk)
	Spec(view *SpecView)
	Close() error
}
