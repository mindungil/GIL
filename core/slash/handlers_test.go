package slash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/checkpoint"
	"github.com/mindungil/gil/core/paths"
)

func newTestEnv(t *testing.T, sessionID string) (*Registry, *HandlerEnv, paths.Layout, string) {
	t.Helper()
	root := t.TempDir()
	l := paths.Layout{
		Config: filepath.Join(root, "config"),
		Data:   filepath.Join(root, "data"),
		State:  filepath.Join(root, "state"),
		Cache:  filepath.Join(root, "cache"),
	}
	require.NoError(t, l.EnsureDirs())
	env := &HandlerEnv{SessionID: sessionID, Layout: l}
	reg := NewRegistry()
	RegisterDefaults(reg, env)
	return reg, env, l, root
}

func TestRegisterDefaults_RegistersNine(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "")
	specs := reg.List()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	require.ElementsMatch(t, []string{
		"agents", "clear", "compact", "cost", "diff", "help", "model", "quit", "status",
	}, names)
}

func TestHelpHandler_ListsAllCommands(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess")
	s, ok := reg.Lookup("help")
	require.True(t, ok)
	out, err := s.Handler(context.Background(), Command{Name: "help"})
	require.NoError(t, err)
	for _, c := range []string{"/help", "/status", "/cost", "/clear", "/compact", "/model", "/agents", "/diff", "/quit"} {
		require.Contains(t, out, c, "help should mention %s", c)
	}
	// Aliases listed for /quit
	require.Contains(t, out, "exit")
	require.Contains(t, out, "/q")
}

