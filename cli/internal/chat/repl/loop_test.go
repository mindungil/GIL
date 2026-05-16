package repl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeClient simulates the M2-narrowed SessionClient. The chat surface
// only sends prompts and drains chunks/events now — verb dispatch is
// gone (the agent calls tools server-side).
type fakeClient struct {
	assistantChunks []string
	events          []TrackerInput
	sentPrompts     []string
	eventErr        error
	sessionID       string
	sessionList     []SessionSummary
}

func (f *fakeClient) SendPrompt(_ context.Context, prompt string) error {
	f.sentPrompts = append(f.sentPrompts, prompt)
	return nil
}
func (f *fakeClient) NextAssistantChunk(_ context.Context) (string, bool, error) {
	if len(f.assistantChunks) == 0 {
		return "", false, nil
	}
	c := f.assistantChunks[0]
	f.assistantChunks = f.assistantChunks[1:]
	return c, len(f.assistantChunks) > 0, nil
}
func (f *fakeClient) NextEvent(_ context.Context) (TrackerInput, bool, error) {
	if f.eventErr != nil {
		err := f.eventErr
		f.eventErr = nil
		return TrackerInput{}, false, err
	}
	if len(f.events) == 0 {
		return TrackerInput{}, false, nil
	}
	e := f.events[0]
	f.events = f.events[1:]
	return e, true, nil
}
func (f *fakeClient) Close() error                                             { return nil }
func (f *fakeClient) ActiveSessionID() string                                  { return f.sessionID }
func (f *fakeClient) ListSessions(_ context.Context) ([]SessionSummary, error) { return f.sessionList, nil }

func TestLoop_BarePrompt_SendsAndRendersAssistant(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{assistantChunks: []string{"hello ", "world"}}
	in := strings.NewReader("hi there\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	require.Equal(t, []string{"hi there"}, fc.sentPrompts)
	seq := mock.MethodSequence()
	require.Contains(t, seq, "AssistantText")
	require.Contains(t, seq, "PromptCue")
}

func TestLoop_TerminalExit_BareWordExits(t *testing.T) {
	for _, word := range []string{"quit", "exit", "bye", "QUIT", "/quit", "/exit"} {
		mock := render.NewMockRenderer()
		fc := &fakeClient{}
		in := strings.NewReader(word + "\n")
		err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
		require.NoError(t, err, "exit word %q should clean-quit", word)
		require.Empty(t, fc.sentPrompts, "exit word %q must not be sent to the daemon", word)
	}
}

func TestLoop_NL_AlwaysForwards_NoSlashDispatch(t *testing.T) {
	// M2: every non-exit input forwards to the daemon's agent. Phrases
	// that USED to be classified client-side (slash commands, verb
	// patterns) now flow through verbatim — the agent's tools handle
	// the actual verb work.
	for _, phrase := range []string{
		"show me the diff",
		"/diff",
		"merge it",
		"/sessions",
		"what's the spec",
		"안녕",
		"한국어 입력",
	} {
		mock := render.NewMockRenderer()
		fc := &fakeClient{assistantChunks: []string{"ok"}}
		in := strings.NewReader(phrase + "\nquit\n")
		err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
		require.NoError(t, err)
		require.Equal(t, []string{phrase}, fc.sentPrompts,
			"phrase %q must forward verbatim", phrase)
	}
}

func TestLoop_StatusStripOnlyRedrawsOnPhaseChange(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{assistantChunks: []string{"hello"}}
	in := strings.NewReader("hi\n\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	stripCount := 0
	for _, c := range mock.Calls {
		if c.Method == "StatusStrip" {
			stripCount++
		}
	}
	require.Equal(t, 1, stripCount,
		"strip should paint only on phase changes; got %d paints", stripCount)
}

func TestLoop_ContextCancelled_ReturnsErr(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, Config{In: strings.NewReader("hello\n"), Renderer: mock, Client: fc})
	require.True(t, errors.Is(err, context.Canceled))
}

// L2 inline-drain regression: tool.call / tool.result events arrive
// on a sibling channel but must render INTERLEAVED with the assistant
// text, not batched at end-of-turn. Pre-fix, the streaming loop
// drained chunks first and only handled events between turns.
func TestLoop_ToolEventsInterleaveWithAssistantText(t *testing.T) {
	mock := render.NewMockRenderer()
	// fakeClient pulls both queues round-robin per loop iteration:
	// drainPendingEvents fires first, then NextAssistantChunk.
	// So with assistantChunks=[A, B, C] and events=[e1, e2], the
	// rendered sequence is: e1, e2, A, B, C (events are returned
	// before any chunk asked for).
	fc := &fakeClient{
		assistantChunks: []string{"hello ", "world"},
		events: []TrackerInput{
			{Kind: "tool.call", ToolName: "read_file", ToolInput: `{"path":"x"}`},
			{Kind: "tool.result", ToolContent: "ok"},
		},
	}
	in := strings.NewReader("hi\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	seq := mock.MethodSequence()
	// Find the indices of the first SystemNote (from event drain) and
	// the first AssistantText. SystemNote should come BEFORE
	// AssistantText now that drainPendingEvents runs at the top of
	// each streaming iteration.
	firstSystemNote := indexOf(seq, "SystemNote")
	firstAssistant := indexOf(seq, "AssistantText")
	require.NotEqual(t, -1, firstSystemNote, "expected at least one SystemNote from tool event")
	require.NotEqual(t, -1, firstAssistant, "expected at least one AssistantText")
	require.Less(t, firstSystemNote, firstAssistant,
		"tool events must render BEFORE assistant text when they arrive first; "+
			"got sequence %v", seq)
}

func indexOf(s []string, needle string) int {
	for i, v := range s {
		if v == needle {
			return i
		}
	}
	return -1
}
