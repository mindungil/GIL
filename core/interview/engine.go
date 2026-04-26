package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jedutools/gil/core/provider"
)

// Engine drives the interview State via an LLM provider. It is stateless
// itself — all mutable data lives in *State.
type Engine struct {
	prov  provider.Provider
	model string
}

// NewEngine returns an Engine that uses the given provider and model name.
func NewEngine(p provider.Provider, model string) *Engine {
	return &Engine{prov: p, model: model}
}

const sensingSystemPrompt = `You are estimating the domain of a software project from the user's first message.
Output STRICT JSON only — no prose, no markdown fences. Schema:
{"domain":"string","domain_confidence":0.0-1.0,"tech_hints":["string"],"scale_hint":"small|medium|large|unknown","ambiguity":"string"}`

// RunSensing performs the Stage 1 domain estimation. On success, st.Domain and
// st.DomainConfidence are populated, the user's input is appended to history,
// and st.Stage advances to StageConversation. The Engine itself does not call
// AppendUser before sending — it appends after the LLM call succeeds so that
// retries don't double-record.
func (e *Engine) RunSensing(ctx context.Context, st *State, firstInput string) error {
	resp, err := e.prov.Complete(ctx, provider.Request{
		Model:     e.model,
		System:    sensingSystemPrompt,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: firstInput}},
		MaxTokens: 200,
	})
	if err != nil {
		return fmt.Errorf("interview.RunSensing provider: %w", err)
	}

	var parsed struct {
		Domain     string  `json:"domain"`
		Confidence float64 `json:"domain_confidence"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return fmt.Errorf("interview.RunSensing parse %q: %w", resp.Text, err)
	}

	st.AppendUser(firstInput)
	st.Domain = parsed.Domain
	st.DomainConfidence = parsed.Confidence
	st.Stage = StageConversation
	return nil
}

// NextQuestion asks the LLM what to ask the user next, given the current
// conversation history. Returns the question text. Caller is responsible for
// displaying it and feeding the user's reply via st.AppendUser before calling
// NextQuestion again.
func (e *Engine) NextQuestion(ctx context.Context, st *State) (string, error) {
	system := fmt.Sprintf(`You are conducting a deep requirements interview for a software project.
Domain estimate: %q
Goal: read the conversation so far and produce ONE follow-up question that maximizes information gain.
Be brief (1-2 sentences). Output ONLY the question text — no markdown, no quotes.`, st.Domain)

	resp, err := e.prov.Complete(ctx, provider.Request{
		Model:     e.model,
		System:    system,
		Messages:  st.History,
		MaxTokens: 200,
	})
	if err != nil {
		return "", fmt.Errorf("interview.NextQuestion: %w", err)
	}
	return resp.Text, nil
}

// ReplyOutcome describes what the engine decided to do after a user reply.
type ReplyOutcome int

const (
	// ReplyOutcomeNextQuestion means the engine generated the next interview question.
	ReplyOutcomeNextQuestion ReplyOutcome = iota
	// ReplyOutcomeReadyToConfirm means saturation reached + audit passed; stage advanced to Confirm.
	ReplyOutcomeReadyToConfirm
)

// ReplyTurn is the result of RunReplyTurn.
type ReplyTurn struct {
	Outcome ReplyOutcome
	Content string // next question OR audit reason
}

// EngineWithSubmodels combines the main Engine with SlotFiller, Adversary, and
// SelfAuditGate sub-engines. All sub-engines may use the same provider but
// different model names (Phase 4 will properly separate models).
type EngineWithSubmodels struct {
	*Engine
	slotFiller *SlotFiller
	adversary  *Adversary
	audit      *SelfAuditGate
}

// NewEngineWithSubmodels returns an EngineWithSubmodels using the given
// provider for all sub-engines.
//   - mainModel: question generation (Engine.NextQuestion, RunSensing)
//   - slotModel: slot extraction (SlotFiller)
//   - adversaryModel: critique (Adversary)
//   - auditModel: self-audit gate (SelfAuditGate)
func NewEngineWithSubmodels(p provider.Provider, mainModel, slotModel, adversaryModel, auditModel string) *EngineWithSubmodels {
	return &EngineWithSubmodels{
		Engine:     NewEngine(p, mainModel),
		slotFiller: NewSlotFiller(p, slotModel),
		adversary:  NewAdversary(p, adversaryModel),
		audit:      NewSelfAuditGate(p, auditModel),
	}
}

// RunReplyTurn orchestrates the per-reply pipeline:
//
//	1. Append the user reply to history.
//	2. Run SlotFiller to update st.Spec from the reply.
//	3. If all required slots are now filled AND no adversary has run yet,
//	   run Adversary to detect blockers.
//	4. If saturated (slots filled + adversary clean), run SelfAuditGate.
//	   If gate passes, advance st.Stage to StageConfirm and return
//	   ReplyOutcomeReadyToConfirm.
//	5. Otherwise call NextQuestion, append assistant reply, return
//	   ReplyOutcomeNextQuestion.
func (e *EngineWithSubmodels) RunReplyTurn(ctx context.Context, st *State, userReply string) (*ReplyTurn, error) {
	st.AppendUser(userReply)

	if err := e.slotFiller.Apply(ctx, st, userReply); err != nil {
		return nil, fmt.Errorf("RunReplyTurn slotfill: %w", err)
	}

	// First adversary round once all required slots are filled
	if st.AllRequiredSlotsFilled() && st.AdversaryRounds == 0 {
		if _, err := e.adversary.Critique(ctx, st); err != nil {
			return nil, fmt.Errorf("RunReplyTurn adversary: %w", err)
		}
	}

	if st.IsSaturated() {
		ready, reason, err := e.audit.AuditConversationToConfirm(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("RunReplyTurn audit: %w", err)
		}
		if ready {
			st.Stage = StageConfirm
			return &ReplyTurn{Outcome: ReplyOutcomeReadyToConfirm, Content: reason}, nil
		}
		// Audit blocks → fall through to NextQuestion
	}

	q, err := e.NextQuestion(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("RunReplyTurn next question: %w", err)
	}
	st.AppendAssistant(q)
	return &ReplyTurn{Outcome: ReplyOutcomeNextQuestion, Content: q}, nil
}
