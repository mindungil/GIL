package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// recordingProvider wraps a Mock and records the model field of every Complete call.
type recordingProvider struct {
	mu    sync.Mutex
	calls []string
	*provider.Mock
}

func (r *recordingProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req.Model)
	r.mu.Unlock()
	return r.Mock.Complete(ctx, req)
}

func (r *recordingProvider) modelsUsed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// fakeInterviewStream implements grpc.ServerStreamingServer[InterviewEvent].
type fakeInterviewStream struct {
	ctx    context.Context
	events []*gilv1.InterviewEvent
}

func (s *fakeInterviewStream) Send(e *gilv1.InterviewEvent) error {
	s.events = append(s.events, e)
	return nil
}

func (s *fakeInterviewStream) Context() context.Context { return s.ctx }
func (s *fakeInterviewStream) SendMsg(m any) error      { return nil }
func (s *fakeInterviewStream) RecvMsg(m any) error      { return nil }
func (s *fakeInterviewStream) SetHeader(md metadata.MD) error { return nil }
func (s *fakeInterviewStream) SendHeader(md metadata.MD) error { return nil }
func (s *fakeInterviewStream) SetTrailer(md metadata.MD) {}

// fakeReplyStream implements grpc.ServerStreamingServer[InterviewEvent] for Reply.
type fakeReplyStream struct {
	ctx    context.Context
	events []*gilv1.InterviewEvent
}

func (s *fakeReplyStream) Send(e *gilv1.InterviewEvent) error {
	s.events = append(s.events, e)
	return nil
}

func (s *fakeReplyStream) Context() context.Context { return s.ctx }
func (s *fakeReplyStream) SendMsg(m any) error      { return nil }
func (s *fakeReplyStream) RecvMsg(m any) error      { return nil }
func (s *fakeReplyStream) SetHeader(md metadata.MD) error { return nil }
func (s *fakeReplyStream) SendHeader(md metadata.MD) error { return nil }
func (s *fakeReplyStream) SetTrailer(md metadata.MD) {}

func newInterviewSvc(t *testing.T, mockResponses []string) (*InterviewService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMock(mockResponses), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	svc := NewInterviewService(repo, sessionsBase, factory)
	return svc, repo, sessionsBase
}

func TestInterviewService_Start_EmitsStageThenAgentTurn(t *testing.T) {
	// Mock provides 2 responses: sensing JSON, then first question
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"web-saas","domain_confidence":0.9,"tech_hints":["go"],"scale_hint":"medium","ambiguity":"none"}`,
		`What problem are you solving?`,
	})
	ctx := context.Background()

	// Create session first
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	stream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "I want to build a REST API",
		Provider:   "mock",
	}, stream)
	require.NoError(t, err)

	// Expect at least 2 events: stage transition + agent turn
	require.GreaterOrEqual(t, len(stream.events), 2)
	// First should be StageTransition
	first := stream.events[0]
	require.NotNil(t, first.GetStage())
	require.Equal(t, "sensing", first.GetStage().From)
	require.Equal(t, "conversation", first.GetStage().To)
	// Last should be AgentTurn with question
	last := stream.events[len(stream.events)-1]
	require.NotNil(t, last.GetAgentTurn())
	require.Contains(t, last.GetAgentTurn().Content, "problem")

	// Session status updated
	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, "interviewing", got.Status)
}

func TestInterviewService_Start_NotFound_ReturnsError(t *testing.T) {
	svc, _, _ := newInterviewSvc(t, []string{`{"domain":"x","domain_confidence":0.5}`})
	stream := &fakeInterviewStream{ctx: context.Background()}
	err := svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  "nonexistent",
		FirstInput: "x",
		Provider:   "mock",
	}, stream)
	require.Error(t, err)
}

func TestInterviewService_Reply_AppendsAndGeneratesQuestion(t *testing.T) {
	// Start needs: sensing JSON + first question = 2
	// Reply needs: slotfill + next question = 2 (no adversary because slots aren't filled)
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"x","domain_confidence":0.5}`,           // 1 sensing
		`What problem are you solving?`,                     // 2 first question
		`{"updates":[]}`,                                    // 3 slotfill (no extractions)
		`Tell me about your users.`,                         // 4 next question
	})
	ctx := context.Background()

	// Create and start session
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	startStream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "I want to build a REST API",
		Provider:   "mock",
	}, startStream)
	require.NoError(t, err)

	// Send reply
	replyStream := &fakeReplyStream{ctx: ctx}
	err = svc.Reply(&gilv1.ReplyRequest{
		SessionId: s.ID,
		Content:   "users are devs",
	}, replyStream)
	require.NoError(t, err)

	// Should get a question back
	require.Len(t, replyStream.events, 1)
	agentTurn := replyStream.events[0].GetAgentTurn()
	require.NotNil(t, agentTurn)
	require.Equal(t, "Tell me about your users.", agentTurn.Content)
}

