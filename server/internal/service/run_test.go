package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/jedutools/gil/core/event"
	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	"github.com/jedutools/gil/core/specstore"
	"github.com/jedutools/gil/core/tool"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/runtime/local"
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

func TestRunService_Start_Detach_ReturnsStarted(t *testing.T) {
	workDir := t.TempDir()

	// Multi-iteration turns so progress tracking has time to record something.
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

	// Detach=true → should return immediately with Status="started".
	resp, err := svc.Start(ctx, &gilv1.StartRunRequest{
		SessionId: s.ID,
		Provider:  "mock",
		Detach:    true,
	})
	require.NoError(t, err)
	require.Equal(t, "started", resp.Status)

	// Progress should be tracked (eventually > 0 iterations) within 2 seconds.
	assert.Eventually(t, func() bool {
		iters, _, ok := svc.Progress(s.ID)
		return ok && iters > 0
	}, 2*time.Second, 10*time.Millisecond, "expected progress to be tracked for detached run")

	// Wait for the background goroutine to finish before TempDir cleanup fires.
	// Without this, the shadow git objects may still be open, causing cleanup errors.
	assert.Eventually(t, func() bool {
		sess, err := repo.Get(ctx, s.ID)
		return err == nil && sess.Status != "running"
	}, 5*time.Second, 20*time.Millisecond, "expected detached run to finish within 5s")
}

func TestBuildTools_LocalNative_NoSandbox(t *testing.T) {
	tools, err := buildTools("/work", &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_NATIVE})
	require.NoError(t, err)
	require.Len(t, tools, 4)

	bash, ok := tools[0].(*tool.Bash)
	require.True(t, ok, "first tool should be *tool.Bash")
	require.Nil(t, bash.Wrapper, "Wrapper should be nil for LOCAL_NATIVE")

	wf, ok := tools[1].(*tool.WriteFile)
	require.True(t, ok, "second tool should be *tool.WriteFile")
	require.False(t, wf.ReadOnly, "ReadOnly should be false for LOCAL_NATIVE")

	rm, ok := tools[3].(*tool.Repomap)
	require.True(t, ok, "fourth tool should be *tool.Repomap")
	require.Equal(t, "/work", rm.Root)
}

func TestBuildTools_Unspecified_DefaultsToLocalNative(t *testing.T) {
	// BACKEND_UNSPECIFIED should behave like LOCAL_NATIVE
	tools, err := buildTools("/work", &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_BACKEND_UNSPECIFIED})
	require.NoError(t, err)
	require.Len(t, tools, 4)
	bash, ok := tools[0].(*tool.Bash)
	require.True(t, ok)
	require.Nil(t, bash.Wrapper)

	// nil workspace should also behave like LOCAL_NATIVE
	tools2, err2 := buildTools("/work", nil)
	require.NoError(t, err2)
	require.Len(t, tools2, 4)
	bash2, ok2 := tools2[0].(*tool.Bash)
	require.True(t, ok2)
	require.Nil(t, bash2.Wrapper)
}

func TestBuildTools_LocalSandbox_Behavior(t *testing.T) {
	t.Run("withBwrap", func(t *testing.T) {
		if !local.Available() {
			t.Skip("bwrap not installed on this machine")
		}
		tools, err := buildTools("/work", &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX})
		require.NoError(t, err)
		require.Len(t, tools, 4)

		bash, ok := tools[0].(*tool.Bash)
		require.True(t, ok)
		require.NotNil(t, bash.Wrapper, "Wrapper should be set for LOCAL_SANDBOX")

		sb, ok := bash.Wrapper.(*local.Sandbox)
		require.True(t, ok, "Wrapper should be *local.Sandbox")
		require.Equal(t, "/work", sb.WorkspaceDir)
		require.Equal(t, local.ModeWorkspaceWrite, sb.Mode)
	})
	t.Run("withoutBwrap", func(t *testing.T) {
		if local.Available() {
			t.Skip("bwrap is installed; cannot test missing-bwrap path")
		}
		_, err := buildTools("/work", &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX})
		require.Error(t, err)
		require.Contains(t, err.Error(), "requires bwrap")
	})
}

