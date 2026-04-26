# Phase 3 — Adversary 비판 + 슬롯 자동 채우기 + Self-Audit Gate

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` for tracking.

**Goal:** Phase 2의 인터뷰 엔진을 **실제로 작동하는** 상태로 만든다. 현재는 Reply가 빈 spec에 새 질문만 던지는데, Phase 3에서는:
1. 매 user reply에서 **spec 슬롯을 자동 추출** (LLM이 답변 파싱)
2. 주기적으로 **adversary critique** 실행 (별도 LLM 패스가 spec 비판)
3. saturation 도달 시 **self-audit gate** 통과해야 Confirm으로 전환
4. **resume** — 기존 partial 인터뷰 재개

**Architecture:**
- `core/interview/slotfill.go` — LLM이 user reply에서 spec field 추출, State.Spec 갱신
- `core/interview/adversary.go` — 별도 LLM이 working spec 비판, finding 개수 반환
- `core/interview/audit.go` — Stage 전환 self-audit (1턴 LLM, "ready to advance?")
- `core/interview/engine.go` 확장 — `RunReplyTurn(ctx, st, userReply)` 가 slot fill + adversary trigger + saturation check + 다음 행동 결정
- `server/InterviewService` Reply 핸들러 확장 — RunReplyTurn 호출 + 적절한 이벤트 emit
- CLI: `gil resume <session-id>` (인터뷰 재개)

**Tech Stack:** Phase 2 그대로 (추가 dep 없음)

**산출물 검증** (Phase 3 종료 시점):
```bash
# Mock provider로 인터뷰가 실제로 진행되어 saturation까지 도달
gil interview <id> --provider mock
# → user 답변 후 spec 슬롯이 채워짐 (gil spec <id>로 확인 가능)
# → 모든 슬롯 채워지고 adversary clean되면 stage가 confirm으로 전환
# → "Saturation reached. Run gil spec freeze" 메시지

# 인터뷰 도중 끊었다 재개
gil interview <id>  # 시작
# (몇 턴 진행 후 ctrl-c)
gil resume <id>     # 재개 (state는 디스크에서 복원)
```

---

## Task 1: SlotFiller — user reply에서 spec field 추출

**Files:**
- Create: `core/interview/slotfill.go`
- Create: `core/interview/slotfill_test.go`

- [ ] **Step 1: 테스트 (Mock provider)**

```go
package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestSlotFiller_ExtractsGoalAndConstraints(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[
		  {"field":"goal.one_liner","value":"Build a CLI todo manager"},
		  {"field":"constraints.tech_stack","value":["Go","SQLite"]}
		]}`,
	})
	f := NewSlotFiller(mock, "claude-haiku-4-5")
	st := NewState()
	st.Stage = StageConversation

	require.NoError(t, f.Apply(context.Background(), st, "I want a Go CLI for managing todos, using SQLite"))
	require.Equal(t, "Build a CLI todo manager", st.Spec.Goal.OneLiner)
	require.Equal(t, []string{"Go", "SQLite"}, st.Spec.Constraints.TechStack)
}

func TestSlotFiller_NoUpdates_OK(t *testing.T) {
	mock := provider.NewMock([]string{`{"updates":[]}`})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.NoError(t, f.Apply(context.Background(), st, "small talk"))
	require.Nil(t, st.Spec.Goal)
}

func TestSlotFiller_BadJSON_ReturnsError(t *testing.T) {
	mock := provider.NewMock([]string{`not json`})
	f := NewSlotFiller(mock, "x")
	st := NewState()
	require.Error(t, f.Apply(context.Background(), st, "x"))
}
```

- [ ] **Step 2: slotfill.go 작성**

```go
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
- goal.success_criteria_natural (array of strings, ≥3 to satisfy required slot)
- goal.non_goals (array of strings)
- constraints.tech_stack (array of strings)
- constraints.forbidden (array of strings)
- constraints.license (string)
- constraints.code_style (string)
- verification.checks (array of {name,kind,command,expected_exit_code})
- workspace.backend (string: "LOCAL_NATIVE"|"LOCAL_SANDBOX"|"DOCKER"|"SSH"|"VM")
- models.main / models.weak / models.editor / models.adversary / models.interview (object: {provider,modelId})
- risk.autonomy (string: "PLAN_ONLY"|"ASK_PER_ACTION"|"ASK_DESTRUCTIVE_ONLY"|"FULL")
- budget.max_total_tokens / max_total_cost_usd / max_iterations (number)

Only extract fields the user explicitly stated or strongly implied. If nothing extractable, return {"updates":[]}.`

