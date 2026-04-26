package stuck

import (
	"context"
	"errors"
	"fmt"

	"github.com/jedutools/gil/core/checkpoint"
)

// Strategy is one tactic for recovering from a detected stuck Signal.
// Strategies are pure planners: they return a Decision describing what
// AgentLoop should do next iteration. They DO NOT mutate state directly.
type Strategy interface {
	Name() string
	// Apply consults the signal + current state and returns a Decision.
	// err is non-nil only on unrecoverable internal errors; "no help available"
	// is expressed by returning Decision{Action: ActionNone}, nil.
	Apply(ctx context.Context, req ApplyRequest) (Decision, error)
}

// CheckpointReader is the subset of *checkpoint.ShadowGit that strategies
// need. *checkpoint.ShadowGit satisfies it naturally.
type CheckpointReader interface {
	ListCommits(ctx context.Context) ([]checkpoint.CommitInfo, error)
}

// ApplyRequest carries the minimum read-only context a strategy needs.
type ApplyRequest struct {
	Signal       Signal
	CurrentModel string          // model id currently being used
	ModelChain   []string        // ordered fallback chain (may be empty)
	Iteration    int             // current iteration count
	Checkpoint   CheckpointReader // nil-safe; nil → strategy returns ErrNoFallback
}

// Action enumerates the operations AgentLoop knows how to perform.
type Action int

const (
	ActionNone        Action = iota
	ActionSwitchModel        // Decision.NewModel is the model to use next iteration
	// Reserved for Phase 6:
	ActionAltToolOrder
	ActionSubagentBranch
	ActionResetSection
	ActionAdversaryConsult
)

// String returns a human-readable name for a.
func (a Action) String() string {
	switch a {
	case ActionNone:
		return "None"
	case ActionSwitchModel:
		return "SwitchModel"
	case ActionAltToolOrder:
		return "AltToolOrder"
	case ActionSubagentBranch:
		return "SubagentBranch"
	case ActionResetSection:
		return "ResetSection"
	case ActionAdversaryConsult:
		return "AdversaryConsult"
	default:
		return "Unknown"
	}
}

// Decision describes what AgentLoop should do next iteration.
type Decision struct {
	Action      Action
	NewModel    string // valid when Action == ActionSwitchModel
	Explanation string // human-readable reason; emitted as event detail
	RestoreSHA  string // valid when Action == ActionResetSection
}

// ErrNoFallback indicates ModelEscalateStrategy has exhausted its chain.
var ErrNoFallback = errors.New("model escalation chain exhausted")

// --------------------------------------------------------------------------
// ModelEscalateStrategy
// --------------------------------------------------------------------------

// ModelEscalateStrategy advances to the next model in the escalation chain.
// Given the current model and the chain, it finds the current model's index
// and returns the next one. If the chain is empty, the current model is not
// in the chain, or the current model is the last entry it returns ErrNoFallback.
type ModelEscalateStrategy struct{}

func (ModelEscalateStrategy) Name() string { return "model_escalate" }

