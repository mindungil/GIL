package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jedutools/gil/core/interview"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/spec"
	"github.com/jedutools/gil/core/specstore"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// ProviderFactory returns a Provider + default model name for the given provider name.
type ProviderFactory func(name string) (provider.Provider, string, error)

// InterviewService manages per-session interview state and persists frozen specs.
type InterviewService struct {
	gilv1.UnimplementedInterviewServiceServer

	repo            *session.Repo
	sessionsBase    string
	providerFactory ProviderFactory

	mu     sync.Mutex
	states map[string]*interviewSlot
}

type interviewSlot struct {
	state  *interview.State
	engine *interview.Engine
}

// NewInterviewService constructs the service. sessionsBase is the directory
// under which per-session subdirectories will be created (e.g.,
// "~/.gil/sessions"). factory turns a provider name into a concrete Provider.
func NewInterviewService(repo *session.Repo, sessionsBase string, factory ProviderFactory) *InterviewService {
	return &InterviewService{
		repo:            repo,
		sessionsBase:    sessionsBase,
		providerFactory: factory,
		states:          make(map[string]*interviewSlot),
	}
}

func (s *InterviewService) sessionDir(sessionID string) string {
	return filepath.Join(s.sessionsBase, sessionID)
}

// Start handles the Start RPC: validates session, runs Sensing, emits stage
// transition + first question.
func (s *InterviewService) Start(req *gilv1.StartInterviewRequest, stream gilv1.InterviewService_StartServer) error {
	ctx := stream.Context()

	// Validate session
	if _, err := s.repo.Get(ctx, req.SessionId); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return status.Errorf(codes.Internal, "session lookup: %v", err)
	}

	// Mark session as interviewing
	if err := s.repo.UpdateStatus(ctx, req.SessionId, "interviewing"); err != nil {
		return status.Errorf(codes.Internal, "update status: %v", err)
	}

	// Build provider + engine
	prov, model, err := s.providerFactory(req.Provider)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "provider: %v", err)
	}
	if req.Model != "" {
		model = req.Model
	}
	eng := interview.NewEngine(prov, model)
	st := interview.NewState()

	s.mu.Lock()
	s.states[req.SessionId] = &interviewSlot{state: st, engine: eng}
	s.mu.Unlock()

	// Run Sensing
	if err := eng.RunSensing(ctx, st, req.FirstInput); err != nil {
		return status.Errorf(codes.Internal, "sensing: %v", err)
	}

	// Emit stage transition
	if err := stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{
			From: "sensing", To: "conversation", Reason: fmt.Sprintf("domain=%s confidence=%.2f", st.Domain, st.DomainConfidence),
		}},
	}); err != nil {
		return err
	}

	// Generate first question
	q, err := eng.NextQuestion(ctx, st)
	if err != nil {
		return status.Errorf(codes.Internal, "first question: %v", err)
	}
	st.AppendAssistant(q)

	return stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: q}},
	})
}

// Reply handles the Reply RPC: appends user content, generates next question.
func (s *InterviewService) Reply(req *gilv1.ReplyRequest, stream gilv1.InterviewService_ReplyServer) error {
	ctx := stream.Context()

	s.mu.Lock()
	slot, ok := s.states[req.SessionId]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.FailedPrecondition, "no active interview for session %q", req.SessionId)
	}

	slot.state.AppendUser(req.Content)

	q, err := slot.engine.NextQuestion(ctx, slot.state)
	if err != nil {
		return status.Errorf(codes.Internal, "next question: %v", err)
	}
	slot.state.AppendAssistant(q)

	return stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: q}},
	})
}

// Confirm finalizes the spec: requires all required slots filled, calls
// spec.Freeze, and persists via specstore. Updates session status to frozen.
func (s *InterviewService) Confirm(ctx context.Context, req *gilv1.ConfirmRequest) (*gilv1.ConfirmResponse, error) {
	s.mu.Lock()
	slot, ok := s.states[req.SessionId]
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "no active interview for session %q", req.SessionId)
	}

	if !slot.state.AllRequiredSlotsFilled() {
		return nil, status.Errorf(codes.FailedPrecondition, "spec missing required slots")
	}

	store := specstore.NewStore(s.sessionDir(req.SessionId))
	if err := store.Save(slot.state.Spec); err != nil {
		return nil, status.Errorf(codes.Internal, "save spec: %v", err)
	}
	if err := store.Freeze(); err != nil {
		return nil, status.Errorf(codes.Internal, "freeze spec: %v", err)
	}
	hex, _ := spec.Freeze(slot.state.Spec)

	if err := s.repo.UpdateStatus(ctx, req.SessionId, "frozen"); err != nil {
		return nil, status.Errorf(codes.Internal, "update status: %v", err)
	}
	return &gilv1.ConfirmResponse{
		SpecId:        slot.state.Spec.SpecId,
		ContentSha256: hex,
	}, nil
}

// GetSpec returns the current (possibly partial) spec.
func (s *InterviewService) GetSpec(ctx context.Context, req *gilv1.GetSpecRequest) (*gilv1.FrozenSpec, error) {
	// Try in-memory state first
	s.mu.Lock()
	slot, ok := s.states[req.SessionId]
	s.mu.Unlock()
	if ok {
		return slot.state.Spec, nil
	}
	// Fallback to disk
	store := specstore.NewStore(s.sessionDir(req.SessionId))
	fs, err := store.Load()
	if err != nil {
		if errors.Is(err, specstore.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "no spec for session %q", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "load spec: %v", err)
	}
	return fs, nil
}
