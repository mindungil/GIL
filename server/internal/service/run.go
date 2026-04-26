package service

import (
	"context"
	"errors"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
}

// NewRunService constructs the service.
func NewRunService(repo *session.Repo, sessionsBase string, factory ProviderFactory) *RunService {
	return &RunService{repo: repo, sessionsBase: sessionsBase, providerFactory: factory}
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

	loop := runner.NewAgentLoop(spec, prov, model, tools, ver)
	res, runErr := loop.Run(ctx)

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

// Tail is a Phase 5 stub.
func (s *RunService) Tail(req *gilv1.TailRequest, stream gilv1.RunService_TailServer) error {
	return status.Errorf(codes.Unimplemented, "Tail is implemented in Phase 5")
}
