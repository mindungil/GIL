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
	sessionID       string
	spec            *render.SpecView
	statusText      string
	diffHunks       []render.DiffHunk
	merged          bool
	runStarted      bool
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
func (f *fakeClient) Close() error                                                { return nil }
func (f *fakeClient) ActiveSessionID() string                                     { return f.sessionID }
func (f *fakeClient) Spec(_ context.Context) (*render.SpecView, error)            { return f.spec, nil }
func (f *fakeClient) Status(_ context.Context) (string, error)                    { return f.statusText, nil }
func (f *fakeClient) Diff(_ context.Context) ([]render.DiffHunk, error)           { return f.diffHunks, nil }
func (f *fakeClient) Merge(_ context.Context) error                               { f.merged = true; return nil }
func (f *fakeClient) StartRun(_ context.Context) error                            { f.runStarted = true; return nil }
func (f *fakeClient) ListSessions(_ context.Context) ([]SessionSummary, error)    { return f.sessionList, nil }
func (f *fakeClient) SwitchSession(_ context.Context, _ string) error             { f.sessionID = "switched"; return nil }
func (f *fakeClient) NewSession(_ context.Context) error                          { f.sessionID = "new"; return nil }

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

func TestLoop_SlashSpec_RendersSpec(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{
		spec:      &render.SpecView{YAML: "goal:\n  one_liner: x\n"},
		sessionID: "01HQ",
	}
	in := strings.NewReader("/spec\n/quit\n")
	require.NoError(t, Run(context.Background(), Config{
		In: in, Renderer: mock, Client: fc,
	}))
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "Spec" && c.Spec != nil && strings.Contains(c.Spec.YAML, "one_liner") {
			found = true
		}
	}
	require.True(t, found)
}

func TestLoop_SlashDiff_RendersHunks(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{
		diffHunks: []render.DiffHunk{{Path: "a.go", Added: 3, Removed: 1}},
		sessionID: "01HQ",
	}
	in := strings.NewReader("/diff\n/quit\n")
	require.NoError(t, Run(context.Background(), Config{
		In: in, Renderer: mock, Client: fc,
	}))
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "Diff" && len(c.Hunks) == 1 && c.Hunks[0].Path == "a.go" {
			found = true
		}
	}
	require.True(t, found)
}

func TestLoop_SlashMerge_PromptsConfirm(t *testing.T) {
	mock := render.NewMockRenderer()
	mock.ConfirmAnswers = []bool{true}
	fc := &fakeClient{sessionID: "01HQ"}
	in := strings.NewReader("/merge\n/quit\n")
	require.NoError(t, Run(context.Background(), Config{
		In: in, Renderer: mock, Client: fc,
	}))
	var foundConfirm bool
	for _, c := range mock.Calls {
		if c.Method == "Confirm" && strings.Contains(c.Text, "Apply") {
			foundConfirm = true
		}
	}
	require.True(t, foundConfirm)
	require.True(t, fc.merged, "client.Merge should have been called after Y")
}

func TestLoop_SlashRun_RequiresAwaitingConfirmPhase(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{sessionID: "01HQ"}
	in := strings.NewReader("/run\n/quit\n")
	require.NoError(t, Run(context.Background(), Config{
		In: in, Renderer: mock, Client: fc,
	}))
	// Phase is idle (no awaiting-confirm event), so /run should refuse.
	var found bool
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "spec is not ready") {
			found = true
		}
	}
	require.True(t, found)
	require.False(t, fc.runStarted)
}

func TestLoop_SlashSessions_ShortID_NoPanic(t *testing.T) {
	mock := render.NewMockRenderer()
	fc := &fakeClient{
		sessionID: "01HQ",
		sessionList: []SessionSummary{
			{ID: "ab", Name: "tiny", Phase: "idle"},     // 2 chars
			{ID: "01HQXY", Name: "exact", Phase: "run"}, // 6 chars
			{ID: "01HQXYZ123", Name: "long", Phase: "done"},
		},
	}
	in := strings.NewReader("/sessions\n/quit\n")
	require.NoError(t, Run(context.Background(), Config{
		In: in, Renderer: mock, Client: fc,
	}))
	// Expect 3 SystemNote calls listing the sessions; the panic-free run
	// is the main assertion. Spot-check that the short ID appears verbatim
	// (no truncation, no padding).
	var lines []string
	for _, c := range mock.Calls {
		if c.Method == "SystemNote" && strings.Contains(c.Text, "tiny") {
			lines = append(lines, c.Text)
		}
	}
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], " ab ", "short ID should appear verbatim")
}
