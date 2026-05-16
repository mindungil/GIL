package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"

	"github.com/mindungil/gil/cli/internal/errmap"
)

// Message is one item from a Prompt stream turn, delivered in wire
// order. Kind is "text" for an assistant text chunk (carried in Text)
// or "event" for a server-emitted tracker event (carried in Event).
// Producers emit Messages onto a single ordered channel so consumers
// render them in the order they arrived from the daemon. Replaces the
// pre-iter14a split chunkCh/eventCh whose two-goroutine demux raced
// when text arrived between two tool events, surfacing verify_missing
// mid-tool-sequence (eval-loop iter14 L9, iter15 L15).
type Message struct {
	Kind  string // "text" | "event"
	Text  string
	Event TrackerInput
}

// GRPCClient adapts sdk.Client to the SessionClient interface.
//
// Streaming model:
//   - Each user turn opens a SessionService.Prompt server stream.
//     SendPrompt allocates a fresh msgCh + msgDone for the turn, then
//     launches drainPromptStream pushing all Parts (text + tool events
//     + session.allocated + metrics) onto msgCh in wire order. Done
//     closes msgDone. Per-turn channels prevent stale items from
//     leaking across turns.
//
// Known limitation: drainPromptStream does not observe ctx; its
// goroutine outlives /quit until the underlying stream closes. Acceptable
// for V1 dogfood since process exit closes the gRPC connection.
type GRPCClient struct {
	sdk        *sdk.Client
	activeSess string
	workingDir string

	providerName string
	model        string

	// msgCh receives all Prompt stream items for the current turn in
	// wire order: text chunks AND tool events flow through the same
	// channel so the consumer cannot interleave them out of order.
	msgCh chan Message
	// msgDone is closed when the current Prompt stream ends.
	msgDone chan struct{}
	// streamErr captures the terminating error from the Prompt stream
	// (non-EOF only). Read after msgDone closes.
	streamErrMu sync.Mutex
	streamErr   error
}

// NewGRPCClient constructs a GRPCClient. workingDir is used when
// auto-creating a session on the first SendPrompt.
func NewGRPCClient(s *sdk.Client, workingDir string) *GRPCClient {
	return &GRPCClient{
		sdk:        s,
		workingDir: workingDir,
		msgCh:      make(chan Message, 64),
		msgDone:    make(chan struct{}),
	}
}

// SetProvider sets the provider/model to forward in StartInterview.
// Empty strings fall through to the daemon's workspace-config defaults.
func (g *GRPCClient) SetProvider(name, model string) {
	g.providerName = name
	g.model = model
}

// ActiveSessionID returns the current session ID (empty if none).
func (g *GRPCClient) ActiveSessionID() string { return g.activeSess }

// NewSession creates a new session on the server and sets it as active.
// Any prior subscription state is discarded. Equivalent to newSession with
// an empty hint — used by the /new slash command, where no first prompt
// is available yet.
func (g *GRPCClient) NewSession(ctx context.Context) error {
	return g.newSession(ctx, "")
}

// newSession is the internal session-create that accepts an optional
// GoalHint. SendPrompt uses this to seed the hint from the user's first
// message so /sessions has a human-readable label per row instead of two
// columns of identical ULID prefixes.
func (g *GRPCClient) newSession(ctx context.Context, hint string) error {
	sess, err := g.sdk.CreateSession(ctx, sdk.CreateOptions{
		WorkingDir: g.workingDir,
		GoalHint:   truncateHint(hint, 80),
	})
	if err != nil {
		return errmap.WrapRPCError(err)
	}
	g.activeSess = sess.ID
	return nil
}

// truncateHint trims a free-form prompt to fit a single listing column.
// Whitespace-collapses then cuts at max runes with an ellipsis when needed.
func truncateHint(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// SwitchSession makes the session identified by idOrName active. idOrName
// may be an exact ID or a unique prefix (SDK has no resolve endpoint; we
// list and match).
func (g *GRPCClient) SwitchSession(ctx context.Context, idOrName string) error {
	list, err := g.sdk.ListSessions(ctx, 100)
	if err != nil {
		return errmap.WrapRPCError(err)
	}
	for _, s := range list {
		if s.ID == idOrName || strings.HasPrefix(s.ID, idOrName) {
			g.activeSess = s.ID
			return nil
		}
	}
	return fmt.Errorf("session not found: %s", idOrName)
}

// ListSessions returns a summary list of all sessions (up to 100).
func (g *GRPCClient) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	list, err := g.sdk.ListSessions(ctx, 100)
	if err != nil {
		return nil, errmap.WrapRPCError(err)
	}
	out := make([]SessionSummary, 0, len(list))
	for _, s := range list {
		// Name is the human label for the listing column; use the
		// session's GoalHint (the first prompt, captured at create time).
		// Falls back to empty so the renderer can show "—".
		out = append(out, SessionSummary{
			ID:        s.ID,
			Name:      truncateHint(s.GoalHint, 60),
			Phase:     s.Status,
			GoalHint:  s.GoalHint,
			CreatedAt: s.CreatedAt,
		})
	}
	return out, nil
}