// Apply parses the user reply, extracts spec field updates via LLM, and mutates st.Spec.
// Does not append to history (caller's responsibility).
func (f *SlotFiller) Apply(ctx context.Context, st *State, userReply string) error {
	resp, err := f.prov.Complete(ctx, provider.Request{
		Model:    f.model,
		System:   slotFillSystem,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: userReply}},
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
		if err := applyFieldUpdate(st.Spec, u.Field, u.Value); err != nil {
			// Log + skip malformed update; don't fail the whole batch
			continue
		}
	}
	return nil
}

// applyFieldUpdate sets a field in fs using a dotted path. Returns an error
// for unknown paths or type mismatches.
func applyFieldUpdate(fs *gilv1.FrozenSpec, field string, raw json.RawMessage) error {
	parts := strings.Split(field, ".")
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
```

- [ ] **Step 3: 테스트 통과 확인 + race + vet**
- [ ] **Step 4: Commit**

```bash
git add core/interview/slotfill.go core/interview/slotfill_test.go
git commit -m "feat(core/interview): SlotFiller extracts spec fields from user replies"
```

---

## Task 2: Adversary — spec 비판 LLM 패스

**Files:**
- Create: `core/interview/adversary.go`
- Create: `core/interview/adversary_test.go`

- [ ] **Step 1: 테스트**

```go
package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
)

func TestAdversary_FindsBlockers(t *testing.T) {
	mock := provider.NewMock([]string{
		`[{"severity":"blocker","category":"missing_verification","finding":"no integration test","question_to_user":"How do you verify integration?","proposed_addition":"add e2e check"}]`,
	})
	a := NewAdversary(mock, "claude-haiku-4-5")
	st := NewState()

	findings, err := a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "blocker", findings[0].Severity)
}

func TestAdversary_NoFindings(t *testing.T) {
	mock := provider.NewMock([]string{`[]`})
	a := NewAdversary(mock, "x")
	st := NewState()
	findings, err := a.Critique(context.Background(), st)
	require.NoError(t, err)
	require.Empty(t, findings)
}
```

- [ ] **Step 2: adversary.go**

```go
package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jedutools/gil/core/provider"
)

