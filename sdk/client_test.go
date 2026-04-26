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

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// testSessionServer is a minimal implementation of SessionServiceServer for testing.
type testSessionServer struct {
	gilv1.UnimplementedSessionServiceServer
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

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "gild.sock")

	// Clean up socket file if it exists
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	lis, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	g := grpc.NewServer()
	gilv1.RegisterSessionServiceServer(g, &testSessionServer{})
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
	}
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
