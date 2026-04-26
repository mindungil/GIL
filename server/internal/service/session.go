package service

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// SessionService implements the gRPC SessionService server-side handler.
type SessionService struct {
	gilv1.UnimplementedSessionServiceServer
	repo *session.Repo
}

// NewSessionService returns a new SessionService backed by the provided Repo.
func NewSessionService(repo *session.Repo) *SessionService {
	return &SessionService{repo: repo}
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
	return toProto(created), nil
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
	return toProto(got), nil
}

// List returns a list of sessions, optionally filtered by status.
func (s *SessionService) List(ctx context.Context, req *gilv1.ListRequest) (*gilv1.ListResponse, error) {
	limit := int(req.Limit)
	got, err := s.repo.List(ctx, session.ListOptions{Limit: limit, StatusFilter: req.StatusFilter})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "session.List: %v", err)
	}
	out := make([]*gilv1.Session, 0, len(got))
	for _, s := range got {
		out = append(out, toProto(s))
	}
	return &gilv1.ListResponse{Sessions: out}, nil
}

// toProto converts a core Session to a proto Session.
func toProto(s session.Session) *gilv1.Session {
	return &gilv1.Session{
		Id:           s.ID,
		Status:       statusToProto(s.Status),
		CreatedAt:    timestamppb.New(s.CreatedAt),
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
		SpecId:       s.SpecID,
		WorkingDir:   s.WorkingDir,
		GoalHint:     s.GoalHint,
		TotalTokens:  s.TotalTokens,
		TotalCostUsd: s.TotalCostUSD,
	}
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
