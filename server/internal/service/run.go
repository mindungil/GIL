package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/runner"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/specstore"
	"github.com/jedutools/gil/core/tool"
	"github.com/jedutools/gil/core/verify"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/runtime/local"
)

// runProgressSnap holds live iteration/token counters for an active run.
type runProgressSnap struct {
	iters  int32
	tokens int64
}

// RunService handles RunService gRPC. Loads frozen spec, builds tools/verifier,
// runs AgentLoop synchronously or in background (detach mode). Tail subscribes
// to the live event stream.
type RunService struct {
	gilv1.UnimplementedRunServiceServer

	repo            *session.Repo
	sessionsBase    string
	providerFactory ProviderFactory

	mu          sync.Mutex
	runStreams  map[string]*event.Stream    // per-session live event streams
	runProgress map[string]*runProgressSnap // per-session live progress counters
}

// NewRunService constructs the service.
func NewRunService(repo *session.Repo, sessionsBase string, factory ProviderFactory) *RunService {
	return &RunService{
		repo:            repo,
		sessionsBase:    sessionsBase,
		providerFactory: factory,
		runStreams:      make(map[string]*event.Stream),
		runProgress:     make(map[string]*runProgressSnap),
	}
}

// Progress returns a live snapshot of iteration and token counts for the given
// session. Returns ok=false when no run is active for that session.
func (s *RunService) Progress(sessionID string) (iters int32, tokens int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.runProgress[sessionID]
	if !ok {
		return 0, 0, false
	}
	return p.iters, p.tokens, true
}

func (s *RunService) sessionDir(sessionID string) string {
	return filepath.Join(s.sessionsBase, sessionID)
}

// buildTools returns the tool set for a run, configured per spec.Workspace.Backend.
// Returns (tools, error). Unsupported backends produce errors so RunService.Start
// can refuse the run rather than silently degrading.
func buildTools(workspaceDir string, ws *gilv1.Workspace) ([]tool.Tool, error) {
	backend := gilv1.WorkspaceBackend_LOCAL_NATIVE
	if ws != nil && ws.Backend != gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED {
		backend = ws.Backend
	}
	switch backend {
	case gilv1.WorkspaceBackend_LOCAL_NATIVE:
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
		}, nil
	case gilv1.WorkspaceBackend_LOCAL_SANDBOX:
		if !local.Available() {
			return nil, fmt.Errorf("workspace backend LOCAL_SANDBOX requires bwrap, but it is not installed")
		}
		sb := &local.Sandbox{
			WorkspaceDir: workspaceDir,
			Mode:         local.ModeWorkspaceWrite,
		}
		return []tool.Tool{
			&tool.Bash{WorkingDir: workspaceDir, Wrapper: sb},
			&tool.WriteFile{WorkingDir: workspaceDir},
			&tool.ReadFile{WorkingDir: workspaceDir},
		}, nil
	case gilv1.WorkspaceBackend_DOCKER, gilv1.WorkspaceBackend_SSH, gilv1.WorkspaceBackend_VM:
		return nil, fmt.Errorf("workspace backend %s not yet supported (Phase 6)", backend.String())
	default:
		return nil, fmt.Errorf("unknown workspace backend: %v", backend)
	}
}

// Start runs the agent loop and returns the result. When req.Detach is true,
// the loop runs in a goroutine and the method returns immediately with
// Status="started".
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
	tools, err := buildTools(workspaceDir, spec.Workspace)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "workspace backend: %v", err)
	}
	ver := verify.NewRunner(workspaceDir)

	// Mark running BEFORE spawning goroutine so the client sees consistent state.
	if err := s.repo.UpdateStatus(ctx, req.SessionId, "running"); err != nil {
		return nil, status.Errorf(codes.Internal, "update status: %v", err)
	}

	if req.Detach {
		go func() {
			// Use a background context: the gRPC ctx cancels when Start returns.
			bgCtx := context.Background()
			_, _ = s.executeRun(bgCtx, req.SessionId, spec, prov, model, tools, ver, workspaceDir)
		}()
		return &gilv1.StartRunResponse{Status: "started"}, nil
	}
	return s.executeRun(ctx, req.SessionId, spec, prov, model, tools, ver, workspaceDir)
}