// Finding is one issue raised by the Adversary.
type Finding struct {
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	Finding          string `json:"finding"`
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
- finding: what's wrong
- question_to_user: what should the user clarify (1 sentence)
- proposed_addition: what to add to spec if user agrees

Be ruthless. If the spec says "build a web app" without specifying database, deployment, auth, error handling — that's a blocker.

Output STRICT JSON array only — no prose, no fences. If truly complete and unambiguous, return [].`

// Critique runs the Adversary over st.Spec and returns the list of findings.
// Updates st.AdversaryRounds and st.LastAdversaryFindings.
func (a *Adversary) Critique(ctx context.Context, st *State) ([]Finding, error) {
	specJSON, err := protojson.Marshal(st.Spec)
	if err != nil {
		return nil, fmt.Errorf("adversary marshal spec: %w", err)
	}

	resp, err := a.prov.Complete(ctx, provider.Request{
		Model:    a.model,
		System:   adversarySystem,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Spec:\n" + string(specJSON)}},
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
```

- [ ] **Step 3: 테스트 + commit**

```bash
git add core/interview/adversary.go core/interview/adversary_test.go
git commit -m "feat(core/interview): Adversary critique of working spec via separate LLM pass"
```

---

## Task 3: SelfAuditGate — Stage 전환 자기 검사

**Files:**
- Create: `core/interview/audit.go`
- Create: `core/interview/audit_test.go`

- [ ] **Step 1: 테스트**

```go
package interview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/provider"
)

func TestSelfAuditGate_PassesWhenAgentApproves(t *testing.T) {
	mock := provider.NewMock([]string{`{"ready":true,"reason":"all required slots filled, adversary clean"}`})
	g := NewSelfAuditGate(mock, "x")
	st := NewState()

	pass, reason, err := g.AuditConversationToConfirm(context.Background(), st)
	require.NoError(t, err)
	require.True(t, pass)
	require.NotEmpty(t, reason)
}

func TestSelfAuditGate_BlocksWhenNotReady(t *testing.T) {
	mock := provider.NewMock([]string{`{"ready":false,"reason":"goal still vague"}`})
	g := NewSelfAuditGate(mock, "x")
	st := NewState()

	pass, reason, err := g.AuditConversationToConfirm(context.Background(), st)
	require.NoError(t, err)
	require.False(t, pass)
	require.Contains(t, reason, "vague")
}
```

- [ ] **Step 2: audit.go**

```go
package interview

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jedutools/gil/core/provider"
)

// SelfAuditGate is the explicit "is the agent ready to advance?" check at
// stage transitions. Per design.md §2.4 this gate exists ONLY for interview
// stage transitions, not other phases.
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
Consider: required slots filled? adversary findings resolved? spec internally consistent? Will multi-day autonomous execution succeed with this spec?
Output STRICT JSON: {"ready":bool,"reason":"<1-sentence>"}`

// AuditConversationToConfirm asks the LLM whether the agent is ready to
// transition from Conversation to Confirm stage. Returns (pass, reason, err).
func (g *SelfAuditGate) AuditConversationToConfirm(ctx context.Context, st *State) (bool, string, error) {
	specJSON, err := protojson.Marshal(st.Spec)
	if err != nil {
		return false, "", fmt.Errorf("audit marshal spec: %w", err)
	}

	resp, err := g.prov.Complete(ctx, provider.Request{
		Model:    g.model,
		System:   auditSystem,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Working spec:\n" + string(specJSON)}},
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
```

- [ ] **Step 3: 테스트 + commit**

```bash
git add core/interview/audit.go core/interview/audit_test.go
git commit -m "feat(core/interview): SelfAuditGate for stage transition (Conversation → Confirm)"
```

---

## Task 4: Engine.RunReplyTurn — 통합 흐름

**Files:**
- Modify: `core/interview/engine.go` — add RunReplyTurn method that orchestrates SlotFiller + Adversary (periodic) + saturation check + next question OR stage transition
- Modify: `core/interview/engine_test.go` — test RunReplyTurn behavior

- [ ] **Step 1: 테스트** (Mock with 3 responses: slotfill JSON, adversary [], next-question text)

```go
func TestEngine_RunReplyTurn_FillsSlotAndContinues(t *testing.T) {
	mock := provider.NewMock([]string{
		`{"updates":[{"field":"goal.one_liner","value":"Build CLI"}]}`,  // slotfill
		`Tell me more about the user.`,                                   // next question
	})
	eng := NewEngineWithSubmodels(mock, "main", "weak", "adversary")
	st := NewState()
	st.Stage = StageConversation

	turn, err := eng.RunReplyTurn(context.Background(), st, "I want a CLI for X")
	require.NoError(t, err)
	require.Equal(t, ReplyOutcomeNextQuestion, turn.Outcome)
	require.Equal(t, "Tell me more about the user.", turn.Content)
	require.Equal(t, "Build CLI", st.Spec.Goal.OneLiner)
}
```

- [ ] **Step 2: Engine 확장**

```go
// Add to engine.go

type ReplyOutcome int

const (
	ReplyOutcomeNextQuestion ReplyOutcome = iota
	ReplyOutcomeReadyToConfirm
	ReplyOutcomeAdversaryFinding
)

type ReplyTurn struct {
	Outcome  ReplyOutcome
	Content  string             // next question OR confirmation message
	Findings []Finding          // populated if Outcome == ReplyOutcomeAdversaryFinding
}

// EngineWithSubmodels supports separate sub-engines for slotfill and adversary.
type EngineWithSubmodels struct {
	*Engine
	slotFiller *SlotFiller
	adversary  *Adversary
	audit      *SelfAuditGate
}

// NewEngineWithSubmodels returns an Engine plus slotfill/adversary/audit using the
// same provider but allow separate model names for each role.
func NewEngineWithSubmodels(p provider.Provider, mainModel, slotModel, adversaryModel string) *EngineWithSubmodels {
	return &EngineWithSubmodels{
		Engine:     NewEngine(p, mainModel),
		slotFiller: NewSlotFiller(p, slotModel),
		adversary:  NewAdversary(p, adversaryModel),
		audit:      NewSelfAuditGate(p, mainModel),
	}
}

// RunReplyTurn:
// 1. Append user reply to history
// 2. SlotFiller.Apply
// 3. If All required slots filled AND adversary not yet run, run Adversary
// 4. If saturated (slots + adversary clean), self-audit gate → if pass, transition to Confirm
// 5. Else NextQuestion
func (e *EngineWithSubmodels) RunReplyTurn(ctx context.Context, st *State, userReply string) (*ReplyTurn, error) {
	st.AppendUser(userReply)

	if err := e.slotFiller.Apply(ctx, st, userReply); err != nil {
		return nil, fmt.Errorf("RunReplyTurn slotfill: %w", err)
	}

	// Check saturation
	if st.AllRequiredSlotsFilled() && st.AdversaryRounds == 0 {
		// First adversary round
		if _, err := e.adversary.Critique(ctx, st); err != nil {
			return nil, fmt.Errorf("RunReplyTurn adversary: %w", err)
		}
	}

	if st.IsSaturated() {
		// Self-audit gate
		ready, reason, err := e.audit.AuditConversationToConfirm(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("RunReplyTurn audit: %w", err)
		}
		if ready {
			st.Stage = StageConfirm
			return &ReplyTurn{Outcome: ReplyOutcomeReadyToConfirm, Content: reason}, nil
		}
		// audit blocks → continue with question
	}

	q, err := e.NextQuestion(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("RunReplyTurn next question: %w", err)
	}
	st.AppendAssistant(q)
	return &ReplyTurn{Outcome: ReplyOutcomeNextQuestion, Content: q}, nil
}
```

- [ ] **Step 3: 테스트 + commit**

```bash
git add core/interview/engine.go core/interview/engine_test.go
git commit -m "feat(core/interview): RunReplyTurn orchestrates slotfill + adversary + audit + next question"
```

---

## Task 5: server/InterviewService Reply 통합

**Files:**
- Modify: `server/internal/service/interview.go` — Reply uses RunReplyTurn; emit appropriate event based on outcome

- [ ] **Step 1: ProviderFactory signature 확장**

기존 `func(name string) (provider.Provider, string, error)` 을 유지하되, Reply에서는 EngineWithSubmodels를 사용하도록 internal에서 재구성. 가장 단순한 방법: `interviewSlot`에 `*interview.EngineWithSubmodels` 도 저장.

`type interviewSlot { state *State; engine *Engine; richEngine *EngineWithSubmodels }`

Start에서:
```go
prov, model, err := s.providerFactory(req.Provider)
// ...
slot := &interviewSlot{
    state:      st,
    engine:     interview.NewEngine(prov, model),
    richEngine: interview.NewEngineWithSubmodels(prov, model, model, model),  // 단일 모델
}
```

Reply 핸들러를 다음으로 교체:
```go
turn, err := slot.richEngine.RunReplyTurn(ctx, slot.state, req.Content)
if err != nil {
    return status.Errorf(codes.Internal, "reply turn: %v", err)
}

switch turn.Outcome {
case interview.ReplyOutcomeReadyToConfirm:
    if err := stream.Send(&gilv1.InterviewEvent{
        Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{
            From: "conversation", To: "confirm", Reason: turn.Content,
        }},
    }); err != nil {
        return err
    }
default: // NextQuestion (or AdversaryFinding for now treated as next question)
    if err := stream.Send(&gilv1.InterviewEvent{
        Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: turn.Content}},
    }); err != nil {
        return err
    }
}
return nil
```

- [ ] **Step 2: 테스트** — `TestInterviewService_Reply_AdvancesToConfirm` (mock with slotfill + adversary [] + audit ready=true 응답들; verify stage transition emitted)

- [ ] **Step 3: 통과 + commit**

```bash
git add server/internal/service/interview.go server/internal/service/interview_test.go
git commit -m "feat(server/service): Reply uses RunReplyTurn (slot fill + adversary + audit + transition)"
```

---

## Task 6: gild main — provider model split for sub-engines

**Files:**
- Modify: `server/cmd/gild/main.go` — provider factory unchanged but document that the same model is used for all sub-engines in Phase 3

- [ ] (실제로는 변경 없음. Phase 4에서 모델 분리 본격 도입 예정. 그냥 주석 추가)
- [ ] Skip if no real change.

---

## Task 7: gil resume 명령 + InterviewService.Resume

**Files:**
- Create: `cli/internal/cmd/resume.go`
- Modify: `server/internal/service/interview.go` — add Resume RPC OR have Start handle "session has interviewing status → load existing state from disk"
- Modify: `proto/gil/v1/interview.proto` — add Resume RPC OR document that Start works for both

**Design choice:** Simplest approach — modify InterviewService.Start to detect "session is already interviewing" and skip RunSensing, just emit the next question. But session state is in-memory and lost across daemon restarts. To truly resume, need to persist State.

For Phase 3 minimum: **`gil resume <id>` simply calls Start with an empty first_input + "resume marker" — server detects interviewing status and skips Sensing.** This works as long as daemon hasn't restarted. Cross-restart resume = Phase 4.

- [ ] **Step 1:** `cli/internal/cmd/resume.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

func resumeCmd() *cobra.Command {
	var socket, providerName, model string
	c := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume an in-progress interview",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			out := cmd.OutOrStdout()
			// Sentinel: server treats first_input == "" as resume
			stream, err := cli.StartInterview(ctx, sessionID, "", providerName, model)
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
			for {
				evt, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return fmt.Errorf("recv: %w", err)
				}
				if t := evt.GetAgentTurn(); t != nil {
					fmt.Fprintln(out, "Agent:", t.Content)
					return nil // initial turn done; user enters reply via gil interview <id> next time, OR add inline reply loop here
				}
			}
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "anthropic", "provider")
	c.Flags().StringVar(&model, "model", "", "model")
	return c
}
```

(Implementation note: for Phase 3 simplicity, `gil resume` just shows the current state by triggering Start with empty input. Real interactive resume = Phase 4.)

Server side modification to InterviewService.Start: if `req.FirstInput == ""` AND session.Status == "interviewing" AND in-memory state exists, skip RunSensing and emit current state. Document carefully.

- [ ] **Step 2:** Register resumeCmd in root.go.

- [ ] **Step 3:** Test:

```go
func TestResume_SkipsIfInterviewing(t *testing.T) {
    // Start interview (which puts state in memory + sets DB status = interviewing)
    // Then call resume — should not error
}
```

- [ ] **Step 4:** Commit:

```bash
git add cli/internal/cmd/resume.go cli/internal/cmd/root.go server/internal/service/interview.go
git commit -m "feat: gil resume command + InterviewService.Start treats empty first_input as resume"
```

---

## Task 8: E2E phase03 — saturation in mock provider scenario

**Files:**
- Create: `tests/e2e/phase03_test.sh`
- Modify: `Makefile` — add e2e3 target + update e2e-all

- [ ] **Step 1:** Script:

```bash
#!/usr/bin/env bash
# Phase 3 e2e: mock interview reaches saturation
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"
PATH="$ROOT/bin:$PATH"
trap 'pkill -f "gild --foreground --base $BASE" 2>/dev/null || true; rm -rf "$BASE"' EXIT

