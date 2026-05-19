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

// Client is a gRPC client for the Gil SessionService and RunService.
// InterviewService removed in M3.
type Client struct {
	conn     *grpc.ClientConn
	sessions gilv1.SessionServiceClient
	runs     gilv1.RunServiceClient
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
		runs:     gilv1.NewRunServiceClient(conn),
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

// InterviewModels / StartInterview / ReplyInterview / ConfirmInterview /
// GetSpec were the SDK surface for the now-deleted InterviewService
// (M3, docs/design/chat-architecture.md). Chat surfaces use Prompt
// instead; spec freezing moves to an agent tool in a future commit.

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

// PromptOptions controls a single SessionService.Prompt call. All
// fields are optional except SessionID (which may be empty to ask
// the daemon to allocate inline; the first streamed Part will then
// carry a SessionAllocatedPart so callers can pin the new id).
//
// Agent picks which agent prompt + tool subset runs the loop. Empty
// falls through to the daemon's "default" agent. Provider/Model
// override workspace.Resolve when set.
type PromptOptions struct {
	SessionID string
	Text      string
	Agent     string
	Provider  string
	Model     string
	// WorkingDir is forwarded to the daemon when SessionID is empty
	// so the auto-created session is rooted at the caller's chosen
	// path. Ignored when SessionID names an existing session (its
	// stored working_dir wins). Empty means "unrooted" — tools then
	// refuse with a clear error rather than silently writing to /tmp.
	WorkingDir string
	// Temperature overrides the daemon's default sampling temperature
	// (0.7) for this call. <= 0 means "use default". Surfaced for
	// dogfood / autonomous-coding runs that want lower variance
	// (Finding #6, 2026-05-18).
	Temperature float64
	// AdversaryModel identifies the model to consult when the daemon's
	// chat-side stuck Detector returns a signal that
	// AdversaryConsultStrategy can act on. Empty disables the adversary
	// path; AltToolOrder and other strategies still fire. See A1b spec
	// (2026-05-19).
	AdversaryModel string
}

// Prompt opens a streaming chat turn against the daemon's agent
// loop (docs/design/chat-architecture.md). The user types text,
// the server runs an agent loop that may call tools, and Parts
// stream back: TextDelta for assistant chunks, ToolCallPart /
// ToolResultPart for tool invocations, SessionAllocatedPart on the
// first call when SessionID is empty, PromptMetrics snapshots, and
// finally DonePart. Caller drains the returned stream until EOF.
func (c *Client) Prompt(ctx context.Context, opt PromptOptions) (gilv1.SessionService_PromptClient, error) {
	req := &gilv1.PromptRequest{
		SessionId:      opt.SessionID,
		Agent:          opt.Agent,
		WorkingDir:     opt.WorkingDir,
		Temperature:    opt.Temperature,
		AdversaryModel: opt.AdversaryModel,
		Parts: []*gilv1.PromptPart{
			{Body: &gilv1.PromptPart_Text{Text: opt.Text}},
		},
	}
	if opt.Provider != "" || opt.Model != "" {
		req.Model = &gilv1.ModelChoice{Provider: opt.Provider, ModelId: opt.Model}
	}
	return c.sessions.Prompt(ctx, req)
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
