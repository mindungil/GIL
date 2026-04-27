package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/notify"
	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// makeClarifySpec is a near-twin of makeFrozenSpec but it uses FULL
// autonomy + a no-op verifier so the run never blocks on permission
// asks — leaving us free to exercise the clarify pause/resume path.
func makeClarifySpec(t *testing.T, sessionsBase, sessionID, workingDir string) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-spec-clarify",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner:               "ask the user a question",
			SuccessCriteriaNatural: []string{"clarify worked"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"clarify"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "noop", Kind: gilv1.CheckKind_SHELL, Command: "true"},
			},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE, Path: workingDir},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "mock", ModelId: "mock-model"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
		Budget:    &gilv1.Budget{MaxIterations: 5},
	}
	require.NoError(t, store.Save(fs))
	require.NoError(t, store.Freeze())
}

// TestRunService_Clarify_PauseResume drives the full server-side dance:
//
//  1. Mock provider calls clarify(question, suggestions) → tool fires.
//  2. AskClarifyCallback emits clarify_requested + registers a pending
//     channel + blocks.
//  3. Test goroutine reads the askID off the pending map.
//  4. AnswerClarification RPC unblocks the channel.
//  5. The clarify tool's Run returns the answer string as tool_result;
//     the agent's next mock turn ends the run.
func TestRunService_Clarify_PauseResume(t *testing.T) {
	workDir := t.TempDir()
	gilHome := t.TempDir()
	t.Setenv("GIL_HOME", gilHome)

	mockTurns := []provider.MockTurn{
		{
			Text: "asking the user",
			ToolCalls: []provider.ToolCall{{
				ID:    "c1",
				Name:  "clarify",
				Input: json.RawMessage(`{"question":"deploy now?","suggestions":["yes","no"],"urgency":"high"}`),
			}},
			StopReason: "tool_use",
		},
		{Text: "got the answer; done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	// Override the notifier to a no-op (default is stdout, which would
	// pollute the test output).
	svc.notifierFor = func(_, _ string) notify.Notifier { return nil }

	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeClarifySpec(t, sessionsBase, s.ID, workDir)

	type runRes struct {
		resp *gilv1.StartRunResponse
		err  error
	}
	resCh := make(chan runRes, 1)
	go func() {
		resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
		resCh <- runRes{resp, err}
	}()

	// Poll pendingClarifications until the callback registers our ask.
	var askID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		for id := range svc.pendingClarifications[s.ID] {
			askID = id
		}
		svc.mu.Unlock()
		if askID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, askID, "AskClarifyCallback should have registered a pending ask")

	// Verify PendingClarifications surfaces the askID.
	pending := svc.PendingClarifications(s.ID)
	require.Contains(t, pending, askID)

	// Send the answer.
	resp, err := svc.AnswerClarification(ctx, &gilv1.AnswerClarificationRequest{
		SessionId: s.ID,
		AskId:     askID,
		Answer:    "yes, deploy",
	})
	require.NoError(t, err)
	require.True(t, resp.Delivered)

	select {
	case r := <-resCh:
		require.NoError(t, r.err)
		require.Equal(t, "done", r.resp.Status)
	case <-time.After(15 * time.Second):
		t.Fatal("run did not complete after AnswerClarification")
	}

	// PendingClarifications should now be empty (the deferred cleanup
	// in makeClarifyCallback removes the entry once the channel
	// returns).
	require.Empty(t, svc.PendingClarifications(s.ID),
		"pending entry should clear after the runner picks up the answer")
}

// TestRunService_AnswerClarification_StaleAskID confirms the
// race-tolerant shape: an unknown ask_id returns delivered=false
// without an error so the surface can render a friendly message.
func TestRunService_AnswerClarification_StaleAskID(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	resp, err := svc.AnswerClarification(context.Background(), &gilv1.AnswerClarificationRequest{
		SessionId: "no-such-session",
		AskId:     "no-such-ask",
		Answer:    "x",
	})
	require.NoError(t, err)
	require.False(t, resp.Delivered)
}

// TestRunService_AnswerClarification_DoubleAnswer_Deduplicates pins
// the contract that a second AnswerClarification for the same ask
// returns delivered=false (channel buffer=1; the first answer wins).
func TestRunService_AnswerClarification_DoubleAnswer_Deduplicates(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	sessID := "race-sess"
	askID := "race-ask"

	ch := make(chan string, 1)
	svc.mu.Lock()
	svc.pendingClarifications[sessID] = map[string]*pendingClarify{askID: {ch: ch}}
	svc.mu.Unlock()

	first, err := svc.AnswerClarification(context.Background(), &gilv1.AnswerClarificationRequest{
		SessionId: sessID, AskId: askID, Answer: "first",
	})
	require.NoError(t, err)
	require.True(t, first.Delivered)

	second, err := svc.AnswerClarification(context.Background(), &gilv1.AnswerClarificationRequest{
		SessionId: sessID, AskId: askID, Answer: "second",
	})
	require.NoError(t, err)
	require.False(t, second.Delivered, "second answer should be deduplicated")

	// Channel should hold the first answer.
	got := <-ch
	require.Equal(t, "first", got)
}

// TestRunService_Clarify_NotifierIsCalled wires a stub notifier and
// verifies it receives the urgency-tagged Notification when the
// callback emits clarify_requested. Mirrors the e2e fan-out test but
// stays hermetic.
func TestRunService_Clarify_NotifierIsCalled(t *testing.T) {
	workDir := t.TempDir()
	gilHome := t.TempDir()
	t.Setenv("GIL_HOME", gilHome)

	mockTurns := []provider.MockTurn{
		{
			Text: "asking",
			ToolCalls: []provider.ToolCall{{
				ID:    "c1",
				Name:  "clarify",
				Input: json.RawMessage(`{"question":"why?","urgency":"high"}`),
			}},
			StopReason: "tool_use",
		},
		{Text: "done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)

	gotCh := make(chan notify.Notification, 1)
	stub := &stubNotifier{out: gotCh}
	svc.notifierFor = func(_, _ string) notify.Notifier { return stub }

	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeClarifySpec(t, sessionsBase, s.ID, workDir)

	go func() {
		_, _ = svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	}()

	// Wait for the notifier to fire AND the pending entry to register.
	var n notify.Notification
	select {
	case n = <-gotCh:
	case <-time.After(10 * time.Second):
		t.Fatal("notifier never received the clarify ask")
	}
	require.Equal(t, "high", n.Urgency)
	require.Equal(t, "why?", n.Body)
	require.NotEmpty(t, n.AskID)

	// Wait until the pending channel is registered, then unblock the run.
	var askID string
	for time.Now().Before(time.Now().Add(10 * time.Second)) {
		svc.mu.Lock()
		for id := range svc.pendingClarifications[s.ID] {
			askID = id
		}
		svc.mu.Unlock()
		if askID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEmpty(t, askID)
	_, _ = svc.AnswerClarification(ctx, &gilv1.AnswerClarificationRequest{
		SessionId: s.ID, AskId: askID, Answer: "ok",
	})
}

// stubNotifier captures one Notify call into a buffered channel for
// tests. Implements notify.Notifier.
type stubNotifier struct {
	out chan notify.Notification
}

func (s *stubNotifier) Notify(ctx context.Context, n notify.Notification) error {
	select {
	case s.out <- n:
	default:
		// drop on full channel — tests pass a buffered chan
	}
	return nil
}