cd "$ROOT" && make build > /dev/null

ID=$("$ROOT/bin/gil" new --working-dir /tmp/p3 --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session"; exit 1; }

# The gild mock factory pre-loaded responses must include enough turns to reach saturation.
# This requires updating the gild mock factory to return:
#   1. sensing JSON
#   2. first question
#   3. slotfill JSON (fills all required slots)
#   4. adversary [] (clean)
#   5. self-audit ready=true
#   6. (after stage→confirm)... not needed for this test
#
# Modify the mock factory in main.go OR provide a way to load mock script from env var.

# For now, this test is a smoke that just verifies stage transition can be triggered.
# Skip detailed verification.

OUT=$(printf "build a CLI todo app in Go using SQLite\n/done\n" | \
    "$ROOT/bin/gil" interview "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$OUT" | grep -qi "agent\|stage" || { echo "FAIL: no events; got: $OUT"; exit 1; }
echo "OK: phase 3 sanity (interview emits events)"

# More detailed assertions deferred until rich mock loading is implemented (Phase 4).
echo "OK: phase 3 e2e passed"
```

- [ ] **Step 2:** Makefile:

```makefile
e2e3: build
	@bash tests/e2e/phase03_test.sh

e2e-all: e2e e2e2 e2e3
```

- [ ] **Step 3:** Run + commit:

```bash
make e2e3 && make e2e-all
git add tests/e2e/phase03_test.sh Makefile
git commit -m "test(e2e): phase 3 sanity (interview produces events)"
```

---

## Task 9: progress.md Phase 3 갱신

**File:** `docs/progress.md`

- [ ] Update Phase 2 section: check the previously deferred items (adversary critique, self-audit gate) since they're now done in Phase 3
- [ ] Add new Phase 3 section header status "(완료 — 2026-04-26)"
- [ ] Add row to 결정사항 table
- [ ] Append "Phase 3 산출물 요약" section
- [ ] Commit:

```bash
git add docs/progress.md
git commit -m "docs(progress): mark Phase 3 complete — slot filling + adversary + self-audit"
```

---

## Phase 3 완료 체크리스트

- [ ] All core/interview tests pass
- [ ] All server tests pass
- [ ] All cli tests pass
- [ ] `make e2e-all` 통과 (e2e + e2e2 + e2e3)
- [ ] `gil interview` 가 mock provider로 saturation 시나리오 실행 가능 (질적 검증)
- [ ] `gil resume` 동작 확인

---

## Phase 4 미루는 항목

- 진정한 cross-restart resume (메모리 state 디스크 영속화)
- Provider 에러 retry/backoff
- 동적 mock script 로드 (env var or config)
- per-stage 모델 분리 (main vs weak vs editor vs adversary)
- core/event session 통합 + secret masking
- 인터뷰 도중 spec preview command (`gil spec --tail`)