func TestBuildTools_Docker_Returns_NotSupported(t *testing.T) {
	unsupported := []gilv1.WorkspaceBackend{
		gilv1.WorkspaceBackend_DOCKER,
		gilv1.WorkspaceBackend_SSH,
		gilv1.WorkspaceBackend_VM,
	}
	for _, backend := range unsupported {
		backend := backend
		t.Run(backend.String(), func(t *testing.T) {
			_, err := buildTools("/work", &gilv1.Workspace{Backend: backend})
			require.Error(t, err)
			require.Contains(t, err.Error(), "not yet supported")
		})
	}
}

func TestRunService_Restore_RollsBackWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	workDir := t.TempDir()
	svc, repo, sessionsBase := newRunSvc(t, nil)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	// Manually build a shadow with two commits.
	shadowBase := filepath.Join(sessionsBase, s.ID, "shadow")
	sg := checkpoint.New(workDir, shadowBase)
	require.NoError(t, sg.Init(ctx))

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("v1"), 0o644))
	_, err = sg.Commit(ctx, "iter 1")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("v2"), 0o644))
	_, err = sg.Commit(ctx, "iter 2")
	require.NoError(t, err)

	// Mark session frozen (not running) so Restore accepts it.
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))

	// Restore to step=1 (oldest) → file should be "v1".
	resp, err := svc.Restore(ctx, &gilv1.RestoreRequest{SessionId: s.ID, Step: 1})
	require.NoError(t, err)
	require.Equal(t, int32(2), resp.TotalCheckpoints)
	require.Equal(t, "iter 1", resp.CommitMessage)

	got, err := os.ReadFile(filepath.Join(workDir, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "v1", string(got))

	// Restore to step=-1 (latest) → file should be "v2".
	resp2, err := svc.Restore(ctx, &gilv1.RestoreRequest{SessionId: s.ID, Step: -1})
	require.NoError(t, err)
	require.Equal(t, int32(2), resp2.TotalCheckpoints)
	require.Equal(t, "iter 2", resp2.CommitMessage)

	got2, err := os.ReadFile(filepath.Join(workDir, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "v2", string(got2))
}

func TestRunService_Restore_RejectsRunning(t *testing.T) {
	svc, repo, _ := newRunSvc(t, nil)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "running"))

	_, err = svc.Restore(ctx, &gilv1.RestoreRequest{SessionId: s.ID, Step: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "FailedPrecondition")
}

func TestRunService_Restore_NoCheckpoints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	workDir := t.TempDir()
	svc, repo, sessionsBase := newRunSvc(t, nil)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	// Init shadow but commit nothing.
	shadowBase := filepath.Join(sessionsBase, s.ID, "shadow")
	sg := checkpoint.New(workDir, shadowBase)
	require.NoError(t, sg.Init(ctx))

	_, err = svc.Restore(ctx, &gilv1.RestoreRequest{SessionId: s.ID, Step: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no checkpoints")
}

func TestRunService_Restore_OutOfRange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	workDir := t.TempDir()
	svc, repo, sessionsBase := newRunSvc(t, nil)
	ctx := context.Background()

	s, err := repo.Create(ctx, session.CreateInput{WorkingDir: workDir})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(ctx, s.ID, "frozen"))
	makeFrozenSpec(t, sessionsBase, s.ID, workDir)

	shadowBase := filepath.Join(sessionsBase, s.ID, "shadow")
	sg := checkpoint.New(workDir, shadowBase)
	require.NoError(t, sg.Init(ctx))

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("v1"), 0o644))
	_, err = sg.Commit(ctx, "iter 1")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("v2"), 0o644))
	_, err = sg.Commit(ctx, "iter 2")
	require.NoError(t, err)

	// step=5 when only 2 checkpoints exist.
	_, err = svc.Restore(ctx, &gilv1.RestoreRequest{SessionId: s.ID, Step: 5})
	require.Error(t, err)
	require.Contains(t, err.Error(), "OutOfRange")
}
