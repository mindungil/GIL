package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/runner"
	"github.com/mindungil/gil/core/session"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// run_subagent_ask_test.go — S9 invariants: subagent's AskCallback
// resolves to root's pending queue + event stream so the user watching
// the root surface (RunService.Tail) sees the ask and AnswerPermission
// against the root id unblocks the child.

func TestSubagentAsk_RoutesToRootStreamAndPendingQueue(t *testing.T) {
	repo := newTestRepo(t)
	rs := NewRunService(repo, t.TempDir(), nil)

	// Build a parent-child pair.
	rootSess, err := repo.Create(context.Background(), session.CreateInput{
		WorkingDir: t.TempDir(),
		GoalHint:   "root",
	})
	require.NoError(t, err)
	childSess, err := repo.Create(context.Background(), session.CreateInput{
		WorkingDir:      rootSess.WorkingDir,
		GoalHint:        "child task",
		ParentSessionID: rootSess.ID,
		SubagentDepth:   1,
		SubagentLabel:   "explore-auth",
	})
	require.NoError(t, err)

	// Plant a stream for the root so the callback can emit there. The
	// child's stream is separate — confirms the routing pulls from
	// runStreams map, not the callback's stream argument.
	rootStream := event.NewStream()
	childStream := event.NewStream()
	rs.mu.Lock()
	rs.runStreams[rootSess.ID] = rootStream
	rs.mu.Unlock()

	// Subscribe to the root stream BEFORE the callback fires so the
	// event is captured.
	rootSub := rootStream.Subscribe(4)
	defer rootSub.Close()

	cb := rs.makeAskCallback(childSess.ID, childStream, nil)

	// Fire the callback in a goroutine — it blocks until AnswerPermission
	// or timeout. Capture the answer for the assertion.
	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- cb(context.Background(), runner.AskRequest{
			Tool: "bash",
			Key:  "rm -rf /important",
		})
	}()

	// The event lands on the root stream, not the child's. Pull one.
	var ev event.Event
	select {
	case ev = <-rootSub.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("permission_ask event not received on root stream within 2s")
	}
	require.Equal(t, "permission_ask", ev.Type)

	// Payload should include from_session_id + from_subagent_label so the
	// user knows which child is asking.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(ev.Data, &payload))
	require.Equal(t, childSess.ID, payload["from_session_id"])
	require.Equal(t, "explore-auth", payload["from_subagent_label"])

	reqID := payload["request_id"].(string)

	// AnswerPermission against the ROOT id (not child id) must unblock
	// the callback.
	answer := &gilv1.AnswerPermissionRequest{
		SessionId: rootSess.ID,
		RequestId: reqID,
		Allow:     true,
	}
	_, err = rs.AnswerPermission(context.Background(), answer)
	require.NoError(t, err)

	select {
	case ok := <-resultCh:
		require.True(t, ok, "child callback returned allow=true after root-keyed answer")
	case <-time.After(2 * time.Second):
		t.Fatal("child AskCallback did not unblock after AnswerPermission against root id")
	}
}

func TestRootSessionAsk_StaysOnOwnStream(t *testing.T) {
	repo := newTestRepo(t)
	rs := NewRunService(repo, t.TempDir(), nil)

	rootSess, err := repo.Create(context.Background(), session.CreateInput{
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)

	stream := event.NewStream()
	sub := stream.Subscribe(4)
	defer sub.Close()

	cb := rs.makeAskCallback(rootSess.ID, stream, nil)

	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- cb(context.Background(), runner.AskRequest{Tool: "x", Key: "y"})
	}()

	var ev event.Event
	select {
	case ev = <-sub.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("ask not on root's own stream")
	}
	require.Equal(t, "permission_ask", ev.Type)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(ev.Data, &payload))
	// Root session has no subagent label in payload (no routing applied).
	require.NotContains(t, payload, "from_subagent_label")
	require.NotContains(t, payload, "from_session_id")

	reqID := payload["request_id"].(string)
	_, _ = rs.AnswerPermission(context.Background(), &gilv1.AnswerPermissionRequest{
		SessionId: rootSess.ID,
		RequestId: reqID,
		Allow:     true,
	})

	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("root cb not unblocked")
	}
}
