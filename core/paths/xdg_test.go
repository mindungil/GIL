package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// withCleanEnv hides any caller-supplied XDG_* / GIL_HOME / HOME so each
// test sees a deterministic environment. It also points HOME at t.TempDir
// so Default() can compute predictable per-OS defaults.
func withCleanEnv(t *testing.T, home string) {
	t.Helper()
	for _, k := range []string{"GIL_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", home)
}

func TestDefault_LinuxXDGFallbacks(t *testing.T) {
	home := t.TempDir()
	withCleanEnv(t, home)

	l, err := Default()
	require.NoError(t, err)

	// On Linux without XDG_*, os.UserConfigDir returns ~/.config and
	// os.UserCacheDir returns ~/.cache. State and Data have manual
	// fallbacks in xdg.go.
	require.Equal(t, filepath.Join(home, ".config", "gil"), l.Config)
	require.Equal(t, filepath.Join(home, ".local", "share", "gil"), l.Data)
	require.Equal(t, filepath.Join(home, ".local", "state", "gil"), l.State)
	require.Equal(t, filepath.Join(home, ".cache", "gil"), l.Cache)
}

func TestDefault_HonoursXDGEnvVars(t *testing.T) {
	home := t.TempDir()
	withCleanEnv(t, home)
	cfg, data, state, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", cache)

	l, err := Default()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg, "gil"), l.Config)
	require.Equal(t, filepath.Join(data, "gil"), l.Data)
	require.Equal(t, filepath.Join(state, "gil"), l.State)
	require.Equal(t, filepath.Join(cache, "gil"), l.Cache)
}

func TestFromEnv_GILHomeOverride(t *testing.T) {
	home := t.TempDir()
	withCleanEnv(t, home)

	root := t.TempDir()
	t.Setenv("GIL_HOME", root)

	l, err := FromEnv()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "config"), l.Config)
	require.Equal(t, filepath.Join(root, "data"), l.Data)
	require.Equal(t, filepath.Join(root, "state"), l.State)
	require.Equal(t, filepath.Join(root, "cache"), l.Cache)
}

func TestFromEnv_FallsBackToDefault(t *testing.T) {
	home := t.TempDir()
	withCleanEnv(t, home) // GIL_HOME forced empty

	l, err := FromEnv()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".config", "gil"), l.Config)
}

func TestWithUser_AppendsPerRoot(t *testing.T) {
	base := Layout{Config: "/a", Data: "/b", State: "/c", Cache: "/d"}
	got := base.WithUser("alice")
	require.Equal(t, "/a/users/alice", got.Config)
	require.Equal(t, "/b/users/alice", got.Data)
	require.Equal(t, "/c/users/alice", got.State)
	require.Equal(t, "/d/users/alice", got.Cache)
}

func TestWithUser_EmptyIsNoOp(t *testing.T) {
	base := Layout{Config: "/a", Data: "/b", State: "/c", Cache: "/d"}
	require.Equal(t, base, base.WithUser(""))
}

func TestHelpers_ComposePathsCorrectly(t *testing.T) {
	l := Layout{Config: "/c", Data: "/d", State: "/s", Cache: "/k"}
	require.Equal(t, "/c/auth.json", l.AuthFile())
	require.Equal(t, "/c/config.toml", l.ConfigFile())
	require.Equal(t, "/c/mcp.toml", l.MCPConfigFile())
	require.Equal(t, "/c/AGENTS.md", l.AgentsFile())
	require.Equal(t, "/d/sessions", l.SessionsDir())
	require.Equal(t, "/d/sessions.db", l.SessionsDB())
	require.Equal(t, "/s/gild.sock", l.Sock())
	require.Equal(t, "/s/gild.pid", l.Pid())
	require.Equal(t, "/s/logs", l.LogsDir())
	require.Equal(t, "/k/models.json", l.ModelCatalog())
	require.Equal(t, "/k/repomap", l.RepomapCache())
	require.Equal(t, "/d/shadow", l.ShadowGitBase())
}

func TestEnsureDirs_CreatesAllFour(t *testing.T) {
	root := t.TempDir()
	l := Layout{
		Config: filepath.Join(root, "c"),
		Data:   filepath.Join(root, "d"),
		State:  filepath.Join(root, "s"),
		Cache:  filepath.Join(root, "k"),
	}
	require.NoError(t, l.EnsureDirs())
	for _, p := range []string{l.Config, l.Data, l.State, l.Cache} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "%s should be a directory", p)
	}
}