// executeRun performs the actual agent loop execution and cleanup. It is called
// either directly (synchronous path) or from a detached goroutine.
func (s *RunService) executeRun(
	ctx context.Context,
	sessionID string,
	spec *gilv1.FrozenSpec,
	prov provider.Provider,
	model string,
	tools []tool.Tool,
	ver *verify.Runner,
	workspaceDir string,
) (*gilv1.StartRunResponse, error) {
	// Create per-session event stream + persister.
	eventDir := filepath.Join(s.sessionDir(sessionID), "events")
	persister, err := event.NewPersister(eventDir)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create persister: %v", err)
	}
	defer persister.Close()

	stream := event.NewStream()

	// Register stream and progress snap under the lock.
	s.mu.Lock()
	s.runStreams[sessionID] = stream
	s.runProgress[sessionID] = &runProgressSnap{}
	s.mu.Unlock()

	// Cleanup on exit: remove both stream and progress entry.
	defer func() {
		s.mu.Lock()
		delete(s.runStreams, sessionID)
		delete(s.runProgress, sessionID)
		s.mu.Unlock()
	}()

	// Persistence subscriber: write every event to disk.
	persistSub := stream.Subscribe(256)
	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		for evt := range persistSub.Events() {
			_ = persister.Write(evt)
		}
	}()

	// Progress subscriber: track iterations and accumulated tokens.
	progSub := stream.Subscribe(256)
	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		for evt := range progSub.Events() {
			s.mu.Lock()
			snap := s.runProgress[sessionID]
			if snap != nil {
				if evt.Type == "iteration_start" {
					snap.iters++
				}
				if evt.Metrics.Tokens > 0 {
					snap.tokens += evt.Metrics.Tokens
				}
			}
			s.mu.Unlock()
		}
	}()

	loop := runner.NewAgentLoop(spec, prov, model, tools, ver)
	loop.Events = stream

	shadowBase := filepath.Join(s.sessionDir(sessionID), "shadow")
	loop.Checkpoint = checkpoint.New(workspaceDir, shadowBase)

	res, runErr := loop.Run(ctx)

	// Drain both subscribers before syncing to disk (order-independent).
	persistSub.Close()
	<-persistDone
	progSub.Close()
	<-progDone

	_ = persister.Sync()

	finalStatus := "stopped"
	if res != nil && res.Status == "done" {
		finalStatus = "done"
	}
	_ = s.repo.UpdateStatus(ctx, sessionID, finalStatus)

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

// Restore rolls back the session's workspace to the given checkpoint step.
// Positive step counts oldest-first (step=1 → oldest); negative counts
// newest-first (step=-1 → most recent). step=0 is invalid.
func (s *RunService) Restore(ctx context.Context, req *gilv1.RestoreRequest) (*gilv1.RestoreResponse, error) {
	if req.Step == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "step must be non-zero (1-indexed; negatives count from latest)")
	}
	sess, err := s.repo.Get(ctx, req.SessionId)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
		}
		return nil, status.Errorf(codes.Internal, "session lookup: %v", err)
	}
	// Refuse restore on running sessions to avoid concurrent workspace mutation.
	if sess.Status == "running" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"cannot restore session %q while running; stop it first", req.SessionId)
	}
	workspaceDir := sess.WorkingDir
	spec, err := specstore.NewStore(s.sessionDir(req.SessionId)).Load()
	if err == nil && spec.Workspace != nil && spec.Workspace.Path != "" {
		workspaceDir = spec.Workspace.Path
	}
	shadowBase := filepath.Join(s.sessionDir(req.SessionId), "shadow")
	sg := checkpoint.New(workspaceDir, shadowBase)
	commits, err := sg.ListCommits(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list checkpoints: %v", err)
	}
	if len(commits) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %q has no checkpoints", req.SessionId)
	}
	// commits is newest-first. Resolve step:
	//   step  1 → oldest (commits[len-1])
	//   step  N → commits[len-N]
	//   step -1 → newest (commits[0])
	//   step -N → commits[N-1]
	var idx int
	if req.Step > 0 {
		idx = len(commits) - int(req.Step)
	} else {
		idx = int(-req.Step) - 1
	}
	if idx < 0 || idx >= len(commits) {
		return nil, status.Errorf(codes.OutOfRange,
			"step %d out of range (have %d checkpoints)", req.Step, len(commits))
	}
	target := commits[idx]
	if err := sg.Restore(ctx, target.SHA); err != nil {
		return nil, status.Errorf(codes.Internal, "restore: %v", err)
	}
	return &gilv1.RestoreResponse{
		CommitSha:        target.SHA,
		CommitMessage:    target.Message,
		TotalCheckpoints: int32(len(commits)),
	}, nil
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
