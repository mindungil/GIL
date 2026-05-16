package repl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeClient simulates the M2-narrowed SessionClient. iter14a: drains
// a single ordered Message queue (text + events interleaved by the
// test) so order assertions match the production wire-order contract.
type fakeClient struct {
	messages    []Message
	sentPrompts []string
	streamErr   error
	sessionID   string
	sessionList []SessionSummary
}

// queueChunks is a convenience helper for tests that previously passed
// assistantChunks=[a,b,c]: equivalent to appending three text Messages.
func (f *fakeClient) queueChunks(chunks ...string) {
	for _, c := range chunks {
		f.messages = append(f.messages, Message{Kind: "text", Text: c})
	}
}

// queueEvents appends events at the end of the message queue (used by
// tests that previously set events: ...).
func (f *fakeClient) queueEvents(evs ...TrackerInput) {
	for _, e := range evs {
		f.messages = append(f.messages, Message{Kind: "event", Event: e})
	}
}

func (f *fakeClient) SendPrompt(_ context.Context, prompt string) error {
	f.sentPrompts = append(f.sentPrompts, prompt)
	return nil
}

func (f *fakeClient) NextMessage(_ context.Context) (Message, bool, error) {
	if f.streamErr != nil {
		err := f.streamErr
		f.streamErr = nil
		return Message{}, false, err
	}
	if len(f.messages) == 0 {
		return Message{}, false, nil
	}
	m := f.messages[0]
	f.messages = f.messages[1:]
	return m, true, nil
}

func (f *fakeClient) Close() error                                             { return nil }
func (f *fakeClient) ActiveSessionID() string                                  { return f.sessionID }
func (f *fakeClient) ListSessions(_ context.Context) ([]SessionSummary, error) { return f.sessionList, nil }

func TestLoop_BarePrompt_SendsAndRendersAssistant(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{}
	fc.queueChunks("hello ", "world")
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
		fc := &fakeClient{}
		fc.queueChunks("ok")
		in := strings.NewReader(phrase + "\nquit\n")
		err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
		require.NoError(t, err)
		require.Equal(t, []string{phrase}, fc.sentPrompts,
			"phrase %q must forward verbatim", phrase)
	}
}

func TestLoop_StatusStripOnlyRedrawsOnPhaseChange(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{}
	fc.queueChunks("hello")
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

// L2 inline-drain regression (now iter14a-strengthened): tool events
// must render in WIRE order with assistant text. With unified msgCh,
// "in wire order" means literal queue order — events first when queued
// first, interleaved when interleaved.
func TestLoop_ToolEventsInterleaveWithAssistantText(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{}
	fc.queueEvents(
		TrackerInput{Kind: "tool.call", ToolName: "read_file", ToolInput: `{"path":"x"}`},
		TrackerInput{Kind: "tool.result", ToolContent: "ok"},
	)
	fc.queueChunks("hello ", "world")
	in := strings.NewReader("hi\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	seq := mock.MethodSequence()
	firstSystemNote := indexOf(seq, "SystemNote")
	firstAssistant := indexOf(seq, "AssistantText")
	require.NotEqual(t, -1, firstSystemNote)
	require.NotEqual(t, -1, firstAssistant)
	require.Less(t, firstSystemNote, firstAssistant,
		"events queued before text must render first; got sequence %v", seq)
}

// iter14a regression: wire order is preserved even when text appears
// BETWEEN two events. Pre-fix, drainPendingEvents drained all pending
// events before each NextAssistantChunk, which inverted this ordering
// (e1, e2, text1, text2 instead of text1, e1, text2, e2). With unified
// msgCh, the consumer pulls items strictly in producer order.
func TestLoop_PreservesWireOrder_TextBetweenEvents(t *testing.T) {
	mock := render.NewMockRenderer()
	// Non-empty sessionID skips the welcome disclosure SystemNote so we
	// only observe renders from the message queue under test.
	fc := &fakeClient{sessionID: "01TEST"}
	// Wire order: text1, event1, text2, event2.
	fc.messages = []Message{
		{Kind: "text", Text: "first "},
		{Kind: "event", Event: TrackerInput{
			Kind: "tool.call", ToolName: "read_file", ToolInput: `{"path":"a"}`,
		}},
		{Kind: "text", Text: "second"},
		{Kind: "event", Event: TrackerInput{
			Kind: "tool.result", ToolContent: "ok",
		}},
	}
	in := strings.NewReader("hi\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	seq := mock.MethodSequence()
	// Strip the framing methods we don't care about for ordering: keep
	// only AssistantText (text chunks) and SystemNote (event renders).
	var rendered []string
	for _, m := range seq {
		if m == "AssistantText" || m == "SystemNote" {
			rendered = append(rendered, m)
		}
	}
	// Pre-fix this would be [SystemNote, SystemNote, AssistantText,
	// AssistantText, AssistantText(\n)] — events drained first.
	// With unified msgCh, expect interleaved order matching the queue:
	// AssistantText("first "), SystemNote(event), AssistantText("second"),
	// SystemNote(event), plus the trailing AssistantText("\n") and
	// possibly an inline newline-separator AssistantText between text
	// and event.
	require.GreaterOrEqual(t, len(rendered), 4,
		"expected at least 4 rendered items; got %v", rendered)
	// First rendered item must be AssistantText (text1 came first on the wire).
	require.Equal(t, "AssistantText", rendered[0],
		"first item on wire was text; must render first. got %v", rendered)
	// First SystemNote must come AFTER the first AssistantText.
	firstNote := indexOf(rendered, "SystemNote")
	firstText := indexOf(rendered, "AssistantText")
	require.Less(t, firstText, firstNote,
		"text1 must render before event1; got %v", rendered)
}

// iter91a regression: tool.result body and tool.call input must be
// stripped of control chars before SystemNote. Pre-fix, an attacker-
// controlled file echoed via read_file's ToolContent could emit raw
// ESC sequences that repaint the terminal.
func TestLoop_ToolEventDisplay_StripsControlChars(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{sessionID: "01TEST"}
	fc.messages = []Message{
		{Kind: "event", Event: TrackerInput{
			Kind: "tool.call", ToolName: "read_file",
			ToolInput: "{\"path\":\"a\x1b[31mEVIL\x1b[0m\"}",
		}},
		{Kind: "event", Event: TrackerInput{
			Kind:        "tool.result",
			ToolContent: "ok\x1b[2K\x1b[1Aspoofed",
		}},
	}
	in := strings.NewReader("hi\nquit\n")
	err := Run(context.Background(), Config{In: in, Renderer: mock, Client: fc})
	require.NoError(t, err)
	for _, c := range mock.Calls {
		if c.Method != "SystemNote" {
			continue
		}
		require.NotContains(t, c.Text, "\x1b",
			"SystemNote must not carry ESC bytes; got %q", c.Text)
	}
}

func indexOf(s []string, needle string) int {
	for i, v := range s {
		if v == needle {
			return i
		}
	}
	return -1
}