func (ModelEscalateStrategy) Apply(ctx context.Context, req ApplyRequest) (Decision, error) {
	if len(req.ModelChain) == 0 {
		return Decision{}, ErrNoFallback
	}
	idx := -1
	for i, m := range req.ModelChain {
		if m == req.CurrentModel {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(req.ModelChain)-1 {
		return Decision{}, ErrNoFallback
	}
	next := req.ModelChain[idx+1]
	return Decision{
		Action:      ActionSwitchModel,
		NewModel:    next,
		Explanation: "escalating from " + req.CurrentModel + " to " + next + " due to " + req.Signal.Pattern.String(),
	}, nil
}

// --------------------------------------------------------------------------
// Phase 6 stub strategies
// --------------------------------------------------------------------------

// AltToolOrderStrategy hints the agent to try a different tool sequence after
// a repeat-loop pattern fires. Inspired by Cline's loop-detection soft warning
// (cline/src/core/task/loop-detection.ts: when consecutiveIdenticalToolCount
// hits LOOP_DETECTION_SOFT_THRESHOLD=3, a warning is injected to give the LLM
// one chance to self-correct before hard escalation).
//
// Applies only to action-level repetition patterns:
//   - PatternRepeatedActionObservation
//   - PatternRepeatedActionError
//   - PatternPingPong
//
// For other patterns (Monologue, ContextWindow), returns ErrNoFallback so
// the next strategy in the chain can handle it.
type AltToolOrderStrategy struct{}

func (AltToolOrderStrategy) Name() string { return "alt_tool_order" }

func (s AltToolOrderStrategy) Apply(ctx context.Context, req ApplyRequest) (Decision, error) {
	switch req.Signal.Pattern {
	case PatternRepeatedActionObservation,
		PatternRepeatedActionError,
		PatternPingPong:
		// Build a one-line nudge that the runner will prepend to the next
		// iteration's system prompt. Keep it terse — Cline's pattern is to
		// give ONE chance to self-correct before escalating.
		explanation := buildAltToolOrderHint(req.Signal)
		return Decision{
			Action:      ActionAltToolOrder,
			Explanation: explanation,
		}, nil
	default:
		return Decision{}, ErrNoFallback
	}
}

func buildAltToolOrderHint(sig Signal) string {
	return fmt.Sprintf(
		"STUCK PATTERN DETECTED (%s): you just repeated the same action %d times. Try a DIFFERENT tool or different arguments on this iteration. If the previous approach was fundamentally wrong, take a step back and reconsider. Detail: %s",
		sig.Pattern.String(), sig.Count, sig.Detail,
	)
}

// SubagentBranchStrategy will dispatch a fresh sub-engine on the same goal.
// Phase 6: requires sub-engine API.
type SubagentBranchStrategy struct{}

func (SubagentBranchStrategy) Name() string { return "subagent_branch" }
func (SubagentBranchStrategy) Apply(ctx context.Context, req ApplyRequest) (Decision, error) {
	return Decision{}, ErrNoFallback // Phase 6
}

// ResetSectionStrategy rolls the workspace back to the second-newest
// shadow-git checkpoint, giving the agent a clean slate at the last known
// good state. Inspired by Cline's resetHead (CheckpointTracker.ts:336)
// which performs git reset --hard to a target commit.
//
// Applies only to action-level repetition patterns (where rolling back
// undoes the doomed work). Returns ErrNoFallback if Checkpoint is nil
// (no shadow git in this run) or fewer than 2 commits exist (nothing to
// roll back to).
type ResetSectionStrategy struct{}

func (ResetSectionStrategy) Name() string { return "reset_section" }

func (s ResetSectionStrategy) Apply(ctx context.Context, req ApplyRequest) (Decision, error) {
	switch req.Signal.Pattern {
	case PatternRepeatedActionObservation, PatternRepeatedActionError:
		// ok — these are the patterns that benefit from "undo last step + retry"
	default:
		return Decision{}, ErrNoFallback
	}
	if req.Checkpoint == nil {
		return Decision{}, ErrNoFallback
	}
	commits, err := req.Checkpoint.ListCommits(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("reset_section: list commits: %w", err)
	}
	if len(commits) < 2 {
		return Decision{}, ErrNoFallback // need at least 2 commits to roll back to one
	}
	// commits is newest-first; commits[1] is the second-newest = target
	target := commits[1]
	return Decision{
		Action:      ActionResetSection,
		RestoreSHA:  target.SHA,
		Explanation: fmt.Sprintf("rolled back to checkpoint %s (%q) due to %s", short(target.SHA), target.Message, req.Signal.Pattern.String()),
	}, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// AdversaryConsultStrategy will invoke the adversary on the stuck context.
// Phase 6: requires adversary turn from runtime.
type AdversaryConsultStrategy struct{}

func (AdversaryConsultStrategy) Name() string { return "adversary_consult" }
func (AdversaryConsultStrategy) Apply(ctx context.Context, req ApplyRequest) (Decision, error) {
	return Decision{}, ErrNoFallback // Phase 6
}