func TestInterviewService_Reply_AdvancesToConfirmWhenSaturated(t *testing.T) {
	// Start (2) + Reply (slotfill that fills all slots + adversary [] + audit ready=true = 3) = 5
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"x","domain_confidence":0.5}`,                            // sensing
		`What's your goal?`,                                                  // first question
		`{"updates":[
			{"field":"goal.one_liner","value":"Build CLI"},
			{"field":"goal.success_criteria_natural","value":["a","b","c"]},
			{"field":"constraints.tech_stack","value":["go"]},
			{"field":"verification.checks","value":[{"name":"build","kind":"SHELL","command":"go build","expected_exit_code":0}]},
			{"field":"workspace.backend","value":"LOCAL_SANDBOX"},
			{"field":"models.main","value":{"provider":"anthropic","modelId":"claude-opus-4-7"}},
			{"field":"risk.autonomy","value":"FULL"}
		]}`,                                                                  // slotfill — fills all required
		`[]`,                                                                  // adversary clean
		`{"ready":true,"reason":"all good"}`,                                  // audit pass
	})
	ctx := context.Background()

	// Create and start session
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	startStream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "build",
		Provider:   "mock",
	}, startStream)
	require.NoError(t, err)

	// Send reply that triggers saturation
	replyStream := &fakeReplyStream{ctx: ctx}
	err = svc.Reply(&gilv1.ReplyRequest{
		SessionId: s.ID,
		Content:   "go cli with everything",
	}, replyStream)
	require.NoError(t, err)

	// Should get a StageTransition to confirm
	require.Len(t, replyStream.events, 1)
	stageEvt := replyStream.events[0].GetStage()
	require.NotNil(t, stageEvt)
	require.Equal(t, "conversation", stageEvt.From)
	require.Equal(t, "confirm", stageEvt.To)
}

func TestInterviewService_Reply_NoActiveInterview_ReturnsError(t *testing.T) {
	svc, _, _ := newInterviewSvc(t, []string{})
	replyStream := &fakeReplyStream{ctx: context.Background()}
	err := svc.Reply(&gilv1.ReplyRequest{
		SessionId: "nonexistent",
		Content:   "x",
	}, replyStream)
	require.Error(t, err)
}

func TestInterviewService_GetSpec_ReturnsEmptySpec(t *testing.T) {
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"web-saas","domain_confidence":0.9,"tech_hints":["go"],"scale_hint":"medium","ambiguity":"none"}`,
		`What problem are you solving?`,
	})
	ctx := context.Background()

	// Create and start session
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	startStream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "I want to build a REST API",
		Provider:   "mock",
	}, startStream)
	require.NoError(t, err)

	// GetSpec should return the in-memory state's spec
	spec, err := svc.GetSpec(ctx, &gilv1.GetSpecRequest{SessionId: s.ID})
	require.NoError(t, err)
	require.NotNil(t, spec)
}

func TestInterviewService_GetSpec_NotFound_ReturnsError(t *testing.T) {
	svc, _, _ := newInterviewSvc(t, []string{})
	spec, err := svc.GetSpec(context.Background(), &gilv1.GetSpecRequest{SessionId: "nonexistent"})
	require.Error(t, err)
	require.Nil(t, spec)
}

func TestInterviewService_Confirm_RequiresAllSlots(t *testing.T) {
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"x","domain_confidence":0.5}`,
		`What is your goal?`,
	})
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	stream := &fakeInterviewStream{ctx: ctx}
	require.NoError(t, svc.Start(&gilv1.StartInterviewRequest{
		SessionId: s.ID, FirstInput: "x", Provider: "mock",
	}, stream))

	// Without filling required slots, Confirm should fail
	_, err = svc.Confirm(ctx, &gilv1.ConfirmRequest{SessionId: s.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required slots")
}

func TestInterviewService_Confirm_SavesAndCleansUp(t *testing.T) {
	svc, repo, sessionsBase := newInterviewSvc(t, []string{
		`{"domain":"x","domain_confidence":0.5}`,
		`What is your goal?`,
	})
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	stream := &fakeInterviewStream{ctx: ctx}
	require.NoError(t, svc.Start(&gilv1.StartInterviewRequest{
		SessionId: s.ID, FirstInput: "x", Provider: "mock",
	}, stream))

	// Manually fill required slots in the in-memory state (simulating Reply progress)
	svc.mu.Lock()
	slot := svc.states[s.ID]
	svc.mu.Unlock()
	require.NotNil(t, slot)

	slot.state.Spec.SpecId = "01TEST"
	slot.state.Spec.SessionId = s.ID
	slot.state.Spec.Goal = &gilv1.Goal{
		OneLiner:               "build a CLI",
		SuccessCriteriaNatural: []string{"a", "b", "c"},
	}
	slot.state.Spec.Constraints = &gilv1.Constraints{TechStack: []string{"go"}}
	slot.state.Spec.Verification = &gilv1.Verification{
		Checks: []*gilv1.Check{{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "go build"}},
	}
	slot.state.Spec.Workspace = &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX}
	slot.state.Spec.Models = &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}}
	slot.state.Spec.Risk = &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL}

	resp, err := svc.Confirm(ctx, &gilv1.ConfirmRequest{SessionId: s.ID})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ContentSha256)
	require.Len(t, resp.ContentSha256, 64) // SHA-256 hex

	// Session status updated
	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, "frozen", got.Status)

	// In-memory state cleaned up
	svc.mu.Lock()
	_, exists := svc.states[s.ID]
	svc.mu.Unlock()
	require.False(t, exists, "in-memory state should be deleted after Confirm")

	// spec.yaml + spec.lock exist on disk
	_, err = os.Stat(filepath.Join(sessionsBase, s.ID, "spec.yaml"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(sessionsBase, s.ID, "spec.lock"))
	require.NoError(t, err)
}

