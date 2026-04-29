package repl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/cli/internal/chat/render"
)

// fakeClient simulates the SessionClient interface. Tests pre-load
// AssistantText chunks the loop should emit and event sequences the
// tracker should apply.
type fakeClient struct {
	assistantChunks []string
	events          []TrackerInput
	sentPrompts     []string
	eventErr        error
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
		f.eventErr = nil // fire once to avoid infinite loop
		return TrackerInput{}, false, err
	}
	if len(f.events) == 0 {
		return TrackerInput{}, false, nil
	}
	e := f.events[0]
	f.events = f.events[1:]
	return e, true, nil
}
func (f *fakeClient) Close() error { return nil }

func TestLoop_BarePrompt_SendsAndRendersAssistant(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{
		assistantChunks: []string{"hello ", "world"},
	}
	in := strings.NewReader("hi there\n/quit\n")

	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   fc,
	})
	require.NoError(t, err)

	require.Equal(t, []string{"hi there"}, fc.sentPrompts)
	seq := mock.MethodSequence()
	require.Contains(t, seq, "AssistantText")
	require.Contains(t, seq, "PromptCue")
}

func TestLoop_SlashHelp_RoutesToHelp(t *testing.T) {
	mock := render.NewMockRenderer()
	in := strings.NewReader("/help\n/quit\n")
	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   &fakeClient{},
	})
	require.NoError(t, err)
	// /help should emit at least one SystemNote listing commands.
	var foundHelp bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "/sessions") {
			foundHelp = true
			break
		}
	}
	require.True(t, foundHelp, "expected /help to emit a SystemNote listing slash commands")
}

func TestLoop_UnknownSlash_EmitsHint(t *testing.T) {
	mock := render.NewMockRenderer()
	in := strings.NewReader("/bogus\n/quit\n")
	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   &fakeClient{},
	})
	require.NoError(t, err)
	var foundHint bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "unknown") {
			foundHint = true
			break
		}
	}
	require.True(t, foundHint)
}

func TestLoop_SessionScopedSlashWithoutSession_Errors(t *testing.T) {
	mock := render.NewMockRenderer()
	in := strings.NewReader("/spec\n/quit\n")
	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   &fakeClient{},
	})
	require.NoError(t, err)
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "no active session") {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestLoop_ContextCancelled_ReturnsErr(t *testing.T) {
	mock := render.NewMockRenderer()
	in := strings.NewReader("hi\n/quit\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run starts

	err := Run(ctx, Config{
		In:       in,
		Renderer: mock,
		Client:   &fakeClient{},
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestLoop_EventStreamError_EmitsNote(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{eventErr: errors.New("boom")}
	in := strings.NewReader("/quit\n")

	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   fc,
	})
	require.NoError(t, err)
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "event stream error") {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestLoop_DrainEvents_SlotFilled_EmitsSpecNote(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{
		events: []TrackerInput{{
			Kind:        "interview.slot_filled",
			SessionID:   "sess-1",
			SlotsFilled: 3,
			SlotsTotal:  7,
			Saturation:  0.42,
		}},
	}
	in := strings.NewReader("/quit\n")

	err := Run(context.Background(), Config{
		In:       in,
		Renderer: mock,
		Client:   fc,
	})
	require.NoError(t, err)
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "slot filled (3/7") {
			found = true
			break
		}
	}
	require.True(t, found)
}
