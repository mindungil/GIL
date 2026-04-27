package service

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mindungil/gil/core/session"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// ProgressGetter exposes live run progress so SessionService can enrich
// RUNNING session responses without depending on the full RunService.
type ProgressGetter interface {
	Progress(sessionID string) (iters int32, tokens int64, ok bool)
}

// SessionService implements the gRPC SessionService server-side handler.
type SessionService struct {
	gilv1.UnimplementedSessionServiceServer
	repo     *session.Repo
	progress ProgressGetter // may be nil for tests/standalone
}

// NewSessionService returns a new SessionService backed by the provided Repo.
// progress may be nil; when non-nil, RUNNING sessions will have live
// iteration/token counts populated in responses.
func NewSessionService(repo *session.Repo, progress ProgressGetter) *SessionService {
	return &SessionService{repo: repo, progress: progress}
}

// Create creates a new session with the given working directory and goal hint.
func (s *SessionService) Create(ctx context.Context, req *gilv1.CreateRequest) (*gilv1.Session, error) {
	created, err := s.repo.Create(ctx, session.CreateInput{
		WorkingDir: req.WorkingDir,
		GoalHint:   req.GoalHint,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session.Create: %v", err)
	}
	return s.toProto(created), nil
}

// Get retrieves a session by its ID.
func (s *SessionService) Get(ctx context.Context, req *gilv1.GetRequest) (*gilv1.Session, error) {
	got, err := s.repo.Get(ctx, req.Id)
	if errors.Is(err, session.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.Id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session.Get: %v", err)
	}
	return s.toProto(got), nil
}

// List returns a list of sessions, optionally filtered by status.
func (s *SessionService) List(ctx context.Context, req *gilv1.ListRequest) (*gilv1.ListResponse, error) {
	limit := int(req.Limit)
	got, err := s.repo.List(ctx, session.ListOptions{Limit: limit, StatusFilter: req.StatusFilter})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session.List: %v", err)
	}
	out := make([]*gilv1.Session, 0, len(got))
	for _, sess := range got {
		out = append(out, s.toProto(sess))
	}
	return &gilv1.ListResponse{Sessions: out}, nil
}

// toProto converts a core Session to a proto Session. When the session status
// is "running" and a ProgressGetter is wired in, it enriches the response with
// live iteration and token counts.
func (s *SessionService) toProto(sess session.Session) *gilv1.Session {
	p := &gilv1.Session{
		Id:           sess.ID,
		Status:       statusToProto(sess.Status),
		CreatedAt:    timestamppb.New(sess.CreatedAt),
		UpdatedAt:    timestamppb.New(sess.UpdatedAt),
		SpecId:       sess.SpecID,
		WorkingDir:   sess.WorkingDir,
		GoalHint:     sess.GoalHint,
		TotalTokens:  sess.TotalTokens,
		TotalCostUsd: sess.TotalCostUSD,
	}
	if sess.Status == "running" && s.progress != nil {
		if iters, tokens, ok := s.progress.Progress(sess.ID); ok {
			p.CurrentIteration = iters
			p.CurrentTokens = tokens
		}
	}
	return p
}

// statusToProto maps a session status string to the corresponding proto enum.
// String values must align with constants used in core/session
// (currently statusCreated="created"; other states are managed by future
// status-transition methods). Unknown values map to UNSPECIFIED.
func statusToProto(s string) gilv1.SessionStatus {
	switch s {
	case "created":
		return gilv1.SessionStatus_CREATED
	case "interviewing":
		return gilv1.SessionStatus_INTERVIEWING
	case "frozen":
		return gilv1.SessionStatus_FROZEN
	case "running":
		return gilv1.SessionStatus_RUNNING
	case "auto_paused":
		return gilv1.SessionStatus_AUTO_PAUSED
	case "done":
		return gilv1.SessionStatus_DONE
	case "stopped":
		return gilv1.SessionStatus_STOPPED
	default:
		return gilv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}
