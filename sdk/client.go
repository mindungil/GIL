// Package sdk provides a gRPC client wrapper for the Gil autonomous coding harness.
package sdk

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

// Client is a gRPC client for the Gil SessionService.
type Client struct {
	conn     *grpc.ClientConn
	sessions gilv1.SessionServiceClient
}

// Dial connects to a Gil gRPC server at the given Unix socket path.
func Dial(sockPath string) (*Client, error) {
	conn, err := grpc.NewClient(
		"unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", sockPath)
		}),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:     conn,
		sessions: gilv1.NewSessionServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// CreateOptions specifies options for creating a new session.
type CreateOptions struct {
	WorkingDir string
	GoalHint   string
}

// Session represents a Gil session.
type Session struct {
	ID         string
	Status     string
	WorkingDir string
	GoalHint   string
}

// CreateSession creates a new session with the given options.
func (c *Client) CreateSession(ctx context.Context, opts CreateOptions) (*Session, error) {
	resp, err := c.sessions.Create(ctx, &gilv1.CreateRequest{
		WorkingDir: opts.WorkingDir,
		GoalHint:   opts.GoalHint,
	})
	if err != nil {
		return nil, err
	}
	return fromProto(resp), nil
}

// GetSession retrieves a session by ID.
func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	resp, err := c.sessions.Get(ctx, &gilv1.GetRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return fromProto(resp), nil
}

// ListSessions lists sessions with a limit.
func (c *Client) ListSessions(ctx context.Context, limit int) ([]*Session, error) {
	resp, err := c.sessions.List(ctx, &gilv1.ListRequest{Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		out = append(out, fromProto(s))
	}
	return out, nil
}

// fromProto converts a protobuf Session to an SDK Session.
func fromProto(s *gilv1.Session) *Session {
	return &Session{
		ID:         s.Id,
		Status:     s.Status.String(),
		WorkingDir: s.WorkingDir,
		GoalHint:   s.GoalHint,
	}
}
