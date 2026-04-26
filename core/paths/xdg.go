// Package paths centralises gil's filesystem layout. All four XDG-style
// directories (Config / Data / State / Cache) are derived once from the
// environment and propagated through a single Layout value, so consumers
// (gild, gil, giltui, gilmcp) never need to touch HOME or XDG_* vars
// directly.
//
// Resolution order:
//
//  1. GIL_HOME=<dir>           — single-tree override; all four dirs
//                                become $GIL_HOME/{config,data,state,cache}.
//                                Useful for tests, sandboxes, and the
//                                legacy "everything under one folder"
//                                workflow.
//  2. XDG_*_HOME envvars       — honoured via the standard library
//                                (os.UserConfigDir, os.UserCacheDir) and,
//                                for the State dir, manual lookup of
//                                XDG_STATE_HOME (Go's stdlib has no
//                                helper for it as of Go 1.25).
//  3. Per-OS defaults          — Linux: ~/.config/gil, ~/.local/share/gil,
//                                ~/.local/state/gil, ~/.cache/gil.
//
// The design intentionally mirrors goose's paths.rs (etcetera +
// GOOSE_PATH_ROOT override) so that operators familiar with goose's
// layout immediately understand gil's. Reference:
// /home/ubuntu/research/goose/crates/goose/src/config/paths.rs.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// appName is the directory suffix appended to every XDG base.
const appName = "gil"

// Layout describes the four roots gil uses on disk. Every helper method
// composes a fully-qualified path under one of these roots; consumers
// must not manipulate the raw strings unless they have a very good
// reason (e.g. logging, doctor diagnostics).
type Layout struct {
	// Config holds user-editable configuration (auth.json, config.toml,
	// mcp.toml, AGENTS.md). Mode 0700 is recommended for the directory
	// because auth.json contains API keys.
	Config string

	// Data holds durable per-user state that should survive reboots and
	// upgrades — primarily the SQLite session DB and the sessions/
	// workspace tree.
	Data string

	// State holds runtime artifacts that may be rebuilt on the fly:
	// the gild Unix socket, the gild PID file, and rolling logs. State
	// dirs are commonly truncated by the OS (e.g. /run on systemd) so
	// nothing irreplaceable lives here.
	State string

	// Cache holds derived data that can be regenerated freely — the
	// model catalog snapshot, the repomap cache, etc.
	Cache string
}

// Default returns the XDG-derived layout for the current user. It does
// NOT create directories; call Layout.EnsureDirs (or let MigrateLegacyTilde
// do it) when you actually need them on disk.
//
// Default ignores GIL_HOME — pass through FromEnv if you want the
// override to take effect.
func Default() (Layout, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Layout{}, fmt.Errorf("paths: resolve config dir: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Layout{}, fmt.Errorf("paths: resolve cache dir: %w", err)
	}
	data, err := userDataDir()
	if err != nil {
		return Layout{}, fmt.Errorf("paths: resolve data dir: %w", err)
	}
	state, err := userStateDir()
	if err != nil {
		return Layout{}, fmt.Errorf("paths: resolve state dir: %w", err)
	}
	return Layout{
		Config: filepath.Join(cfg, appName),
		Data:   filepath.Join(data, appName),
		State:  filepath.Join(state, appName),
		Cache:  filepath.Join(cache, appName),
	}, nil
}

// FromEnv returns a Layout honouring the GIL_HOME single-tree override
// when set, otherwise it delegates to Default. GIL_HOME is the Go
// equivalent of goose's GOOSE_PATH_ROOT and is the recommended way to
// pin all four roots to one tmpdir during tests / sandboxed runs.
func FromEnv() (Layout, error) {
	if root := os.Getenv("GIL_HOME"); root != "" {
		return Layout{
			Config: filepath.Join(root, "config"),
			Data:   filepath.Join(root, "data"),
			State:  filepath.Join(root, "state"),
			Cache:  filepath.Join(root, "cache"),
		}, nil
	}
	return Default()
}

