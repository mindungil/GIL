// Package interview holds the interview state machine: stage, conversation
// history, working FrozenSpec, and saturation logic. The Engine in this
// package drives state transitions via LLM calls.
package interview

import (
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/spec"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// Stage represents the interview phase.
type Stage int

const (
	// StageSensing is the initial stage where the agent estimates the project domain.
	StageSensing Stage = iota
	// StageConversation is the agent-driven Q&A loop.
	StageConversation
	// StageConfirm is awaiting user's final OK before freeze.
	StageConfirm
	// StageFrozen indicates the spec is locked; interview is read-only.
	StageFrozen
)

// String returns a lowercase identifier for the stage (for logs and gRPC).
func (s Stage) String() string {
	switch s {
	case StageSensing:
		return "sensing"
	case StageConversation:
		return "conversation"
	case StageConfirm:
		return "confirm"
	case StageFrozen:
		return "frozen"
	default:
		return "unknown"
	}
}

// State is the interview state machine. Held in memory by the daemon per session.
// Not goroutine-safe — caller should serialize access (e.g., per-session mutex
// in InterviewService).
type State struct {
	Stage                 Stage
	History               []provider.Message
	Spec                  *gilv1.FrozenSpec
	Domain                string
	DomainConfidence      float64
	AdversaryRounds       int
	LastAdversaryFindings int
}

// NewState returns an empty interview state ready for the Sensing stage.
func NewState() *State {
	return &State{
		Stage: StageSensing,
		Spec:  &gilv1.FrozenSpec{},
	}
}

// AppendUser adds a user turn to the history.
func (s *State) AppendUser(content string) {
	s.History = append(s.History, provider.Message{Role: provider.RoleUser, Content: content})
}

// AppendAssistant adds an agent turn to the history.
func (s *State) AppendAssistant(content string) {
	s.History = append(s.History, provider.Message{Role: provider.RoleAssistant, Content: content})
}

// AllRequiredSlotsFilled delegates to spec.AllRequiredSlotsFilled.
func (s *State) AllRequiredSlotsFilled() bool {
	return spec.AllRequiredSlotsFilled(s.Spec)
}

// IsSaturated returns true when (a) all required slots filled, AND
// (b) at least one adversary round has run with zero findings.
func (s *State) IsSaturated() bool {
	return s.AllRequiredSlotsFilled() &&
		s.AdversaryRounds >= 1 &&
		s.LastAdversaryFindings == 0
}
