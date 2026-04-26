package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

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
	svc, repo, _ := newInterviewSvc(t, []string{
		`{"domain":"web-saas","domain_confidence":0.9,"tech_hints":["go"],"scale_hint":"medium","ambiguity":"none"}`,
		`What problem are you solving?`,
		`How many users?`,
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
		Content:   "It's for a SaaS product",
	}, replyStream)
	require.NoError(t, err)

	// Should get a question back
	require.Len(t, replyStream.events, 1)
	evt := replyStream.events[0]
	require.NotNil(t, evt.GetAgentTurn())
	require.Contains(t, evt.GetAgentTurn().Content, "users")
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
