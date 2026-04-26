package sdk

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// testSessionServer is a minimal implementation of SessionServiceServer for testing.
type testSessionServer struct {
	gilv1.UnimplementedSessionServiceServer
	failGetWith codes.Code // if non-zero, Get returns this code
}

// testInterviewServer is a minimal implementation of InterviewServiceServer for testing.
type testInterviewServer struct {
	gilv1.UnimplementedInterviewServiceServer
}

// testRunServer is a minimal implementation of RunServiceServer for testing.
type testRunServer struct {
	gilv1.UnimplementedRunServiceServer
}

// Create returns a test session.
func (s *testSessionServer) Create(ctx context.Context, req *gilv1.CreateRequest) (*gilv1.Session, error) {
	return &gilv1.Session{
		Id:         "test-id-123",
		Status:     gilv1.SessionStatus_CREATED,
		WorkingDir: req.WorkingDir,
		GoalHint:   req.GoalHint,
	}, nil
}

// Get returns a test session by ID.
func (s *testSessionServer) Get(ctx context.Context, req *gilv1.GetRequest) (*gilv1.Session, error) {
	if s.failGetWith != codes.OK {
		return nil, status.Errorf(s.failGetWith, "test injected error")
	}
	return &gilv1.Session{
		Id:     req.Id,
		Status: gilv1.SessionStatus_CREATED,
	}, nil
}

// List returns a list of test sessions.
func (s *testSessionServer) List(ctx context.Context, req *gilv1.ListRequest) (*gilv1.ListResponse, error) {
	return &gilv1.ListResponse{
		Sessions: []*gilv1.Session{
			{
				Id:     "test-id-1",
				Status: gilv1.SessionStatus_CREATED,
			},
			{
				Id:     "test-id-2",
				Status: gilv1.SessionStatus_RUNNING,
			},
		},
	}, nil
}

// Start implements InterviewService.Start for testing.
func (s *testInterviewServer) Start(req *gilv1.StartInterviewRequest, stream gilv1.InterviewService_StartServer) error {
	// Emit stage transition
	if err := stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_Stage{Stage: &gilv1.StageTransition{
			From: "sensing", To: "conversation", Reason: "test",
		}},
	}); err != nil {
		return err
	}
	// Emit agent turn
	return stream.Send(&gilv1.InterviewEvent{
		Payload: &gilv1.InterviewEvent_AgentTurn{AgentTurn: &gilv1.AgentTurn{
			Content: "test question?",
		}},
	})
}

// Confirm implements InterviewService.Confirm for testing.
func (s *testInterviewServer) Confirm(ctx context.Context, req *gilv1.ConfirmRequest) (*gilv1.ConfirmResponse, error) {
	return &gilv1.ConfirmResponse{
		SpecId:        "test-spec-id",
		ContentSha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}, nil
}

// GetSpec implements InterviewService.GetSpec for testing.
func (s *testInterviewServer) GetSpec(ctx context.Context, req *gilv1.GetSpecRequest) (*gilv1.FrozenSpec, error) {
	return &gilv1.FrozenSpec{
		SpecId:    "test-spec-id",
		SessionId: req.SessionId,
	}, nil
}

// Start implements RunService.Start for testing.
func (s *testRunServer) Start(ctx context.Context, req *gilv1.StartRunRequest) (*gilv1.StartRunResponse, error) {
	return &gilv1.StartRunResponse{
		Status:     "done",
		Iterations: 2,
		Tokens:     100,
		VerifyResults: []*gilv1.VerifyResult{
			{Name: "exists", Passed: true, ExitCode: 0},
		},
	}, nil
}

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	sock, stop, _ := startTestServerWithCtrl(t)
	return sock, stop
}

func startTestServerWithCtrl(t *testing.T) (string, func(), *testSessionServer) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "gild.sock")

	// Clean up socket file if it exists
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	lis, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	srv := &testSessionServer{}
	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, srv)
	gilv1.RegisterInterviewServiceServer(g, &testInterviewServer{})
	gilv1.RegisterRunServiceServer(g, &testRunServer{})
	go g.Serve(lis)

	// Wait for the server to be ready
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("unix", sockPath, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}, time.Second, 20*time.Millisecond)

	return sockPath, func() {
		g.GracefulStop()
		_ = lis.Close()
	}, srv
}

func TestClient_Dial_AndCreateSession(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	s, err := cli.CreateSession(context.Background(), CreateOptions{WorkingDir: "/tmp"})
	require.NoError(t, err)
	require.Equal(t, "test-id-123", s.ID)
	require.Equal(t, "/tmp", s.WorkingDir)
	require.Equal(t, "CREATED", s.Status)
}

func TestClient_GetSession(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	s, err := cli.GetSession(context.Background(), "test-id-456")
	require.NoError(t, err)
	require.Equal(t, "test-id-456", s.ID)
	require.Equal(t, "CREATED", s.Status)
}

func TestClient_ListSessions(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	sessions, err := cli.ListSessions(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, "test-id-1", sessions[0].ID)
	require.Equal(t, "test-id-2", sessions[1].ID)
	require.Equal(t, "RUNNING", sessions[1].Status)
}

func TestClient_GetSession_NotFound(t *testing.T) {
	sock, stop, srv := startTestServerWithCtrl(t)
	defer stop()

	srv.failGetWith = codes.NotFound

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	_, err = cli.GetSession(context.Background(), "any")
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestClient_StartInterview_StreamsEvents(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	stream, err := cli.StartInterview(context.Background(), "sess-1", "build a CLI", "mock", "", InterviewModels{})
	require.NoError(t, err)

	// First event: stage transition
	evt, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, evt.GetStage())
	require.Equal(t, "conversation", evt.GetStage().To)

	// Second event: agent turn
	evt, err = stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, evt.GetAgentTurn())
	require.Equal(t, "test question?", evt.GetAgentTurn().Content)
}

func TestClient_ConfirmInterview(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	specID, hex, err := cli.ConfirmInterview(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, "test-spec-id", specID)
	require.Len(t, hex, 64)
}

func TestClient_GetSpec(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	fs, err := cli.GetSpec(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, "test-spec-id", fs.SpecId)
	require.Equal(t, "sess-1", fs.SessionId)
}

func TestClient_StartRun(t *testing.T) {
	sock, stop := startTestServer(t)
	defer stop()

	cli, err := Dial(sock)
	require.NoError(t, err)
	defer cli.Close()

	resp, err := cli.StartRun(context.Background(), "sess-1", "mock", "", false)
	require.NoError(t, err)
	require.Equal(t, "done", resp.Status)
	require.Equal(t, int32(2), resp.Iterations)
	require.Len(t, resp.VerifyResults, 1)
}
