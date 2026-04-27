package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mindungil/gil/core/provider"
)

// Finding is one issue raised by the Adversary about the working spec.
type Finding struct {
	Severity         string `json:"severity"`          // "blocker" | "high" | "medium" | "low"
	Category         string `json:"category"`          // "ambiguity" | "missing_constraint" | ...
	Finding          string `json:"finding"`           // what's wrong
	QuestionToUser   string `json:"question_to_user,omitempty"`
	ProposedAddition string `json:"proposed_addition,omitempty"`
}

// Adversary is a separate LLM pass that critiques the working spec to find
// gaps that would block multi-day autonomous execution.
type Adversary struct {
	prov  provider.Provider
	model string
}

// NewAdversary returns an Adversary using the given provider and model.
func NewAdversary(p provider.Provider, model string) *Adversary {
	return &Adversary{prov: p, model: model}
}

const adversarySystem = `You are an ADVERSARIAL spec reviewer. The agent will run this spec autonomously for DAYS without human input.

Find every place where this spec is INSUFFICIENT for multi-day autonomous execution. For each issue:
- severity: "blocker" | "high" | "medium" | "low"
- category: "ambiguity" | "missing_constraint" | "missing_verification" | "infeasible_goal" | "scope_creep_risk" | "model_will_get_stuck"
- finding: what's wrong (1 sentence)
- question_to_user: what should the user clarify (1 sentence, optional)
- proposed_addition: what to add to spec if user agrees (optional)

Be ruthless. If the spec says "build a web app" without specifying database, deployment, auth, error handling — that's a blocker.

Output STRICT JSON array only — no prose, no fences. If truly complete and unambiguous, return [].`

// Critique runs the Adversary over st.Spec and returns the list of findings.
// On success, increments st.AdversaryRounds and sets st.LastAdversaryFindings.
// On parse/provider error, st counters are unchanged.
func (a *Adversary) Critique(ctx context.Context, st *State) ([]Finding, error) {
	specJSON, err := protojson.Marshal(st.Spec)
	if err != nil {
		return nil, fmt.Errorf("adversary marshal spec: %w", err)
	}

	resp, err := a.prov.Complete(ctx, provider.Request{
		Model:     a.model,
		System:    adversarySystem,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "Spec:\n" + string(specJSON)}},
		MaxTokens: 2000,
	})
	if err != nil {
		return nil, fmt.Errorf("adversary provider: %w", err)
	}

	var findings []Finding
	if err := json.Unmarshal([]byte(resp.Text), &findings); err != nil {
		return nil, fmt.Errorf("adversary parse %q: %w", resp.Text, err)
	}

	st.AdversaryRounds++
	st.LastAdversaryFindings = len(findings)
	return findings, nil
}
