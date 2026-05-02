package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// GRPCClient adapts sdk.Client to the SessionClient interface.
//
// Streaming model:
//   - Each user turn opens an interview server stream (StartInterview on
//     the first turn; ReplyInterview on subsequent turns). SendPrompt
//     allocates a fresh chunkCh and chunkDone for the turn, then launches
//     drainInterviewStream with those channels captured by value.
//     AgentTurn payloads flow through chunkCh; other proto events become
//     TrackerInput on eventCh; chunkDone is closed when the stream ends.
//     Per-turn channels prevent stale chunks from leaking across turns
//     and stop a dying goroutine from closing the new turn's done channel.
//   - Run progress events come from RunService.Tail. startTailLoop runs
//     until the context is cancelled or the stream ends.
//
// Known limitation: drainInterviewStream does not observe ctx; its
// goroutine outlives /quit until the underlying stream closes. Acceptable
// for V1 dogfood since process exit closes the gRPC connection. T13/T14
// follow-up may plumb a per-turn ctx to enable eager cancellation.
type GRPCClient struct {
	sdk        *sdk.Client
	activeSess string
	workingDir string

	// inInterview tracks whether StartInterview has been called for the
	// current session. Once true, subsequent SendPrompt calls use
	// ReplyInterview instead of StartInterview.
	inInterview bool

	// chunkCh receives assistant text chunks for the current turn.
	chunkCh chan string
	// chunkDone is closed when the current interview stream ends.
	chunkDone chan struct{}
	// streamErr captures the terminating error from the interview
	// stream (non-EOF only). Read after chunkDone closes.
	streamErrMu sync.Mutex
	streamErr   error
	// eventCh receives tracker events from both interview and run streams.
	eventCh chan TrackerInput
}

// NewGRPCClient constructs a GRPCClient. workingDir is used when
// auto-creating a session on the first SendPrompt.
func NewGRPCClient(s *sdk.Client, workingDir string) *GRPCClient {
	return &GRPCClient{
		sdk:        s,
		workingDir: workingDir,
		chunkCh:    make(chan string, 64),
		chunkDone:  make(chan struct{}),
		eventCh:    make(chan TrackerInput, 64),
	}
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
		return err
	}
	g.activeSess = sess.ID
	g.inInterview = false
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
		return err
	}
	for _, s := range list {
		if s.ID == idOrName || strings.HasPrefix(s.ID, idOrName) {
			g.activeSess = s.ID
			g.inInterview = false
			return nil
		}
	}
	return fmt.Errorf("session not found: %s", idOrName)
}