func TestStatusHandler_NoSession(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "")
	s, _ := reg.Lookup("status")
	_, err := s.Handler(context.Background(), Command{Name: "status"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no session attached")
}

func TestStatusHandler_PrintsFromFetcher(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Fetcher = func(ctx context.Context, id string) (*SessionInfo, error) {
		return &SessionInfo{
			ID:               id,
			Status:           "RUNNING",
			WorkingDir:       "/tmp/work",
			GoalHint:         "ship it",
			CurrentIteration: 3,
			CurrentTokens:    1000,
			TotalTokens:      4500,
		}, nil
	}
	s, _ := reg.Lookup("status")
	out, err := s.Handler(context.Background(), Command{Name: "status"})
	require.NoError(t, err)
	require.Contains(t, out, "sess-1")
	require.Contains(t, out, "RUNNING")
	require.Contains(t, out, "ship it")
	require.Contains(t, out, "Iteration:  3")
	require.Contains(t, out, "1000")
}

func TestCostHandler_StubMessageWhenNoFetcher(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Fetcher = nil
	s, _ := reg.Lookup("cost")
	out, err := s.Handler(context.Background(), Command{Name: "cost"})
	require.NoError(t, err)
	require.Contains(t, out, "Track F")
}

func TestCostHandler_PrintsFromFetcher(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Fetcher = func(ctx context.Context, id string) (*SessionInfo, error) {
		return &SessionInfo{ID: id, TotalTokens: 1234, TotalCostUSD: 0.125}, nil
	}
	s, _ := reg.Lookup("cost")
	out, err := s.Handler(context.Background(), Command{Name: "cost"})
	require.NoError(t, err)
	require.Contains(t, out, "$0.1250")
	require.Contains(t, out, "1234")
}

func TestClearHandler_InvokesLocalClearer(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	called := 0
	env.Local.ClearEvents = func() { called++ }
	s, _ := reg.Lookup("clear")
	out, err := s.Handler(context.Background(), Command{Name: "clear"})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.Contains(t, out, "cleared")
}

func TestClearHandler_NilClearerSafe(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess-1")
	s, _ := reg.Lookup("clear")
	_, err := s.Handler(context.Background(), Command{Name: "clear"})
	require.NoError(t, err)
}

// fakeRunControl captures the calls slash handlers make against the
// RunControl interface so tests can both assert what was sent and
// inject pre-canned responses without standing up a real gRPC client.
type fakeRunControl struct {
	compactQueued bool
	compactReason string
	compactErr    error
	compactCalls  []string

	hintPosted bool
	hintReason string
	hintErr    error
	hintCalls  []map[string]string

	diffResult *DiffResult
	diffErr    error
	diffCalls  int
}

func (f *fakeRunControl) RequestCompact(_ context.Context, sessionID string) (bool, string, error) {
	f.compactCalls = append(f.compactCalls, sessionID)
	return f.compactQueued, f.compactReason, f.compactErr
}

func (f *fakeRunControl) PostHint(_ context.Context, _ string, hint map[string]string) (bool, string, error) {
	cp := make(map[string]string, len(hint))
	for k, v := range hint {
		cp[k] = v
	}
	f.hintCalls = append(f.hintCalls, cp)
	return f.hintPosted, f.hintReason, f.hintErr
}

func (f *fakeRunControl) Diff(_ context.Context, _ string) (*DiffResult, error) {
	f.diffCalls++
	return f.diffResult, f.diffErr
}

func TestCompactHandler_NoSession(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "")
	s, _ := reg.Lookup("compact")
	_, err := s.Handler(context.Background(), Command{Name: "compact"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no session attached")
}

func TestCompactHandler_NoRunControl(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = nil
	s, _ := reg.Lookup("compact")
	out, err := s.Handler(context.Background(), Command{Name: "compact"})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(out), "no run-control client configured")
}

func TestCompactHandler_QueuedReportsSuccess(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	fc := &fakeRunControl{compactQueued: true}
	env.Run = fc
	s, _ := reg.Lookup("compact")
	out, err := s.Handler(context.Background(), Command{Name: "compact"})
	require.NoError(t, err)
	require.Contains(t, out, "compact requested for next turn boundary")
	require.Equal(t, []string{"sess-1"}, fc.compactCalls)
}

func TestCompactHandler_NotQueuedReportsReason(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = &fakeRunControl{compactQueued: false, compactReason: "no run in flight"}
	s, _ := reg.Lookup("compact")
	out, err := s.Handler(context.Background(), Command{Name: "compact"})
	require.NoError(t, err)
	require.Contains(t, out, "no run in flight")
}

func TestModelHandler_RequiresArg(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess-1")
	s, _ := reg.Lookup("model")
	_, err := s.Handler(context.Background(), Command{Name: "model"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage")
}

func TestModelHandler_PostsHintViaRunControl(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	fc := &fakeRunControl{hintPosted: true}
	env.Run = fc
	s, _ := reg.Lookup("model")
	out, err := s.Handler(context.Background(), Command{Name: "model", Args: []string{"claude-haiku-4-5"}})
	require.NoError(t, err)
	require.Contains(t, out, "claude-haiku-4-5")
	require.Contains(t, out, "model hint posted")
	require.Len(t, fc.hintCalls, 1)
	require.Equal(t, "claude-haiku-4-5", fc.hintCalls[0]["model"])
}

func TestModelHandler_NotPostedReportsReason(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = &fakeRunControl{hintPosted: false, hintReason: "no run in flight"}
	s, _ := reg.Lookup("model")
	out, err := s.Handler(context.Background(), Command{Name: "model", Args: []string{"gpt-4o"}})
	require.NoError(t, err)
	require.Contains(t, out, "no run in flight")
	require.Contains(t, out, "gpt-4o")
}

func TestModelHandler_NoRunControlReportsFriendly(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = nil
	s, _ := reg.Lookup("model")
	out, err := s.Handler(context.Background(), Command{Name: "model", Args: []string{"gpt-4o"}})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(out), "no run-control client configured")
}

func TestQuitHandler_ReturnsSentinel(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "")
	s, _ := reg.Lookup("quit")
	_, err := s.Handler(context.Background(), Command{Name: "quit"})
	require.True(t, errors.Is(err, ErrQuit))

	// Aliases dispatch to the same handler.
	s, _ = reg.Lookup("exit")
	_, err = s.Handler(context.Background(), Command{Name: "exit"})
	require.True(t, errors.Is(err, ErrQuit))

	s, _ = reg.Lookup("q")
	_, err = s.Handler(context.Background(), Command{Name: "q"})
	require.True(t, errors.Is(err, ErrQuit))
}

func TestAgentsHandler_NoneFound(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "")
	s, _ := reg.Lookup("agents")
	out, err := s.Handler(context.Background(), Command{Name: "agents"})
	require.NoError(t, err)
	require.Contains(t, out, "no AGENTS.md")
}

func TestAgentsHandler_PrintsGlobalWhenEditorEmpty(t *testing.T) {
	reg, env, l, _ := newTestEnv(t, "")
	require.NoError(t, os.WriteFile(l.AgentsFile(), []byte("# Global Agents\n\n- be tidy\n"), 0o644))
	t.Setenv("EDITOR", "") // force the print path
	env.Stdout = nil       // not a *os.File → not a terminal
	s, _ := reg.Lookup("agents")
	out, err := s.Handler(context.Background(), Command{Name: "agents"})
	require.NoError(t, err)
	require.Contains(t, out, "# Global Agents")
	require.Contains(t, out, "be tidy")
}

