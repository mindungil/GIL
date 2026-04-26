package interview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jedutools/gil/core/provider"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// SlotFiller analyzes a user reply and updates the working spec accordingly.
// It uses the LLM to extract structured field updates from natural-language input.
type SlotFiller struct {
	prov  provider.Provider
	model string
}

// NewSlotFiller returns a SlotFiller using the given provider and model.
func NewSlotFiller(p provider.Provider, model string) *SlotFiller {
	return &SlotFiller{prov: p, model: model}
}

const slotFillSystem = `You extract structured spec field updates from a user's reply during a software project interview.

Output STRICT JSON only — no prose, no fences. Schema:
{"updates":[{"field":"<dotted.path>","value":<any-json-value>}]}

Supported fields (dotted paths):
- goal.one_liner (string)
- goal.detailed (string)
- goal.success_criteria_natural (array of strings)
- goal.non_goals (array of strings)
- constraints.tech_stack (array of strings)
- constraints.forbidden (array of strings)
- constraints.license (string)
- constraints.code_style (string)
- verification.checks (array of {name,kind,command,expected_exit_code})
- workspace.backend (string: "LOCAL_NATIVE"|"LOCAL_SANDBOX"|"DOCKER"|"SSH"|"VM")
- models.main / models.weak / models.editor / models.adversary / models.interview (object: {provider,modelId})
- risk.autonomy (string: "PLAN_ONLY"|"ASK_PER_ACTION"|"ASK_DESTRUCTIVE_ONLY"|"FULL")

Only extract fields the user explicitly stated or strongly implied. If nothing extractable, return {"updates":[]}.`

// Apply parses the user reply, extracts spec field updates via LLM, and mutates st.Spec.
// Does not append to history (caller's responsibility).
func (f *SlotFiller) Apply(ctx context.Context, st *State, userReply string) error {
	resp, err := f.prov.Complete(ctx, provider.Request{
		Model:     f.model,
		System:    slotFillSystem,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: userReply}},
		MaxTokens: 800,
	})
	if err != nil {
		return fmt.Errorf("slotfill provider: %w", err)
	}

	var parsed struct {
		Updates []struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		} `json:"updates"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return fmt.Errorf("slotfill parse %q: %w", resp.Text, err)
	}

	for _, u := range parsed.Updates {
		// Silently skip malformed updates so one bad field doesn't kill the batch.
		_ = applyFieldUpdate(st.Spec, u.Field, u.Value)
	}
	return nil
}

// applyFieldUpdate sets a field in fs using a dotted path.
// Returns nil for unknown paths (silent skip).
func applyFieldUpdate(fs *gilv1.FrozenSpec, field string, raw json.RawMessage) error {
	parts := strings.Split(field, ".")
	if len(parts) < 2 {
		return nil
	}

	switch parts[0] {
	case "goal":
		if fs.Goal == nil {
			fs.Goal = &gilv1.Goal{}
		}
		switch parts[1] {
		case "one_liner":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			fs.Goal.OneLiner = s
		case "detailed":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			fs.Goal.Detailed = s
		case "success_criteria_natural":
			var arr []string
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
			fs.Goal.SuccessCriteriaNatural = arr
		case "non_goals":
			var arr []string
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
			fs.Goal.NonGoals = arr
		}

	case "constraints":
		if fs.Constraints == nil {
			fs.Constraints = &gilv1.Constraints{}
		}
		switch parts[1] {
		case "tech_stack":
			var arr []string
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
			fs.Constraints.TechStack = arr
		case "forbidden":
			var arr []string
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
			fs.Constraints.Forbidden = arr
		case "license":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			fs.Constraints.License = s
		case "code_style":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			fs.Constraints.CodeStyle = s
		}

	case "workspace":
		if fs.Workspace == nil {
			fs.Workspace = &gilv1.Workspace{}
		}
		if parts[1] == "backend" {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			if v, ok := gilv1.WorkspaceBackend_value[s]; ok {
				fs.Workspace.Backend = gilv1.WorkspaceBackend(v)
			}
		}

	case "risk":
		if fs.Risk == nil {
			fs.Risk = &gilv1.RiskProfile{}
		}
		if parts[1] == "autonomy" {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			if v, ok := gilv1.AutonomyDial_value[s]; ok {
				fs.Risk.Autonomy = gilv1.AutonomyDial(v)
			}
		}

	case "models":
		if fs.Models == nil {
			fs.Models = &gilv1.ModelConfig{}
		}
		var mc struct {
			Provider string `json:"provider"`
			ModelId  string `json:"modelId"`
		}
		if err := json.Unmarshal(raw, &mc); err != nil {
			return err
		}
		choice := &gilv1.ModelChoice{Provider: mc.Provider, ModelId: mc.ModelId}
		switch parts[1] {
		case "main":
			fs.Models.Main = choice
		case "weak":
			fs.Models.Weak = choice
		case "editor":
			fs.Models.Editor = choice
		case "adversary":
			fs.Models.Adversary = choice
		case "interview":
			fs.Models.Interview = choice
		}

	case "verification":
		if fs.Verification == nil {
			fs.Verification = &gilv1.Verification{}
		}
		if parts[1] == "checks" {
			var checks []struct {
				Name             string `json:"name"`
				Kind             string `json:"kind"`
				Command          string `json:"command"`
				ExpectedExitCode int32  `json:"expected_exit_code"`
			}
			if err := json.Unmarshal(raw, &checks); err != nil {
				return err
			}
			fs.Verification.Checks = nil
			for _, c := range checks {
				kind := gilv1.CheckKind_SHELL
				if v, ok := gilv1.CheckKind_value[c.Kind]; ok {
					kind = gilv1.CheckKind(v)
				}
				fs.Verification.Checks = append(fs.Verification.Checks, &gilv1.Check{
					Name: c.Name, Kind: kind, Command: c.Command, ExpectedExitCode: c.ExpectedExitCode,
				})
			}
		}
	}
	return nil
}
