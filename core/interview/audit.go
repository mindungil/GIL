package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jedutools/gil/core/provider"
)

// SelfAuditGate is the explicit "is the agent ready to advance?" check at
// interview stage transitions. Per design.md §2.4 this gate exists ONLY for
// interview stage transitions, not other phases (run/stop/compact).
type SelfAuditGate struct {
	prov  provider.Provider
	model string
}

// NewSelfAuditGate returns a SelfAuditGate using the given provider and model.
func NewSelfAuditGate(p provider.Provider, model string) *SelfAuditGate {
	return &SelfAuditGate{prov: p, model: model}
}

const auditSystem = `You are the agent itself, performing a self-audit before advancing the interview to the next stage.

Question: am I truly ready to advance Conversation → Confirm (i.e., freeze the spec)?
Consider:
- Required slots filled?
- Adversary findings resolved?
- Spec internally consistent?
- Will multi-day autonomous execution succeed with this spec?

Output STRICT JSON: {"ready":bool,"reason":"<1-sentence>"}`

// AuditConversationToConfirm asks the LLM whether the agent is ready to
// transition from Conversation to Confirm stage. Returns (pass, reason, err).
// The state is NOT mutated by this call.
func (g *SelfAuditGate) AuditConversationToConfirm(ctx context.Context, st *State) (bool, string, error) {
	specJSON, err := protojson.Marshal(st.Spec)
	if err != nil {
		return false, "", fmt.Errorf("audit marshal spec: %w", err)
	}

	resp, err := g.prov.Complete(ctx, provider.Request{
		Model:     g.model,
		System:    auditSystem,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "Working spec:\n" + string(specJSON)}},
		MaxTokens: 200,
	})
	if err != nil {
		return false, "", fmt.Errorf("audit provider: %w", err)
	}

	var parsed struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return false, "", fmt.Errorf("audit parse %q: %w", resp.Text, err)
	}
	return parsed.Ready, parsed.Reason, nil
}