// SendPrompt sends the user's text to the server. On the first call to a
// session it calls StartInterview (which includes the first user turn);
// on subsequent calls it calls ReplyInterview. If no session is active
// one is auto-created.
//
// Each turn allocates fresh msgCh/msgDone so items cannot leak between
// turns and the previous goroutine cannot close the new turn's done
// channel.
//
// Per docs/design/chat-architecture.md M2 this routes through the
// single SessionService.Prompt RPC. Verb dispatch (diff, merge,
// freeze, run) lands as agent tool_calls inside the Part stream, not
// as separate RPCs. session_id may be empty for the first turn — the
// first streamed Part carries SessionAllocatedPart with the new id.
func (g *GRPCClient) SendPrompt(ctx context.Context, prompt string) error {
	g.msgCh = make(chan Message, 64)
	g.msgDone = make(chan struct{})
	msgCh := g.msgCh
	done := g.msgDone

	stream, err := g.sdk.Prompt(ctx, sdk.PromptOptions{
		SessionID:  g.activeSess,
		Text:       prompt,
		Provider:   g.providerName,
		Model:      g.model,
		WorkingDir: g.workingDir,
	})
	if err != nil {
		return errmap.WrapRPCError(err)
	}
	go g.drainPromptStream(stream, msgCh, done)
	return nil
}

// drainPromptStream consumes the SessionService.Prompt Part stream
// for one turn and pushes Messages onto msgCh in wire order. Text
// Parts become Kind="text"; tool events, SessionAllocated, and Metrics
// become Kind="event" with a TrackerInput payload. DonePart closes the
// stream. Single channel = order-preserving by construction; the prior
// dual-channel demux raced when text arrived between events.
func (g *GRPCClient) drainPromptStream(
	stream gilv1.SessionService_PromptClient,
	msgCh chan<- Message,
	done chan struct{},
) {
	defer close(done)
	for {
		ev, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				g.streamErrMu.Lock()
				g.streamErr = err
				g.streamErrMu.Unlock()
			}
			return
		}
		switch b := ev.GetBody().(type) {
		case *gilv1.Part_Text:
			if b.Text != nil && b.Text.GetContent() != "" {
				msgCh <- Message{Kind: "text", Text: b.Text.GetContent()}
			}
		case *gilv1.Part_ToolCall:
			if b.ToolCall != nil {
				msgCh <- Message{Kind: "event", Event: TrackerInput{
					Kind:      "tool.call",
					SessionID: g.activeSess,
					ToolName:  b.ToolCall.GetName(),
					ToolID:    b.ToolCall.GetId(),
					ToolInput: b.ToolCall.GetInputJson(),
				}}
			}
		case *gilv1.Part_ToolResult:
			if b.ToolResult != nil {
				msgCh <- Message{Kind: "event", Event: TrackerInput{
					Kind:        "tool.result",
					SessionID:   g.activeSess,
					ToolID:      b.ToolResult.GetCallId(),
					ToolContent: b.ToolResult.GetContent(),
					ToolIsError: b.ToolResult.GetIsError(),
				}}
			}
		case *gilv1.Part_SessionAllocated:
			if b.SessionAllocated != nil {
				g.activeSess = b.SessionAllocated.GetSessionId()
				msgCh <- Message{Kind: "event", Event: TrackerInput{
					Kind:      "session.allocated",
					SessionID: g.activeSess,
				}}
			}
		case *gilv1.Part_Metrics:
			if b.Metrics != nil {
				msgCh <- Message{Kind: "event", Event: TrackerInput{
					Kind:      "prompt.metrics",
					SessionID: g.activeSess,
					Tokens:    b.Metrics.GetTokensIn() + b.Metrics.GetTokensOut(),
					LatencyMs: b.Metrics.GetLatencyMs(),
				}}
			}
		case *gilv1.Part_Done:
			if b.Done != nil && b.Done.GetStopReason() == "error" {
				g.streamErrMu.Lock()
				g.streamErr = errors.New(b.Done.GetErrorMessage())
				g.streamErrMu.Unlock()
			}
			return
		}
	}
}

