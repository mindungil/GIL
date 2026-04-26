package slash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/checkpoint"
	"github.com/jedutools/gil/core/paths"
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

func TestCompactHandler_StubMessage(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess-1")
	s, _ := reg.Lookup("compact")
	out, err := s.Handler(context.Background(), Command{Name: "compact"})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(out), "not yet wired")
}

func TestModelHandler_RequiresArg(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess-1")
	s, _ := reg.Lookup("model")
	_, err := s.Handler(context.Background(), Command{Name: "model"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage")
}

func TestModelHandler_QueuesHint(t *testing.T) {
	reg, _, _, _ := newTestEnv(t, "sess-1")
	s, _ := reg.Lookup("model")
	out, err := s.Handler(context.Background(), Command{Name: "model", Args: []string{"gpt-4o"}})
	require.NoError(t, err)
	require.Contains(t, out, "gpt-4o")
	require.Contains(t, out, "hint queued")
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
