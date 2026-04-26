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

// Client is a gRPC client for the Gil SessionService, InterviewService, and RunService.
type Client struct {
	conn       *grpc.ClientConn
	sessions   gilv1.SessionServiceClient
	interviews gilv1.InterviewServiceClient
	runs       gilv1.RunServiceClient
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
		conn:       conn,
		sessions:   gilv1.NewSessionServiceClient(conn),
		interviews: gilv1.NewInterviewServiceClient(conn),
		runs:       gilv1.NewRunServiceClient(conn),
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

// fromProto converts a proto Session to the SDK Session value type.
// Returns nil if the input is nil.
func fromProto(s *gilv1.Session) *Session {
	if s == nil {
		return nil
	}
	return &Session{
		ID:         s.Id,
		Status:     s.Status.String(),
		WorkingDir: s.WorkingDir,
		GoalHint:   s.GoalHint,
	}
}

// StartInterview begins an interview for sessionID. Returns a server stream
// that emits agent events (stage transitions, agent turns, errors). The caller
// must drain the stream until io.EOF or first AgentTurn before calling Reply.
//
// providerName is "anthropic", "mock", or "" (server default = anthropic).
// model is provider-specific (empty → server default for that provider).
func (c *Client) StartInterview(ctx context.Context, sessionID, firstInput, providerName, model string) (gilv1.InterviewService_StartClient, error) {
	return c.interviews.Start(ctx, &gilv1.StartInterviewRequest{
		SessionId:  sessionID,
		FirstInput: firstInput,
		Provider:   providerName,
		Model:      model,
	})
}

// ReplyInterview sends a user reply mid-interview. Returns a stream of
// subsequent agent events.
func (c *Client) ReplyInterview(ctx context.Context, sessionID, content string) (gilv1.InterviewService_ReplyClient, error) {
	return c.interviews.Reply(ctx, &gilv1.ReplyRequest{
		SessionId: sessionID,
		Content:   content,
	})
}

// ConfirmInterview freezes the spec for sessionID. Returns the spec ID and
// SHA-256 hex of the frozen content.
func (c *Client) ConfirmInterview(ctx context.Context, sessionID string) (specID, contentSha256 string, err error) {
	resp, err := c.interviews.Confirm(ctx, &gilv1.ConfirmRequest{SessionId: sessionID})
	if err != nil {
		return "", "", err
	}
	return resp.SpecId, resp.ContentSha256, nil
}

// GetSpec returns the current (possibly partial) spec for sessionID.
func (c *Client) GetSpec(ctx context.Context, sessionID string) (*gilv1.FrozenSpec, error) {
	return c.interviews.GetSpec(ctx, &gilv1.GetSpecRequest{SessionId: sessionID})
}

// StartRun executes the agent loop synchronously and returns the result.
// providerName: "anthropic" | "mock" | "" (server default).
func (c *Client) StartRun(ctx context.Context, sessionID, providerName, model string) (*gilv1.StartRunResponse, error) {
	return c.runs.Start(ctx, &gilv1.StartRunRequest{
		SessionId: sessionID,
		Provider:  providerName,
		Model:     model,
	})
}

// TailRun subscribes to the session's event stream (Phase 5 stub on server).
func (c *Client) TailRun(ctx context.Context, sessionID string) (gilv1.RunService_TailClient, error) {
	return c.runs.Tail(ctx, &gilv1.TailRequest{SessionId: sessionID})
}