// NextMessage returns the next item from the current Prompt stream
// turn. Items arrive in wire order, so the caller renders them in the
// order received.
//
// Contract:
//   - A Message received from msgCh returns (msg, true, nil).
//   - When msgDone is closed AND msgCh is drained, returns
//     (Message{}, false, streamErr) — the turn is over.
//   - On context cancellation, returns (Message{}, false, ctx.Err()).
func (g *GRPCClient) NextMessage(ctx context.Context) (Message, bool, error) {
	select {
	case <-ctx.Done():
		return Message{}, false, ctx.Err()
	case msg := <-g.msgCh:
		return msg, true, nil
	case <-g.msgDone:
		select {
		case msg := <-g.msgCh:
			return msg, true, nil
		default:
		}
		g.streamErrMu.Lock()
		serr := g.streamErr
		g.streamErr = nil
		g.streamErrMu.Unlock()
		return Message{}, false, serr
	}
}

// (Spec / Status / Diff / Merge / Compact / StartRun verb methods
// removed in M3 — chat surface routes through SessionService.Prompt
// and the agent calls these as tools server-side. The verb-mode
// cobra subcommands in cli/internal/cmd/* still call the SDK
// directly for headless / script use.)

// Close closes the underlying gRPC connection.
func (g *GRPCClient) Close() error {
	if g.sdk == nil {
		return nil
	}
	return g.sdk.Close()
}

