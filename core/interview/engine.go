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
