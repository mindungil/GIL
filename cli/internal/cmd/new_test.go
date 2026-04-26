package cmd

import (
	"bytes"
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
type testSessionServer struct {
	gilv1.UnimplementedSessionServiceServer
}

func (s *testSessionServer) Create(ctx context.Context, req *gilv1.CreateRequest) (*gilv1.Session, error) {
	return &gilv1.Session{
		Id:         "01TESTSESSIONIDXXXXXXXXXX1", // 26-char ULID-like for length check
		Status:     gilv1.SessionStatus_CREATED,
		WorkingDir: req.WorkingDir,
		GoalHint:   req.GoalHint,
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
