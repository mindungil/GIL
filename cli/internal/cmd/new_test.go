package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func startGildForTest(t *testing.T) (sock string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	sock = filepath.Join(dir, "gild.sock")

	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	lis, err := net.Listen("unix", sock)
	require.NoError(t, err)

	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, &testSessionServer{})
	gilv1.RegisterInterviewServiceServer(g, &testInterviewServer{})
	gilv1.RegisterRunServiceServer(g, &testRunServer{})
	go g.Serve(lis)

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, time.Second, 20*time.Millisecond)

	return sock, func() {
		g.GracefulStop()
		_ = lis.Close()
	}
}

// testSessionServer is a minimal in-test stub of SessionService.
// It maintains state across Create and List calls.
type testSessionServer struct {
	gilv1.UnimplementedSessionServiceServer
	mu       sync.Mutex
	sessions []*gilv1.Session
}

func (s *testSessionServer) Create(ctx context.Context, req *gilv1.CreateRequest) (*gilv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := &gilv1.Session{
		Id:         fmt.Sprintf("01TESTSESSIONIDXXXXXXXXX%02d", len(s.sessions)+1),
		Status:     gilv1.SessionStatus_CREATED,
		WorkingDir: req.WorkingDir,
		GoalHint:   req.GoalHint,
	}
	s.sessions = append(s.sessions, sess)
	return sess, nil
}

func (s *testSessionServer) List(ctx context.Context, req *gilv1.ListRequest) (*gilv1.ListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &gilv1.ListResponse{Sessions: append([]*gilv1.Session(nil), s.sessions...)}, nil
}

// Get / Delete are needed by the session subcommand tests
// (cli/internal/cmd/session_test.go). They live on the same stub so the
// shared startGildForTest harness keeps one in-process server.
func (s *testSessionServer) Get(ctx context.Context, req *gilv1.GetRequest) (*gilv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.Id == req.Id {
			return sess, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "session %q not found", req.Id)
}

func (s *testSessionServer) Delete(ctx context.Context, req *gilv1.DeleteRequest) (*gilv1.DeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sess := range s.sessions {
		if sess.Id == req.Id {
			if sess.Status == gilv1.SessionStatus_RUNNING {
				return nil, status.Errorf(codes.FailedPrecondition,
					"session %q is currently running; stop the run first", req.Id)
			}
			s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
			return &gilv1.DeleteResponse{FreedBytes: 1024}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "session %q not found", req.Id)
}

// testInterviewServer is a minimal in-test stub of InterviewService.
type testInterviewServer struct {
	gilv1.UnimplementedInterviewServiceServer
}

// testRunServer is a minimal in-test stub of RunService.
type testRunServer struct {
	gilv1.UnimplementedRunServiceServer
}

func (s *testInterviewServer) Start(req *gilv1.StartInterviewRequest, stream gilv1.InterviewService_StartServer) error {
	stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_Stage{
			Stage: &gilv1.StageTransition{
				From:   "sensing",
				To:     "conversation",
				Reason: "test",
			},
		},
	})
	return stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_AgentTurn{
			AgentTurn: &gilv1.AgentTurn{
				Content: "What do you want?",
			},
		},
	})
}

func (s *testInterviewServer) Reply(req *gilv1.ReplyRequest, stream gilv1.InterviewService_ReplyServer) error {
	return stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_AgentTurn{
			AgentTurn: &gilv1.AgentTurn{
				Content: "got: " + req.Content,
			},
		},
	})
}

func (s *testInterviewServer) GetSpec(ctx context.Context, req *gilv1.GetSpecRequest) (*gilv1.FrozenSpec, error) {
	return &gilv1.FrozenSpec{
		SpecId:    "test-spec-id",
		SessionId: req.SessionId,
	}, nil
}

func (s *testInterviewServer) Confirm(ctx context.Context, req *gilv1.ConfirmRequest) (*gilv1.ConfirmResponse, error) {
	return &gilv1.ConfirmResponse{
		SpecId:         "test-spec-id",
		ContentSha256:  strings.Repeat("a", 64),
	}, nil
}

// Start implements RunService.Start for testing.
func (s *testRunServer) Start(ctx context.Context, req *gilv1.StartRunRequest) (*gilv1.StartRunResponse, error) {
	return &gilv1.StartRunResponse{
		Status:     "done",
		Iterations: 1,
		Tokens:     50,
		VerifyResults: []*gilv1.VerifyResult{{Name: "ok", Passed: true}},
	}, nil
}

func TestNew_OutputsSessionID(t *testing.T) {
	sock, cleanup := startGildForTest(t)
	defer cleanup()

	var buf bytes.Buffer
	cmd := newCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--socket", sock, "--working-dir", "/tmp/p"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	out := buf.String()
	require.Contains(t, out, "Session created:")
	// ULID is 26 chars
	require.Greater(t, len(out), 26)
}