// TestChooseModel_Fallback verifies the chooseModel helper: returns override
// when non-empty, fallback when override is empty.
func TestChooseModel_Fallback(t *testing.T) {
	require.Equal(t, "override", chooseModel("override", "fallback"))
	require.Equal(t, "fallback", chooseModel("", "fallback"))
	require.Equal(t, "fallback", chooseModel("", "fallback"))
	require.Equal(t, "", chooseModel("", ""))
}

// newInterviewSvcWithRecorder builds a service where the provider factory
// returns the supplied recordingProvider, allowing callers to inspect which
// model strings were used in each Complete call.
func newInterviewSvcWithRecorder(t *testing.T, rec *recordingProvider) (*InterviewService, *session.Repo) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return rec, "default-model", nil
	}
	svc := NewInterviewService(repo, filepath.Join(dir, "sessions"), factory)
	return svc, repo
}

// TestInterviewService_Start_UsesMainModel verifies that when only Model is
// set (sub-model fields empty), every LLM call uses that model.
func TestInterviewService_Start_UsesMainModel(t *testing.T) {
	rec := &recordingProvider{
		Mock: provider.NewMock([]string{
			`{"domain":"web-saas","domain_confidence":0.9,"tech_hints":[],"scale_hint":"medium","ambiguity":"none"}`,
			`What problem are you solving?`,
		}),
	}
	svc, repo := newInterviewSvcWithRecorder(t, rec)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	stream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "build a CLI",
		Provider:   "mock",
		Model:      "M",
		// SlotModel, AdversaryModel, AuditModel all empty → fall back to "M"
	}, stream)
	require.NoError(t, err)

	calls := rec.modelsUsed()
	require.NotEmpty(t, calls)
	for _, m := range calls {
		require.Equal(t, "M", m, "all calls should use the main model when sub-models are empty")
	}
}

// TestInterviewService_Start_OverrideUsesProvidedModel verifies that when
// SlotModel is set to "SLOT", slot extraction calls use "SLOT" while the
// main model "MAIN" is used for sensing and question generation.
func TestInterviewService_Start_OverrideUsesProvidedModel(t *testing.T) {
	// Start phase: sensing (MAIN) + first question (MAIN) = 2 MAIN calls
	// Reply phase: slotfill (SLOT) + next question (MAIN) = 1 SLOT + 1 MAIN
	rec := &recordingProvider{
		Mock: provider.NewMock([]string{
			`{"domain":"web-saas","domain_confidence":0.9,"tech_hints":[],"scale_hint":"medium","ambiguity":"none"}`,
			`What problem are you solving?`,
			`{"updates":[]}`, // slotfill no-op
			`Tell me more.`,  // next question
		}),
	}
	svc, repo := newInterviewSvcWithRecorder(t, rec)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: "/x"})
	require.NoError(t, err)

	startStream := &fakeInterviewStream{ctx: ctx}
	err = svc.Start(&gilv1.StartInterviewRequest{
		SessionId:  s.ID,
		FirstInput: "build a CLI",
		Provider:   "mock",
		Model:      "MAIN",
		SlotModel:  "SLOT",
	}, startStream)
	require.NoError(t, err)

	// Start phase: verify all calls used "MAIN"
	afterStart := rec.modelsUsed()
	for _, m := range afterStart {
		require.Equal(t, "MAIN", m, "start phase should only call MAIN (sensing + first question)")
	}

	// Reply phase: triggers slotfill with "SLOT"
	replyStream := &fakeReplyStream{ctx: ctx}
	err = svc.Reply(&gilv1.ReplyRequest{SessionId: s.ID, Content: "it's a tool"}, replyStream)
	require.NoError(t, err)

	afterReply := rec.modelsUsed()
	// Must contain at least one "SLOT" call from slotfill
	var foundSlot bool
	for _, m := range afterReply[len(afterStart):] {
		if m == "SLOT" {
			foundSlot = true
		}
	}
	require.True(t, foundSlot, "reply phase must use SLOT model for slot extraction")
}
