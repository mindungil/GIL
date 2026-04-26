package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func TestGild_StartsAndAcceptsCreate(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "gild.sock")
	dbPath := filepath.Join(dir, "sessions.db")

	srv, err := newServer(dbPath, sockPath)
	require.NoError(t, err)

	go func() {
		_ = srv.Serve()
	}()
	defer srv.Stop()

	// 클라이언트로 연결
	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		return err == nil
	}, time.Second, 20*time.Millisecond)

	conn, err := grpc.NewClient(
		"unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		}),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := gilv1.NewSessionServiceClient(conn)
	resp, err := client.Create(context.Background(), &gilv1.CreateRequest{WorkingDir: "/tmp"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Id)
}