// mapRunEventToTracker converts a generic gilv1.Event (from RunService.Tail)
// to a TrackerInput. The event type string follows the convention established
// in the tracker (e.g. "run.started", "run.iter").
//
// RunService.Tail emits generic Event messages with Type+DataJson. The type
// strings below are the canonical names the server emits; adjust if the
// server uses different type strings.
func mapRunEventToTracker(sessionID string, ev *gilv1.Event) TrackerInput {
	if ev == nil {
		return TrackerInput{}
	}
	in := TrackerInput{SessionID: sessionID}
	// Pull EventMetrics.Tokens / LatencyMs once, off the kind switch,
	// so any future event type that carries metrics flows through to
	// the SessionState totals without an explicit case below. Tokens
	// accumulate (sum at the tracker), LatencyMs snapshots.
	if mt := ev.GetMetrics(); mt != nil {
		in.Tokens = mt.GetTokens()
		in.LatencyMs = mt.GetLatencyMs()
	}
	switch ev.GetType() {
	case "run.started":
		in.Kind = "run.started"
		if ev.GetMetrics() != nil {
			// MaxIter not available in generic Event metrics; left at zero.
		}
	case "run.iter":
		in.Kind = "run.iter"
		if ev.GetMetrics() != nil {
			in.CostUSD = ev.GetMetrics().CostUsd
		}
	// The runner emits "stuck_detected" / "stuck_recovered" (see
	// core/runner/runner.go ~727,746). Earlier the adapter listened
	// for "run.stuck", so the user never saw the stuck signal —
	// every stuck loop appeared as a generic hang. Internal Kind
	// stays as "run.stuck" / "run.recovered" so state.go and loop.go
	// taxonomies don't shift.
	case "stuck_detected":
		in.Kind = "run.stuck"
		// Pattern + detail enrich the SystemNote so the user sees
		// WHICH of the 6 detector patterns triggered, not just
		// "stuck". Best-effort parse — bail to bare phase change if
		// the payload shape is unexpected.
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if p, ok := d["pattern"].(string); ok {
				in.StuckPattern = p
			}
			if dt, ok := d["detail"].(string); ok {
				in.StuckDetail = dt
			}
		}
	case "stuck_recovered":
		in.Kind = "run.recovered"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if e, ok := d["explanation"].(string); ok {
				in.StuckDetail = e
			}
		}
	case "run.done":
		in.Kind = "run.done"
		if ev.GetMetrics() != nil {
			in.CostUSD = ev.GetMetrics().CostUsd
		}
	case "compact_start":
		// Runner crossed the threshold (or /compact forced one) and
		// is about to compress the conversation history. Surfacing
		// gives the user a heads-up before the next iteration's
		// provider request looks suspiciously fast.
		in.Kind = "compact_start"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if forced, _ := d["forced"].(bool); forced {
				in.Reason = "forced via /compact"
			} else if v, ok := d["estimated_tokens"].(float64); ok {
				in.Tokens = int64(v) // reuse Tokens to carry the estimate
			}
		}
	case "compact_done":
		in.Kind = "compact_done"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if v, ok := d["saved_tokens"].(float64); ok {
				in.Tokens = int64(v) // reuse Tokens to carry savings
			}
			if v, ok := d["original"].(float64); ok {
				in.RetryAttempt = int(v) // reuse counter for original count
			}
			if v, ok := d["compacted"].(float64); ok {
				in.RetryMax = int(v) // reuse for compacted count
			}
		}
	case "compact_error":
		in.Kind = "compact_error"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if e, ok := d["err"].(string); ok {
				in.Reason = e
			}
		}
	case "subagent_started":
		// Parent agent kicked off a sub-loop investigation (the
		// "subagent" tool, not RunSubagent — same event though).
		// Surface the goal so the user understands the gap before
		// subagent_done fires; long sub-loops otherwise look like
		// the parent stopped working. Phase stays whatever it was.
		in.Kind = "subagent_started"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if g, ok := d["goal"].(string); ok {
				in.Reason = g
			}
		}
	case "subagent_done":
		// Sub-loop finished. The summary is truncated to 512B server-
		// side (runner.go) so it's safe to forward whole — chat strip
		// will narrow it further if needed.
		in.Kind = "subagent_done"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if s, ok := d["summary"].(string); ok {
				in.Reason = s
			}
			if v, ok := d["iterations"].(float64); ok {
				in.RetryAttempt = int(v) // reuse field as iteration count
			}
		}
	case "budget_warning":
		// Cost or token budget crossed a 75% / 90% threshold. Reason
		// distinguishes "tokens" vs "cost" so the user knows which
		// dial is closing in. budget_exceeded latches sticky, but a
		// warning is just a heads-up — phase unchanged.
		in.Kind = "budget_warning"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if r, ok := d["reason"].(string); ok {
				in.Reason = r
			}
			if v, ok := d["used"].(float64); ok {
				in.CostUSD = v
			}
		}
	case "budget_exceeded":
		// Budget hit the hard limit; the runner will halt at end of
		// the current iteration. Sticky — surface as an alert rather
		// than a passing note so the user can decide whether to bump
		// the budget or stop.
		in.Kind = "budget_exceeded"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if r, ok := d["reason"].(string); ok {
				in.Reason = r
			}
			if v, ok := d["used"].(float64); ok {
				in.CostUSD = v
			}
		}
	case "provider.retry_attempt":
		// Provider hit a transient failure (5xx / rate-limit / network).
		// Retry.OnRetry fired before sleeping `wait_ms` and trying again.
		// Surface so the user sees backoff is happening rather than
		// staring at silence — the previous adapter ignored these and
		// flaky upstreams looked like hangs.
		in.Kind = "provider.retry_attempt"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if v, ok := d["attempt"].(float64); ok {
				in.RetryAttempt = int(v)
			}
			if v, ok := d["max_attempts"].(float64); ok {
				in.RetryMax = int(v)
			}
			if v, ok := d["wait_ms"].(float64); ok {
				in.RetryWaitMs = int64(v)
			}
			if v, ok := d["err"].(string); ok {
				in.Reason = v
			}
		}
	case "permission_ask":
		// Followup #2 — pre-M3 claim was "dropped on the chat floor."
		// Wire the event through TrackerInput so loop.go surfaces it as
		// a SystemNote with the request_id the user feeds to
		// `gil permission answer`. S9 subagent routing means the ask
		// may originate from a child running under this root session;
		// surface the label so the user knows what asked.
		in.Kind = "permission.ask"
		var d map[string]any
		if jerr := json.Unmarshal(ev.GetDataJson(), &d); jerr == nil {
			if v, ok := d["request_id"].(string); ok {
				in.RequestID = v
			}
			if v, ok := d["tool"].(string); ok {
				in.PermissionTool = v
			}
			if v, ok := d["key"].(string); ok {
				in.PermissionKey = v
			}
			if v, ok := d["from_subagent_label"].(string); ok {
				in.FromSubagentLabel = v
			}
		}
	}
	return in
}

// shortID returns the first 10 characters of an ID, or the full ID if
// shorter. 10 covers the full ms-precision ULID timestamp prefix; 6
// (the previous default) bins to ~30s windows and collided in /sessions
// listings whenever multiple sessions were created in the same minute.
func shortID(id string) string {
	if len(id) >= 10 {
		return id[:10]
	}
	return id
}

// Compile-time assertion: GRPCClient must implement SessionClient.
var _ SessionClient = (*GRPCClient)(nil)
