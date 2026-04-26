package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/runner"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/specstore"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// RunService handles RunService gRPC. Loads frozen spec, builds tools/verifier,
// runs AgentLoop synchronously. Tail is a Phase 5 stub.
type RunService struct {
	gilv1.UnimplementedRunServiceServer

	repo            *session.Repo
	sessionsBase    string
	providerFactory ProviderFactory

	mu         sync.Mutex
	runStreams map[string]*event.Stream  // per-session live event streams
}

// NewRunService constructs the service.
func NewRunService(repo *session.Repo, sessionsBase string, factory ProviderFactory) *RunService {
	return &RunService{
		repo:            repo,
		sessionsBase:    sessionsBase,
		providerFactory: factory,
		runStreams:      make(map[string]*event.Stream),
	}
}

func (s *RunService) sessionDir(sessionID string) string {
	return filepath.Join(s.sessionsBase, sessionID)
}

// Start runs the agent loop synchronously and returns the result.
func (s *RunService) Start(ctx context.Context, req *gilv1.StartRunRequest) (*gilv1.StartRunResponse, error) {
	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "session lookup: %v", err)
	}
	if sess.Status != "frozen" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %q must be frozen before run (current status: %s)", req.SessionId, sess.Status)
	}

	store := specstore.NewStore(s.sessionDir(req.SessionId))
	spec, err := store.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load spec: %v", err)
	}

	prov, model, err := s.providerFactory(req.Provider)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "provider: %v", err)
	}
	if req.Model != "" {
		model = req.Model
	}
	prov = provider.NewRetry(prov)

	workspaceDir := sess.WorkingDir
	if spec.Workspace != nil && spec.Workspace.Path != "" {
		workspaceDir = spec.Workspace.Path
	}
	tools := []tool.Tool{
		&tool.Bash{WorkingDir: workspaceDir},
		&tool.WriteFile{WorkingDir: workspaceDir},
		&tool.ReadFile{WorkingDir: workspaceDir},
	}
	ver := verify.NewRunner(workspaceDir)

	if err := s.repo.UpdateStatus(ctx, req.SessionId, "running"); err != nil {
		return nil, status.Errorf(codes.Internal, "update status: %v", err)
	}

	// Create per-session event stream + persister
	eventDir := filepath.Join(s.sessionDir(req.SessionId), "events")
	persister, err := event.NewPersister(eventDir)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create persister: %v", err)
	}
	defer persister.Close()

	stream := event.NewStream()

	// Register stream so Tail can subscribe
	s.mu.Lock()
	s.runStreams[req.SessionId] = stream
	s.mu.Unlock()

	// Cleanup on exit
	defer func() {
		s.mu.Lock()
		delete(s.runStreams, req.SessionId)
		s.mu.Unlock()
	}()

	// Persistence subscriber: write every event to disk
	persistSub := stream.Subscribe(256)
	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		for evt := range persistSub.Events() {
			_ = persister.Write(evt)
		}
	}()

	loop := runner.NewAgentLoop(spec, prov, model, tools, ver)
	loop.Events = stream

	res, runErr := loop.Run(ctx)

	// Allow persister sub to drain
	persistSub.Close()
	<-persistDone
	_ = persister.Sync()

	finalStatus := "stopped"
	if res != nil && res.Status == "done" {
		finalStatus = "done"
	}
	_ = s.repo.UpdateStatus(ctx, req.SessionId, finalStatus)

	if runErr != nil && res == nil {
		return nil, status.Errorf(codes.Internal, "run: %v", runErr)
	}

	resp := &gilv1.StartRunResponse{
		Status:     res.Status,
		Iterations: int32(res.Iterations),
		Tokens:     res.Tokens,
	}
	for _, vr := range res.VerifyAll {
		resp.VerifyResults = append(resp.VerifyResults, &gilv1.VerifyResult{
			Name: vr.Name, Passed: vr.Passed, ExitCode: int32(vr.ExitCode),
			Stdout: vr.Stdout, Stderr: vr.Stderr,
		})
	}
	if res.FinalError != nil {
		resp.ErrorMessage = res.FinalError.Error()
	}
	return resp, nil
}

// toProtoEvent converts a core event.Event to its proto representation.
func toProtoEvent(e event.Event) *gilv1.Event {
	return &gilv1.Event{
		Id:        e.ID,
		Timestamp: timestamppb.New(e.Timestamp),
		Source:    eventSourceToProto(e.Source),
		Kind:      eventKindToProto(e.Kind),
		Type:      e.Type,
		DataJson:  e.Data,
		Cause:     e.Cause,
		Metrics: &gilv1.EventMetrics{
			Tokens:    e.Metrics.Tokens,
			CostUsd:   e.Metrics.CostUSD,
			LatencyMs: e.Metrics.LatencyMs,
		},
	}
}

func eventSourceToProto(s event.Source) gilv1.EventSource {
	switch s {
	case event.SourceAgent:
		return gilv1.EventSource_AGENT
	case event.SourceUser:
		return gilv1.EventSource_USER
	case event.SourceEnvironment:
		return gilv1.EventSource_ENVIRONMENT
	case event.SourceSystem:
		return gilv1.EventSource_SYSTEM
	default:
		return gilv1.EventSource_SOURCE_UNSPECIFIED
	}
}

func eventKindToProto(k event.Kind) gilv1.EventKind {
	switch k {
	case event.KindAction:
		return gilv1.EventKind_ACTION
	case event.KindObservation:
		return gilv1.EventKind_OBSERVATION
	case event.KindNote:
		return gilv1.EventKind_NOTE
	default:
		return gilv1.EventKind_KIND_UNSPECIFIED
	}
}

// Tail subscribes to the per-session live event stream and forwards each
// event to the gRPC client. Returns NotFound if no run is active for the
// session. (Replay from disk is Phase 6.)
func (s *RunService) Tail(req *gilv1.TailRequest, stream gilv1.RunService_TailServer) error {
	s.mu.Lock()
	rs, ok := s.runStreams[req.SessionId]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound,
			"no active run for session %q (replay from disk is Phase 6)", req.SessionId)
	}

	sub := rs.Subscribe(256)
	defer sub.Close()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub.Events():
			if !ok {
				return nil
			}
			if err := stream.Send(toProtoEvent(e)); err != nil {
				return err
			}
		}
	}
}
