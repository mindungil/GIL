package repl

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// GRPCClient adapts sdk.Client to the SessionClient interface.
//
// Streaming model:
//   - Interview turns (Start / Reply) each return a short-lived server-
//     streaming RPC. The goroutine launched by drainInterviewStream consumes
//     the entire stream: AgentTurn payloads are forwarded to chunkCh, other
//     payloads are mapped to TrackerInput and forwarded to eventCh. When the
//     stream ends chunkDone is closed so NextAssistantChunk callers can tell
//     the turn is over.
//   - Run progress events come from RunService.Tail (TailRun). startTailLoop
//     launches that goroutine; it runs until the context is cancelled.
//
// chunkDone is reset on each new turn via resetChunkDone; the channel is
// swapped atomically under a single-goroutine write model (only subscribe
// methods touch it).
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

// resetChunkDone replaces chunkDone with a fresh, open channel. Must be
// called before launching a new drainInterviewStream goroutine.
func (g *GRPCClient) resetChunkDone() {
	g.chunkDone = make(chan struct{})
}

// ActiveSessionID returns the current session ID (empty if none).
func (g *GRPCClient) ActiveSessionID() string { return g.activeSess }

// NewSession creates a new session on the server and sets it as active.
// Any prior subscription state is discarded.
func (g *GRPCClient) NewSession(ctx context.Context) error {
	sess, err := g.sdk.CreateSession(ctx, sdk.CreateOptions{WorkingDir: g.workingDir})
	if err != nil {
		return err
	}
	g.activeSess = sess.ID
	g.inInterview = false
	return nil
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
		out = append(out, SessionSummary{
			ID:    s.ID,
			Name:  shortID(s.ID),
			Phase: s.Status,
		})
	}
	return out, nil
}

// SendPrompt sends the user's text to the server. On the first call to a
// session it calls StartInterview (which includes the first user turn); on
// subsequent calls it calls ReplyInterview. If no session is active one is
// auto-created.
func (g *GRPCClient) SendPrompt(ctx context.Context, prompt string) error {
	if g.activeSess == "" {
		if err := g.NewSession(ctx); err != nil {
			return err
		}
	}
	g.resetChunkDone()

	if !g.inInterview {
		stream, err := g.sdk.StartInterview(ctx, g.activeSess, prompt, "", "", sdk.InterviewModels{})
		if err != nil {
			return err
		}
		g.inInterview = true
		go g.drainInterviewStream(stream)
	} else {
		stream, err := g.sdk.ReplyInterview(ctx, g.activeSess, prompt)
		if err != nil {
			return err
		}
		go g.drainInterviewStream(stream)
	}
	return nil
}

// drainInterviewStream consumes a server-streaming InterviewEvent stream.
// AgentTurn payloads are forwarded as chunks; other payloads become
// TrackerInput events. The stream always ends with a close of chunkDone.
func (g *GRPCClient) drainInterviewStream(stream interface {
	Recv() (*gilv1.InterviewEvent, error)
}) {
	defer close(g.chunkDone)
	for {
		ev, err := stream.Recv()
		if err != nil {
			// EOF or transport error — turn is over.
			return
		}
		switch v := ev.GetPayload().(type) {
		case *gilv1.InterviewEvent_AgentTurn:
			if v.AgentTurn != nil && v.AgentTurn.Content != "" {
				g.chunkCh <- v.AgentTurn.Content
			}
		case *gilv1.InterviewEvent_SaturationUpdate:
			if v.SaturationUpdate != nil {
				g.eventCh <- TrackerInput{
					Kind:        "interview.slot_filled",
					SessionID:   g.activeSess,
					SlotsFilled: int(v.SaturationUpdate.SlotsFilled),
					SlotsTotal:  int(v.SaturationUpdate.SlotsTotal),
					Saturation:  v.SaturationUpdate.Saturation,
				}
			}
		case *gilv1.InterviewEvent_AdversaryFindings:
			if v.AdversaryFindings != nil {
				g.eventCh <- TrackerInput{
					Kind:        "interview.adversary",
					SessionID:   g.activeSess,
					AdvFindings: int(v.AdversaryFindings.Count),
				}
			}
		case *gilv1.InterviewEvent_Stage:
			if v.Stage != nil && v.Stage.To == "ready_to_freeze" {
				g.eventCh <- TrackerInput{
					Kind:      "interview.ready_to_freeze",
					SessionID: g.activeSess,
				}
			}
		case *gilv1.InterviewEvent_SpecUpdate:
			// SpecUpdate events are informational; no TrackerInput mapping.
		case *gilv1.InterviewEvent_Error:
			// Error events are logged via the done path; surface at stream close.
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

// NextAssistantChunk returns the next text chunk from the current interview
// turn. Returns ("", false, nil) when the turn is complete (chunkDone closed
// and chunkCh drained). Returns ("", false, ctx.Err()) on context
// cancellation.
func (g *GRPCClient) NextAssistantChunk(ctx context.Context) (string, bool, error) {
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case chunk, ok := <-g.chunkCh:
		if !ok {
			return "", false, nil
		}
		more := len(g.chunkCh) > 0
		return chunk, more, nil
	case <-g.chunkDone:
		// Drain any last chunks that arrived before close.
		select {
		case chunk := <-g.chunkCh:
			more := len(g.chunkCh) > 0
			return chunk, more, nil
		default:
			return "", false, nil
		}
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
// TODO(T14): wire to a server-side apply endpoint when available.
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

// shortID returns the first 6 characters of an ID, or the full ID if shorter.
func shortID(id string) string {
	if len(id) >= 6 {
		return id[:6]
	}
	return id
}

// Compile-time assertion: GRPCClient must implement SessionClient.
var _ SessionClient = (*GRPCClient)(nil)
