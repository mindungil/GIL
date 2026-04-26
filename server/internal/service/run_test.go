package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/specstore"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func newRunSvc(t *testing.T, mockTurns []provider.MockTurn) (*RunService, *session.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, session.Migrate(db))
	repo := session.NewRepo(db)

	factory := func(name string) (provider.Provider, string, error) {
		return provider.NewMockToolProvider(mockTurns), "mock-model", nil
	}
	sessionsBase := filepath.Join(dir, "sessions")
	return NewRunService(repo, sessionsBase, factory), repo, sessionsBase
}

func makeFrozenSpec(t *testing.T, sessionsBase, sessionID, workingDir string) {
	t.Helper()
	store := specstore.NewStore(filepath.Join(sessionsBase, sessionID))
	fs := &gilv1.FrozenSpec{
		SpecId:    "test-spec",
		SessionId: sessionID,
		Goal: &gilv1.Goal{
			OneLiner:               "create hello.txt",
			SuccessCriteriaNatural: []string{"file exists", "contains hello", "no other files"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"bash"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{
				{Name: "exists", Kind: gilv1.CheckKind_SHELL, Command: "test -f hello.txt"},
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

func TestRunService_Start_HelloTxt_Done(t *testing.T) {
	workDir := t.TempDir()

	mockTurns := []provider.MockTurn{
		{Text: "Creating", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hello"}`)},
		}, StopReason: "tool_use"},
		{Text: "Done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.NoError(t, err)
	require.Equal(t, "done", resp.Status)
	require.Equal(t, int32(2), resp.Iterations)
	require.Len(t, resp.VerifyResults, 1)
	require.True(t, resp.VerifyResults[0].Passed)

	got, _ := repo.Get(ctx, s.ID)
	require.Equal(t, "done", got.Status)
}

func TestRunService_Start_NotFrozen_FailsPrecondition(t *testing.T) {
	svc, repo, _ := newRunSvc(t, nil)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: t.TempDir()})
	require.NoError(t, err)

	_, err = svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "frozen")
}

func TestRunService_Start_NotFound(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	_, err := svc.Start(context.Background(), &gilv1.StartRunRequest{SessionId: "nope", Provider: "mock"})
	require.Error(t, err)
}

func TestRunService_Start_PersistsEventsToDisk(t *testing.T) {
	workDir := t.TempDir()

	mockTurns := []provider.MockTurn{
		{Text: "Creating", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hi"}`)},
		}, StopReason: "tool_use"},
		{Text: "Done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	ctx := context.Background()
	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{SessionId: s.ID, Provider: "mock"})
	require.NoError(t, err)
	require.Equal(t, "done", resp.Status)

	// Verify events.jsonl exists and contains events
	eventsPath := filepath.Join(sessionsBase, s.ID, "events", "events.jsonl")
	require.FileExists(t, eventsPath)

	// Load and verify event count > 0
	loaded, err := event.LoadAll(eventsPath)
	require.NoError(t, err)
	require.NotEmpty(t, loaded)

	// Should contain at least an iteration_start and run_done
	types := map[string]int{}
	for _, e := range loaded {
		types[e.Type]++
	}
	require.Greater(t, types["iteration_start"], 0, "got types: %+v", types)
	require.Greater(t, types["run_done"], 0)
}

func TestRunService_Tail_StreamsEvents(t *testing.T) {
	workDir := t.TempDir()
	mockTurns := []provider.MockTurn{
		{Text: "Creating", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"hello.txt","content":"hi"}`)},
		}, StopReason: "tool_use"},
		{Text: "Done", StopReason: "end_turn"},
	}

	svc, repo, sessionsBase := newRunSvc(t, mockTurns)
	ctx := context.Background()
	sess, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, sess.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, sess.ID, workDir)

	// Create a stream and register it
	stream := event.NewStream()
	svc.mu.Lock()
	svc.runStreams[sess.ID] = stream
	svc.mu.Unlock()
	defer func() {
		svc.mu.Lock()
		delete(svc.runStreams, sess.ID)
		svc.mu.Unlock()
	}()

	// Spin up gRPC server with bufconn
	lis := bufconn.Listen(1024 * 1024)
	g := grpc.NewServer()
	gilv1.RegisterRunServiceServer(g, svc)
	go g.Serve(lis)
	defer g.Stop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(c context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(c)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	client := gilv1.NewRunServiceClient(conn)

	// Start Tail in background
	tailDone := make(chan error, 1)
	go func() {
		tailCtx, tailCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer tailCancel()
		tail, err := client.Tail(tailCtx, &gilv1.TailRequest{SessionId: sess.ID})
		if err != nil {
			tailDone <- err
			return
		}

		// Collect events
		received := 0
		for {
			evt, err := tail.Recv()
			if err != nil {
				tailDone <- nil
				break
			}
			received++
			require.Equal(t, "test_event", evt.GetType())
			require.Equal(t, gilv1.EventSource_SYSTEM, evt.GetSource())
			require.Equal(t, gilv1.EventKind_NOTE, evt.GetKind())
			if received >= 3 {
				tailDone <- nil
				break
			}
		}
	}()

	// Give Tail time to subscribe
	time.Sleep(100 * time.Millisecond)

	// Send test events AFTER Tail has subscribed
	for i := 0; i < 3; i++ {
		_, _ = stream.Append(event.Event{
			Timestamp: time.Now(),
			Source:    event.SourceSystem,
			Kind:      event.KindNote,
			Type:      "test_event",
			Data:      []byte(`{}`),
		})
	}

	err = <-tailDone
	require.NoError(t, err)
}

func TestRunService_Tail_NotFoundForInactive(t *testing.T) {
	svc, _, _ := newRunSvc(t, nil)
	lis := bufconn.Listen(1024 * 1024)
	g := grpc.NewServer()
	gilv1.RegisterRunServiceServer(g, svc)
	go g.Serve(lis)
	defer g.Stop()
	conn, _ := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(c context.Context, _ string) (net.Conn, error) { return lis.DialContext(c) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	defer conn.Close()
	client := gilv1.NewRunServiceClient(conn)

	tail, err := client.Tail(context.Background(), &gilv1.TailRequest{SessionId: "nope"})
	require.NoError(t, err)
	_, err = tail.Recv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active run")
}
