// Package sdk provides a gRPC client wrapper for the Gil autonomous coding harness.
package sdk

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
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
//
// CreatedAt / UpdatedAt are zero-valued when the server didn't fill
// the proto field (e.g. older daemons). TotalTokens / TotalCostUSD
// are the persisted rollup; CurrentIteration / CurrentTokens are
// the live snapshot for RUNNING sessions.
//
// BudgetMaxTokens / BudgetMaxCostUSD are zero when the spec didn't
// set a cap on that dimension. BudgetExceeded is the sticky flag
// the server sets after observing a budget_exceeded event; clients
// use it to keep the alert glyph on the row after the run stops.
type Session struct {
	ID               string
	Status           string
	WorkingDir       string
	GoalHint         string
	SpecID           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	TotalTokens      int64
	TotalCostUSD     float64
	CurrentIteration int32
	CurrentTokens    int64
	BudgetMaxTokens  int64
	BudgetMaxCostUSD float64
	BudgetExceeded   bool
	BudgetReason     string
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

// DeleteSession removes a session by ID. Returns the number of bytes
// freed (server-side) for the session's on-disk workspace dir; zero
// when the session had no artefacts. NotFound and FailedPrecondition
// (running session) are surfaced as gRPC errors — callers should
// inspect status.Code(err) to distinguish them.
func (c *Client) DeleteSession(ctx context.Context, id string) (freedBytes int64, err error) {
	resp, err := c.sessions.Delete(ctx, &gilv1.DeleteRequest{Id: id})
	if err != nil {
		return 0, err
	}
	return resp.FreedBytes, nil
}

// fromProto converts a proto Session to the SDK Session value type.
// Returns nil if the input is nil.
func fromProto(s *gilv1.Session) *Session {
	if s == nil {
		return nil
	}
	out := &Session{
		ID:               s.Id,
		Status:           s.Status.String(),
		WorkingDir:       s.WorkingDir,
		GoalHint:         s.GoalHint,
		SpecID:           s.SpecId,
		TotalTokens:      s.TotalTokens,
		TotalCostUSD:     s.TotalCostUsd,
		CurrentIteration: s.CurrentIteration,
		CurrentTokens:    s.CurrentTokens,
		BudgetMaxTokens:  s.BudgetMaxTokens,
		BudgetMaxCostUSD: s.BudgetMaxCostUsd,
		BudgetExceeded:   s.BudgetExceeded,
		BudgetReason:     s.BudgetReason,
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.AsTime()
	}
	if s.UpdatedAt != nil {
		out.UpdatedAt = s.UpdatedAt.AsTime()
	}
	return out
}

// InterviewModels lets callers specify per-stage models for an interview.
// Empty fields fall back to the request's primary Model field.
type InterviewModels struct {
	SlotModel      string // slot extraction; empty → falls back to Model
	AdversaryModel string // critique; empty → falls back to Model
	AuditModel     string // self-audit gate; empty → falls back to Model
}

// StartInterview begins an interview for sessionID. Returns a server stream
// that emits agent events (stage transitions, agent turns, errors). The caller
// must drain the stream until io.EOF or first AgentTurn before calling Reply.
//
// providerName is "anthropic", "mock", or "" (server default = anthropic).
// model is provider-specific (empty → server default for that provider).
// models is optional; pass zero value (sdk.InterviewModels{}) to use model for all stages.
func (c *Client) StartInterview(ctx context.Context, sessionID, firstInput, providerName, model string, models InterviewModels) (gilv1.InterviewService_StartClient, error) {
	return c.interviews.Start(ctx, &gilv1.StartInterviewRequest{
		SessionId:      sessionID,
		FirstInput:     firstInput,
		Provider:       providerName,
		Model:          model,
		SlotModel:      models.SlotModel,
		AdversaryModel: models.AdversaryModel,
		AuditModel:     models.AuditModel,
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

// StartRun executes the agent loop. When detach=false (default), blocks until
// completion and returns the final result. When detach=true, returns immediately
// with Status="started"; observe progress via TailRun or GetSession.
func (c *Client) StartRun(ctx context.Context, sessionID, providerName, model string, detach bool) (*gilv1.StartRunResponse, error) {
	return c.runs.Start(ctx, &gilv1.StartRunRequest{
		SessionId: sessionID,
		Provider:  providerName,
		Model:     model,
		Detach:    detach,
	})
}

// TailRun subscribes to the session's event stream (Phase 5 stub on server).
func (c *Client) TailRun(ctx context.Context, sessionID string) (gilv1.RunService_TailClient, error) {
	return c.runs.Tail(ctx, &gilv1.TailRequest{SessionId: sessionID})
}

// RestoreRun rolls back the session's workspace to the given checkpoint step.
// Positive step counts oldest-first (step=1 → oldest); negative counts
// newest-first (step=-1 → most recent).
func (c *Client) RestoreRun(ctx context.Context, sessionID string, step int32) (*gilv1.RestoreResponse, error) {
	return c.runs.Restore(ctx, &gilv1.RestoreRequest{
		SessionId: sessionID,
		Step:      step,
	})
}

// AnswerPermission sends a yes/no response to a pending permission_ask
// with ONCE semantics (legacy bool field). delivered=false means the
// request_id wasn't pending (timed out or unknown).
//
// Prefer AnswerPermissionDecision when the caller wants to record the
// user's persistence intent (session / always). The bool form is kept so
// older clients (the gil run --interactive prompt, phase07 e2e) can stay
// on the once-tier path without thinking about persistence.
func (c *Client) AnswerPermission(ctx context.Context, sessionID, requestID string, allow bool) (bool, error) {
	resp, err := c.runs.AnswerPermission(ctx, &gilv1.AnswerPermissionRequest{
		SessionId: sessionID,
		RequestId: requestID,
		Allow:     allow,
	})
	if err != nil {
		return false, err
	}
	return resp.Delivered, nil
}

// AnswerClarification sends the user's free-form answer to a pending
// clarify_requested ask. delivered=false means the ask_id is no longer
// pending (already answered, timed out, or never existed) — the same
// race-tolerant shape as AnswerPermission.
//
// The answer is fed back to the agent as the clarify tool's tool_result
// content; an empty string is allowed (the agent treats it the same as
// "no extra info" — equivalent to a soft-fail without the timeout
// error string).
func (c *Client) AnswerClarification(ctx context.Context, sessionID, askID, answer string) (bool, error) {
	resp, err := c.runs.AnswerClarification(ctx, &gilv1.AnswerClarificationRequest{
		SessionId: sessionID,
		AskId:     askID,
		Answer:    answer,
	})
	if err != nil {
		return false, err
	}
	return resp.Delivered, nil
}

// AnswerPermissionDecision sends the user's full answer (allow/deny x
// once/session/always) to a pending permission_ask. The server uses the
// enum to drive both the runner unblock AND the persistence side-effect
// (in-memory session list for *_SESSION; on-disk PersistentStore for
// *_ALWAYS). delivered=false has the same meaning as in AnswerPermission.
//
// Use this from the TUI modal where the user picks one of the six tiers
// directly.
func (c *Client) AnswerPermissionDecision(ctx context.Context, sessionID, requestID string, decision gilv1.PermissionDecision) (bool, error) {
	resp, err := c.runs.AnswerPermission(ctx, &gilv1.AnswerPermissionRequest{
		SessionId: sessionID,
		RequestId: requestID,
		Decision:  decision,
	})
	if err != nil {
		return false, err
	}
	return resp.Delivered, nil
}

// RequestCompact asks the server to queue a compaction at the next turn
// boundary for sessionID. Returns queued=false with reason set when no
// run is in flight (the reason is server-supplied and safe to surface
// directly to a user). The server never preempts an in-flight tool
// call — the runner observes the flag at the top of the next iteration.
func (c *Client) RequestCompact(ctx context.Context, sessionID string) (queued bool, reason string, err error) {
	resp, err := c.runs.RequestCompact(ctx, &gilv1.RequestCompactRequest{SessionId: sessionID})
	if err != nil {
		return false, "", err
	}
	return resp.Queued, resp.Reason, nil
}

// PostHint stages a non-binding hint for the agent's next turn. The
// hint shape is opaque key/value: today the canonical key is "model"
// (suggest a model switch) but surfaces may carry additional keys
// without a wire change. Returns posted=false when the session has no
// run in flight.
func (c *Client) PostHint(ctx context.Context, sessionID string, hint map[string]string) (posted bool, reason string, err error) {
	resp, err := c.runs.PostHint(ctx, &gilv1.PostHintRequest{
		SessionId: sessionID,
		Hint:      hint,
	})
	if err != nil {
		return false, "", err
	}
	return resp.Posted, resp.Reason, nil
}

// DiffResult is the SDK-side view of a session diff. Truncated indicates
// the body was clipped server-side; TruncatedBytes carries the count the
// server dropped from the tail. Note is non-empty when the session has
// no checkpoints yet (a normal state, not an error).
type DiffResult struct {
	UnifiedDiff    string
	FilesChanged   int32
	LinesAdded     int32
	LinesRemoved   int32
	Truncated      bool
	TruncatedBytes int32
	CheckpointSHA  string
	Note           string
}

// Diff fetches the unified diff between the latest shadow-git
// checkpoint for sessionID and the current workspace state. The diff
// is read-only — the workspace is unchanged. Use the Note field to
// detect the "no checkpoints yet" case without parsing error strings.
func (c *Client) Diff(ctx context.Context, sessionID string) (*DiffResult, error) {
	resp, err := c.runs.Diff(ctx, &gilv1.DiffRequest{SessionId: sessionID})
	if err != nil {
		return nil, err
	}
	return &DiffResult{
		UnifiedDiff:    resp.UnifiedDiff,
		FilesChanged:   resp.FilesChanged,
		LinesAdded:     resp.LinesAdded,
		LinesRemoved:   resp.LinesRemoved,
		Truncated:      resp.Truncated,
		TruncatedBytes: resp.TruncatedBytes,
		CheckpointSHA:  resp.CheckpointSha,
		Note:           resp.Note,
	}, nil
}