func TestAgentsHandler_PrefersWorkspaceFile(t *testing.T) {
	reg, env, l, _ := newTestEnv(t, "sess-1")
	wsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("WS-AGENTS"), 0o644))
	require.NoError(t, os.WriteFile(l.AgentsFile(), []byte("GLOBAL-AGENTS"), 0o644))
	env.Fetcher = func(ctx context.Context, id string) (*SessionInfo, error) {
		return &SessionInfo{ID: id, WorkingDir: wsDir}, nil
	}
	t.Setenv("EDITOR", "")
	s, _ := reg.Lookup("agents")
	out, err := s.Handler(context.Background(), Command{Name: "agents"})
	require.NoError(t, err)
	require.Contains(t, out, "WS-AGENTS")
	require.NotContains(t, out, "GLOBAL-AGENTS")
}

func TestDiffHandler_NoCheckpoints(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	wsDir := t.TempDir()
	env.Fetcher = func(ctx context.Context, id string) (*SessionInfo, error) {
		return &SessionInfo{ID: id, WorkingDir: wsDir}, nil
	}
	s, _ := reg.Lookup("diff")
	out, err := s.Handler(context.Background(), Command{Name: "diff"})
	require.NoError(t, err)
	require.Contains(t, out, "no checkpoints")
}

func TestDiffHandler_PrefersRunControlWhenSet(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = &fakeRunControl{
		diffResult: &DiffResult{
			UnifiedDiff:   "diff --git a/x b/x\n",
			FilesChanged:  1,
			LinesAdded:    3,
			LinesRemoved:  1,
			CheckpointSHA: "deadbeefcafebabe",
		},
	}
	// Fetcher intentionally nil to prove the RPC path doesn't touch it.
	env.Fetcher = nil
	s, _ := reg.Lookup("diff")
	out, err := s.Handler(context.Background(), Command{Name: "diff"})
	require.NoError(t, err)
	require.Contains(t, out, "diff vs checkpoint deadbeef")
	require.Contains(t, out, "1 files, +3/-1")
	require.Contains(t, out, "diff --git a/x b/x")
}

func TestDiffHandler_RPCNoCheckpointsRendersNote(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = &fakeRunControl{diffResult: &DiffResult{Note: "no checkpoints yet for this session"}}
	s, _ := reg.Lookup("diff")
	out, err := s.Handler(context.Background(), Command{Name: "diff"})
	require.NoError(t, err)
	require.Contains(t, out, "no checkpoints")
}

func TestDiffHandler_RPCTruncatedHeader(t *testing.T) {
	reg, env, _, _ := newTestEnv(t, "sess-1")
	env.Run = &fakeRunControl{
		diffResult: &DiffResult{
			UnifiedDiff:    "...",
			FilesChanged:   2,
			LinesAdded:     200,
			LinesRemoved:   50,
			Truncated:      true,
			TruncatedBytes: 4096,
			CheckpointSHA:  "abcdef0123456789",
		},
	}
	s, _ := reg.Lookup("diff")
	out, err := s.Handler(context.Background(), Command{Name: "diff"})
	require.NoError(t, err)
	require.Contains(t, out, "truncated 4096 bytes")
}

func TestDiffHandler_DetectsWorkspaceChange(t *testing.T) {
	reg, env, l, _ := newTestEnv(t, "sess-1")
	wsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "f.txt"), []byte("v1\n"), 0o644))
	env.Fetcher = func(ctx context.Context, id string) (*SessionInfo, error) {
		return &SessionInfo{ID: id, WorkingDir: wsDir}, nil
	}

	// Manually create a shadow checkpoint at the same layout the server uses.
	shadowBase := filepath.Join(l.SessionsDir(), "sess-1", "shadow")
	require.NoError(t, os.MkdirAll(shadowBase, 0o755))
	sg := checkpoint.New(wsDir, shadowBase)
	require.NoError(t, sg.Init(context.Background()))
	_, err := sg.Commit(context.Background(), "init")
	require.NoError(t, err)

	// Mutate workspace.
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "f.txt"), []byte("v2\n"), 0o644))

	s, _ := reg.Lookup("diff")
	out, err := s.Handler(context.Background(), Command{Name: "diff"})
	require.NoError(t, err)
	require.Contains(t, out, "diff vs checkpoint")
	require.Contains(t, out, "-v1")
	require.Contains(t, out, "+v2")
}

func TestDispatch_UnknownCommand(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess")
	_, ok := reg.Lookup("nopenope")
	require.False(t, ok)
}