// WithUser returns a copy of the layout where each root has
// "users/<name>/" appended. This implements the gild --user namespacing
// (one daemon, multiple isolated users) without forcing each consumer to
// know the convention.
//
// An empty name returns the layout unchanged so callers can pass the raw
// flag value without branching.
func (l Layout) WithUser(name string) Layout {
	if name == "" {
		return l
	}
	return Layout{
		Config: filepath.Join(l.Config, "users", name),
		Data:   filepath.Join(l.Data, "users", name),
		State:  filepath.Join(l.State, "users", name),
		Cache:  filepath.Join(l.Cache, "users", name),
	}
}

// EnsureDirs creates any missing layout directory with mode 0700. We
// pick 0700 because Config holds credentials; the same mode is used
// uniformly for all four to avoid surprising users who chmod one and
// expect the others to follow.
func (l Layout) EnsureDirs() error {
	for _, d := range []string{l.Config, l.Data, l.State, l.Cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("paths: mkdir %s: %w", d, err)
		}
	}
	return nil
}

// AuthFile returns the path to the credstore file (Config/auth.json).
// The file itself is created lazily by core/credstore.
func (l Layout) AuthFile() string { return filepath.Join(l.Config, "auth.json") }

// ConfigFile returns the path to the global TOML config (Config/config.toml).
func (l Layout) ConfigFile() string { return filepath.Join(l.Config, "config.toml") }

// MCPConfigFile returns the path to the user's MCP server registry
// (Config/mcp.toml).
func (l Layout) MCPConfigFile() string { return filepath.Join(l.Config, "mcp.toml") }

// AgentsFile returns the path to the global AGENTS.md (Config/AGENTS.md).
// Project-local AGENTS.md is discovered by walking from the workspace
// root and is independent of this path.
func (l Layout) AgentsFile() string { return filepath.Join(l.Config, "AGENTS.md") }

// SessionsDir returns the directory in which gild stores per-session
// workspaces (Data/sessions). Each session lives under a ULID-named
// subdir within this tree.
func (l Layout) SessionsDir() string { return filepath.Join(l.Data, "sessions") }

// SessionsDB returns the path to the SQLite session database
// (Data/sessions.db). Schema is unaffected by this change — only the
// directory containing the file moves.
func (l Layout) SessionsDB() string { return filepath.Join(l.Data, "sessions.db") }

// Sock returns the Unix domain socket path used by gild
// (State/gild.sock). It must live in State (not Data) because the
// socket is a runtime artifact that should be re-created on every
// daemon start.
func (l Layout) Sock() string { return filepath.Join(l.State, "gild.sock") }

// Pid returns the gild PID file path (State/gild.pid).
func (l Layout) Pid() string { return filepath.Join(l.State, "gild.pid") }

// LogsDir returns the directory for rolling gild logs (State/logs).
func (l Layout) LogsDir() string { return filepath.Join(l.State, "logs") }

// ModelCatalog returns the path to the cached provider model catalog
// (Cache/models.json).
func (l Layout) ModelCatalog() string { return filepath.Join(l.Cache, "models.json") }

// RepomapCache returns the directory under which repomap stores its
// per-workspace caches (Cache/repomap).
func (l Layout) RepomapCache() string { return filepath.Join(l.Cache, "repomap") }

// ShadowGitBase returns the directory under which checkpoint.ShadowGit
// keeps per-workspace shadow .git trees (Data/shadow). Putting it in
// Data (not State) keeps checkpoints durable across reboots.
func (l Layout) ShadowGitBase() string { return filepath.Join(l.Data, "shadow") }

// userDataDir returns $XDG_DATA_HOME or ~/.local/share. Go's stdlib
// only exposes a Cache and Config helper, so we reimplement the same
// fallback chain here.
func userDataDir() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// userStateDir returns $XDG_STATE_HOME or ~/.local/state. Same rationale
// as userDataDir — there's no stdlib helper for State.
func userStateDir() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}
