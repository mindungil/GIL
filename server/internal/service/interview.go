package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/mindungil/gil/core/interview"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/spec"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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
	state      *interview.State
	engine     *interview.Engine
	richEngine *interview.EngineWithSubmodels
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

	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return status.Errorf(codes.Internal, "session lookup: %v", err)
	}

	// Resume path: empty first_input + session already in 'interviewing' status + in-memory state exists
	if req.FirstInput == "" && sess.Status == "interviewing" {
		s.mu.Lock()
		slot, ok := s.states[req.SessionId]
		s.mu.Unlock()
		if !ok {
			return status.Errorf(codes.FailedPrecondition,
				"session %q has interviewing status but no in-memory state (cross-restart resume not yet supported)",
				req.SessionId)
		}
		// Emit a stage marker indicating resume + last assistant message if any
		if err := stream.Send(&gilv1.InterviewEvent{
			Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{
				From: "resume", To: slot.state.Stage.String(), Reason: "resumed in-progress interview",
			}},
		}); err != nil {
			return err
		}
		// Find the last assistant turn and re-emit it
		for i := len(slot.state.History) - 1; i >= 0; i-- {
			if slot.state.History[i].Role == provider.RoleAssistant {
				return stream.Send(&gilv1.InterviewEvent{
					Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: slot.state.History[i].Content}},
				})
			}
		}
		return nil
	}

	// Normal start path
	// Mark session as interviewing
	if err := s.repo.UpdateStatus(ctx, req.SessionId, "interviewing"); err != nil {
		return status.Errorf(codes.Internal, "update status: %v", err)
	}

	// Build provider + engines with per-stage model selection.
	// Each sub-engine uses its dedicated model, falling back to mainModel when empty.
	prov, defaultModel, err := s.providerFactory(req.Provider)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "provider: %v", err)
	}
	mainModel := chooseModel(req.Model, defaultModel)
	slotModel := chooseModel(req.SlotModel, mainModel)
	adversaryModel := chooseModel(req.AdversaryModel, mainModel)
	auditModel := chooseModel(req.AuditModel, mainModel)

	eng := interview.NewEngine(prov, mainModel)
	richEng := interview.NewEngineWithSubmodels(prov, mainModel, slotModel, adversaryModel, auditModel)
	st := interview.NewState()

	s.mu.Lock()
	s.states[req.SessionId] = &interviewSlot{state: st, engine: eng, richEngine: richEng}
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

// Reply handles the Reply RPC: runs richEngine.RunReplyTurn which orchestrates
// slot filling, adversary, audit, and either emits a StageTransition (to confirm)
// or AgentTurn (next question).
//
// After the turn completes, up to three additional events are emitted before
// the main outcome event:
//   - SpecUpdate — once per newly-filled required slot (SlotChangeCount > 0)
//   - SaturationUpdate — always, reflecting the current fill ratio
//   - AdversaryFindings — only when the adversary ran this turn
func (s *InterviewService) Reply(req *gilv1.ReplyRequest, stream gilv1.InterviewService_ReplyServer) error {
	ctx := stream.Context()

	s.mu.Lock()
	slot, ok := s.states[req.SessionId]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.FailedPrecondition, "no active interview for session %q", req.SessionId)
	}

	turn, err := slot.richEngine.RunReplyTurn(ctx, slot.state, req.Content)
	if err != nil {
		return status.Errorf(codes.Internal, "reply turn: %v", err)
	}

	// --- Emit SpecUpdate for each newly-filled required slot this turn ---
	if turn.SlotChangeCount > 0 {
		// We emit one SpecUpdate per newly-filled slot count with a synthetic
		// field_path so the chat surface knows the spec changed this turn.
		// The count is the number of required slots that transitioned to filled.
		for i := 0; i < turn.SlotChangeCount; i++ {
			if err := stream.Send(&gilv1.InterviewEvent{
				Payload: &gilv1.InterviewEvent_SpecUpdate{SpecUpdate: &gilv1.SpecUpdate{
					FieldPath: "required_slot",
					NewValue:  fmt.Sprintf("filled (%d of %d)", turn.SaturationFilled, turn.SaturationTotal),
				}},
			}); err != nil {
				return err
			}
		}
	}

	// --- Always emit SaturationUpdate ---
	var saturation float64
	if turn.SaturationTotal > 0 {
		saturation = float64(turn.SaturationFilled) / float64(turn.SaturationTotal)
	}
	if err := stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_SaturationUpdate{SaturationUpdate: &gilv1.SaturationUpdate{
			Saturation:   saturation,
			SlotsFilled:  int32(turn.SaturationFilled),
			SlotsTotal:   int32(turn.SaturationTotal),
		}},
	}); err != nil {
		return err
	}

	// --- Emit AdversaryFindings if adversary ran this turn ---
	if turn.AdversaryFindings != nil {
		if err := stream.Send(&gilv1.InterviewEvent{
			Payload: &gilv1.InterviewEvent_AdversaryFindings{AdversaryFindings: &gilv1.AdversaryFindings{
				Count:           int32(len(turn.AdversaryFindings)),
				HighestSeverity: highestSeverity(turn.AdversaryFindings),
			}},
		}); err != nil {
			return err
		}
	}

	// --- Main outcome event ---
	switch turn.Outcome {
	case interview.ReplyOutcomeReadyToConfirm:
		return stream.Send(&gilv1.InterviewEvent{
			Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{
				From: "conversation", To: "confirm", Reason: turn.Content,
			}},
		})
	case interview.ReplyOutcomeNextQuestion:
		return stream.Send(&gilv1.InterviewEvent{
			Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{Content: turn.Content}},
		})
	default:
		return status.Errorf(codes.Internal, "unknown reply outcome %d", turn.Outcome)
	}
}

// highestSeverity returns the most severe level found in findings.
// Severity order: blocker > high > medium > low > "".
func highestSeverity(findings []interview.Finding) string {
	rank := map[string]int{"blocker": 4, "high": 3, "medium": 2, "low": 1}
	best := ""
	bestRank := 0
	for _, f := range findings {
		if r := rank[f.Severity]; r > bestRank {
			bestRank = r
			best = f.Severity
		}
	}
	return best
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

	// Clean up in-memory state — spec is now persisted on disk
	s.mu.Lock()
	delete(s.states, req.SessionId)
	s.mu.Unlock()

	return &gilv1.ConfirmResponse{
		SpecId:        slot.state.Spec.SpecId,
		ContentSha256: hex,
	}, nil
}

// chooseModel returns override when non-empty, otherwise fallback.
func chooseModel(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// GetSpec returns the current (possibly partial) spec.
func (s *InterviewService) GetSpec(ctx context.Context, req *gilv1.GetSpecRequest) (*gilv1.FrozenSpec, error) {
	// Try in-memory state first
	s.mu.Lock()
	slot, ok := s.states[req.SessionId]
	s.mu.Unlock()
	if ok {
		// Return defensive copy — slot.state.Spec is mutated by Reply
		return proto.Clone(slot.state.Spec).(*gilv1.FrozenSpec), nil
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