// ListSessions returns a summary list of all sessions (up to 100).
func (g *GRPCClient) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	list, err := g.sdk.ListSessions(ctx, 100)
	if err != nil {
		return nil, err
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
// Each turn allocates fresh chunkCh/chunkDone so chunks cannot leak
// between turns and the previous goroutine cannot close the new turn's
// done channel.
func (g *GRPCClient) SendPrompt(ctx context.Context, prompt string) error {
	if g.activeSess == "" {
		if err := g.newSession(ctx, prompt); err != nil {
			return err
		}
	}
	// Allocate fresh per-turn channels and snapshot for the goroutine.
	g.chunkCh = make(chan string, 64)
	g.chunkDone = make(chan struct{})
	chunkCh := g.chunkCh
	done := g.chunkDone
	sessID := g.activeSess

	if !g.inInterview {
		stream, err := g.sdk.StartInterview(ctx, sessID, prompt, "", "", sdk.InterviewModels{})
		if err != nil {
			return err
		}
		g.inInterview = true
		go g.drainInterviewStream(stream, chunkCh, done, sessID)
	} else {
		stream, err := g.sdk.ReplyInterview(ctx, sessID, prompt)
		if err != nil {
			return err
		}
		go g.drainInterviewStream(stream, chunkCh, done, sessID)
	}
	return nil
}

// drainInterviewStream consumes a server-streaming InterviewEvent stream.
// AgentTurn payloads are forwarded as chunks; other payloads become
// TrackerInput events. The stream always ends with a close of the
// supplied done channel — which is the *snapshot* taken at goroutine
// launch, not whatever g.chunkDone happens to be later.
//
// Snapshotting prevents a stale goroutine from closing the new turn's
// done channel after SendPrompt allocates fresh per-turn channels.
func (g *GRPCClient) drainInterviewStream(
	stream interface {
		Recv() (*gilv1.InterviewEvent, error)
	},
	chunkCh chan<- string,
	done chan struct{},
	sessionID string,
) {
	defer close(done)
	for {
		ev, err := stream.Recv()
		if err != nil {
			// EOF is the normal end-of-turn signal. Any other error
			// (transport, gRPC status) is a real failure — record it so
			// NextAssistantChunk can surface it to the chat user instead
			// of silently looping back to idle.
			if !errors.Is(err, io.EOF) {
				g.streamErrMu.Lock()
				g.streamErr = err
				g.streamErrMu.Unlock()
			}
			return
		}
		switch v := ev.GetPayload().(type) {
		case *gilv1.InterviewEvent_AgentTurn:
			if v.AgentTurn != nil && v.AgentTurn.Content != "" {
				chunkCh <- v.AgentTurn.Content
			}
		case *gilv1.InterviewEvent_SaturationUpdate:
			if v.SaturationUpdate != nil {
				g.eventCh <- TrackerInput{
					Kind:        "interview.slot_filled",
					SessionID:   sessionID,
					SlotsFilled: int(v.SaturationUpdate.SlotsFilled),
					SlotsTotal:  int(v.SaturationUpdate.SlotsTotal),
					Saturation:  v.SaturationUpdate.Saturation,
				}
			}
		case *gilv1.InterviewEvent_AdversaryFindings:
			if v.AdversaryFindings != nil {
				g.eventCh <- TrackerInput{
					Kind:        "interview.adversary",
					SessionID:   sessionID,
					AdvFindings: int(v.AdversaryFindings.Count),
				}
			}
		case *gilv1.InterviewEvent_Stage:
			if v.Stage != nil && v.Stage.To == "ready_to_freeze" {
				g.eventCh <- TrackerInput{
					Kind:      "interview.ready_to_freeze",
					SessionID: sessionID,
				}
			}
		case *gilv1.InterviewEvent_SpecUpdate:
			// SpecUpdate events are informational; no TrackerInput mapping.
		case *gilv1.InterviewEvent_Error:
			// Error events surface at stream close via deferred done.
		}
	}
}

// startTailLoop launches a background goroutine that subscribes to run
// events via RunService.Tail and forwards them as TrackerInput to eventCh.
// The goroutine runs until the ctx is cancelled or the stream ends.
func (g *GRPCClient) startTailLoop(ctx context.Context) {
	go func() {
		stream, err := g.sdk.TailRun(ctx, g.activeSess)
		if err != nil {
			return
		}
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			in := mapRunEventToTracker(g.activeSess, ev)
			if in.Kind != "" {
				select {
				case g.eventCh <- in:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// NextAssistantChunk returns the next text chunk from the current
// interview turn. The caller pulls in a loop until more==false.
//
// Contract:
//   - A chunk received from chunkCh always returns more=true. The caller
//     keeps pulling.
//   - When chunkDone is closed AND chunkCh is drained, NextAssistantChunk
//     returns ("", false, nil) — the turn is over.
//   - On context cancellation, returns ("", false, ctx.Err()).
func (g *GRPCClient) NextAssistantChunk(ctx context.Context) (string, bool, error) {
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case chunk := <-g.chunkCh:
		// We don't know if more chunks are coming; the producer signals
		// completion by closing chunkDone, not by emptying chunkCh.
		return chunk, true, nil
	case <-g.chunkDone:
		// Producer is done. Drain any chunk that arrived between the
		// last receive and the close, then surface any captured stream
		// error so chat REPL can render it instead of silently looping.
		select {
		case chunk := <-g.chunkCh:
			return chunk, true, nil
		default:
		}
		// Reset the in-interview flag on transport-level failure so the
		// next prompt can rebuild a fresh stream.
		g.streamErrMu.Lock()
		serr := g.streamErr
		g.streamErr = nil
		g.streamErrMu.Unlock()
		if serr != nil {
			g.inInterview = false
		}
		return "", false, serr
	}
}

// NextEvent returns the next pending TrackerInput event without blocking.
// Returns (zero, false, nil) when the eventCh is empty.
func (g *GRPCClient) NextEvent(ctx context.Context) (TrackerInput, bool, error) {
	select {
	case <-ctx.Done():
		return TrackerInput{}, false, ctx.Err()
	case ev := <-g.eventCh:
		return ev, true, nil
	default:
		return TrackerInput{}, false, nil
	}
}

// Spec returns the current spec for the active session as a SpecView.
// The spec content is marshalled from the FrozenSpec proto to JSON
// (YAML marshalling is not available without an extra dependency; the
// render.SpecView.YAML field receives JSON-formatted content).
func (g *GRPCClient) Spec(ctx context.Context) (*render.SpecView, error) {
	fs, err := g.sdk.GetSpec(ctx, g.activeSess)
	if err != nil {
		return nil, err
	}
	m := protojson.MarshalOptions{
		Multiline:       true,
		Indent:          "  ",
		EmitUnpopulated: false,
	}
	jsonBytes, err := m.Marshal(fs)
	if err != nil {
		return nil, fmt.Errorf("spec marshal: %w", err)
	}
	return &render.SpecView{YAML: string(jsonBytes)}, nil
}

// Status returns a one-line status string for the active session.
func (g *GRPCClient) Status(ctx context.Context) (string, error) {
	s, err := g.sdk.GetSession(ctx, g.activeSess)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s · %s · iter %d · $%.4f",
		shortID(s.ID), s.Status, s.CurrentIteration, s.TotalCostUSD), nil
}

// Diff returns the workspace diff for the active session. The SDK returns a
// unified diff string; it is mapped to a single DiffHunk with the raw diff
// in the Snippet field.
func (g *GRPCClient) Diff(ctx context.Context) ([]render.DiffHunk, error) {
	result, err := g.sdk.Diff(ctx, g.activeSess)
	if err != nil {
		return nil, err
	}
	if result.UnifiedDiff == "" {
		return nil, nil
	}
	return []render.DiffHunk{{
		Path:    "(unified)",
		Added:   int(result.LinesAdded),
		Removed: int(result.LinesRemoved),
		Snippet: result.UnifiedDiff,
	}}, nil
}

// Merge is not implemented in the current SDK. It returns an error indicating
// the user should apply the diff externally.
//
// TODO(T16): wire to a server-side apply endpoint when available.
func (g *GRPCClient) Merge(_ context.Context) error {
	return fmt.Errorf("merge is not yet supported by the server; apply the diff manually or use `git apply`")
}

// StartRun starts the agent run for the active session in detached mode.
// A background tail loop is launched to forward run events to the eventCh.
func (g *GRPCClient) StartRun(ctx context.Context) error {
	_, err := g.sdk.StartRun(ctx, g.activeSess, "", "", true /* detach */)
	if err != nil {
		return err
	}
	g.startTailLoop(ctx)
	return nil
}

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
	case "run.stuck":
		in.Kind = "run.stuck"
	case "run.done":
		in.Kind = "run.done"
		if ev.GetMetrics() != nil {
			in.CostUSD = ev.GetMetrics().CostUsd
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
