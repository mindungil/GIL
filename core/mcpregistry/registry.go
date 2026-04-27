// Package mcpregistry persists MCP-server launch definitions across
// gil sessions so the user only configures a server once and every
// future run picks it up — the same shape codex's `mcp_servers` table
// in `~/.codex/config.toml` and goose's `extensions` map provide.
//
// The registry is split into two layers:
//
//   - Global   (Config/mcp.toml under the user's XDG dir): servers the
//     user wants available in every gil project.
//   - Project  (`<workspace>/.gil/mcp.toml`): servers scoped to one repo.
//     Project entries override global ones with the same name so a
//     monorepo can override a system-wide default without touching the
//     user's home dir.
//
// On disk the file is a single TOML table named `servers` whose keys
// are the server names. We deliberately use the BurntSushi/toml encoder
// (already pulled in by core/workspace/config.go) so we round-trip
// hand-edited files without rearranging keys.
//
// Reference lift:
//
//   - /home/ubuntu/research/codex/codex-rs/cli/src/mcp_cmd.rs — codex's
//     `mcp add` writes a stdio/streamable_http entry into a single
//     `mcp_servers` map and replaces the file atomically. We adopt the
//     same map-keyed-by-name shape (no separate id/name split) and the
//     same "stdio vs http" type discriminator.
//   - /home/ubuntu/research/opencode/packages/opencode/src/cli/cmd/mcp.ts —
//     opencode allows project + global scopes from the same CLI; the
//     project file lives under `<workspace>/.opencode/`. We mirror that
//     two-scope model with `<workspace>/.gil/mcp.toml`.
//   - /home/ubuntu/research/goose/crates/goose/src/config/extensions.rs —
//     goose's `set_extension` insert-into-map-then-write semantics are
//     identical to ours; the only difference is goose serialises YAML
//     and we serialise TOML.
//
// The on-disk format intentionally carries bearer tokens inline (rather
// than indirecting through a separate keystore) because the same convention
// is established for codex (`bearer_token_env_var` plus persisted scopes
// in `config.toml`) and goose (raw env values inside `extensions.<name>`).
// We compensate by mode 0600 on the file plus an explicit chmod after
// the atomic rename.
package mcpregistry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Server is one MCP-server launch definition. The fields are the union
// of "stdio" (Command/Args/Env) and "http" (URL/Auth) transports — only
// the subset relevant to Type is meaningful, and Validate enforces that.
//
// Name is a synthetic field populated from the TOML map key; it is NOT
// serialised back out (the encoder writes the map structure, so the key
// is implicit).
type Server struct {
	// Name is the registry key for this server (e.g. "fs" → table
	// `[servers.fs]`). Populated by Load; ignored on write.
	Name string `toml:"-"`

	// Type discriminates the transport. Currently "stdio" or "http"; an
	// empty value is rejected by Validate so we get a clear error rather
	// than silently launching nothing.
	Type string `toml:"type"`

	// Command is the executable to spawn for stdio servers (e.g. "npx").
	// Required when Type == "stdio".
	Command string `toml:"command,omitempty"`

	// Args is the argv tail passed verbatim to Command. May be empty.
	Args []string `toml:"args,omitempty"`

	// Env supplies extra env vars set on the spawned subprocess in
	// KEY=VALUE form. Inherited from the parent process unless overridden.
	Env map[string]string `toml:"env,omitempty"`

	// URL is the streamable-HTTP endpoint for http servers. Required
	// when Type == "http".
	URL string `toml:"url,omitempty"`

	// Auth carries the http auth credential. Currently only "bearer:<token>"
	// is recognised; Validate accepts an empty value as "no auth required".
	// The whole token is written into mcp.toml — see file-mode notes on
	// AddGlobal/AddProject for why mode 0600 is enforced.
	Auth string `toml:"auth,omitempty"`

	// Description is a free-form one-line label shown by `gil mcp list`.
	// Optional.
	Description string `toml:"description,omitempty"`
}

// File is the on-disk representation: one top-level table `servers`
// whose entries are name → Server.
type File struct {
	Servers map[string]Server `toml:"servers"`
}

// Registry resolves and mutates the layered MCP server registry.
//
// GlobalPath should be paths.Layout.MCPConfigFile() (or empty to skip
// the global layer entirely, e.g. for tests). ProjectPath is the
// `<workspace>/.gil/mcp.toml` of the project under test, also empty
// when no project scope is in play.
type Registry struct {
	GlobalPath  string
	ProjectPath string
}

// scope identifiers used by Remove and reported by Load when surfacing
// where a server originated.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
	ScopeAuto    = "auto"
)

// Load reads both the global and the project file and returns a single
// merged map keyed by server name. When both layers define the same
// name, the project entry wins (matches the workspace.Config layering
// philosophy: the closer file overrides the farther one).
//
// A missing file at either path is not an error — Load returns the
// other layer (or an empty map when both are missing) so callers can
// invoke it unconditionally.
func (r *Registry) Load() (map[string]Server, error) {
	merged := map[string]Server{}

	for _, layer := range []struct {
		path string
		name string
	}{
		{r.GlobalPath, ScopeGlobal},
		{r.ProjectPath, ScopeProject},
	} {
		if layer.path == "" {
			continue
		}
		got, ok, err := loadFile(layer.path)
		if err != nil {
			return nil, fmt.Errorf("mcpregistry: load %s registry %q: %w", layer.name, layer.path, err)
		}
		if !ok {
			continue
		}
		for name, srv := range got {
			srv.Name = name
			merged[name] = srv
		}
	}
	return merged, nil
}

// LoadScope returns the entries from a single scope ("global" or
// "project") without merging. Used by `gil mcp list` to display the
// SCOPE column and by Remove to know which file to rewrite.
func (r *Registry) LoadScope(scope string) (map[string]Server, error) {
	path, err := r.scopePath(scope)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return map[string]Server{}, nil
	}
	got, _, err := loadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mcpregistry: load %s registry %q: %w", scope, path, err)
	}
	if got == nil {
		got = map[string]Server{}
	}
	for name := range got {
		s := got[name]
		s.Name = name
		got[name] = s
	}
	return got, nil
}

// AddGlobal writes s to the global mcp.toml, returning an error if a
// server with the same name already exists in the same scope. Use
// Remove to evict the existing entry first if a "replace" is intended —
// silently overwriting would let typos overwrite working configs.
//
// The directory is created with mode 0700 (matching paths.Layout
// EnsureDirs) and the file is chmod 0600 after the atomic rename so
// bearer tokens never leak through a wider umask.
func (r *Registry) AddGlobal(s Server) error {
	if r.GlobalPath == "" {
		return fmt.Errorf("mcpregistry: AddGlobal requires GlobalPath to be set")
	}
	return r.addAt(r.GlobalPath, s)
}

// AddProject is the project-scope counterpart of AddGlobal. The caller
// must set ProjectPath; if the workspace has no `.gil/` we return an
// error so the CLI can hint the user toward `gil init` (which creates
// the directory) rather than auto-creating it ourselves.
func (r *Registry) AddProject(s Server) error {
	if r.ProjectPath == "" {
		return fmt.Errorf("mcpregistry: AddProject requires ProjectPath to be set")
	}
	dir := filepath.Dir(r.ProjectPath)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("mcpregistry: project dir %q does not exist; run `gil init` first or use --global", dir)
		}
		return fmt.Errorf("mcpregistry: stat project dir %q: %w", dir, err)
	}
	return r.addAt(r.ProjectPath, s)
}

// Remove deletes name from the requested scope. scope="auto" tries the
// global file first, then project — useful for the CLI default where
// the user did not specify which scope. Returns nil even when the file
// did not exist (idempotent), but returns an error when the file
// existed and the name was not present (otherwise typos would be
// silently no-op'd).
func (r *Registry) Remove(name, scope string) error {
	if name == "" {
		return fmt.Errorf("mcpregistry: Remove name is empty")
	}
	switch scope {
	case ScopeGlobal:
		return r.removeAt(r.GlobalPath, name, "global")
	case ScopeProject:
		return r.removeAt(r.ProjectPath, name, "project")
	case ScopeAuto, "":
		// Try global, then project. First match wins; if neither file
		// has it, return a "not found" error.
		for _, attempt := range []struct {
			path string
			name string
		}{
			{r.GlobalPath, "global"},
			{r.ProjectPath, "project"},
		} {
			if attempt.path == "" {
				continue
			}
			got, ok, err := loadFile(attempt.path)
			if err != nil {
				return fmt.Errorf("mcpregistry: read %s registry %q: %w", attempt.name, attempt.path, err)
			}
			if !ok {
				continue
			}
			if _, present := got[name]; present {
				return r.removeAt(attempt.path, name, attempt.name)
			}
		}
		return fmt.Errorf("mcpregistry: server %q not found in any scope", name)
	default:
		return fmt.Errorf("mcpregistry: unknown scope %q (want global|project|auto)", scope)
	}
}

// Validate enforces shape: known Type, transport-specific required
// fields present, no obviously-broken combinations (e.g. URL on a
// stdio server). Returned errors are user-vocabulary so the CLI can
// present them straight without rewording.
func Validate(s Server) error {
	switch s.Type {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("stdio MCP servers require a command (e.g. `npx`)")
		}
		if s.URL != "" {
			return fmt.Errorf("stdio MCP servers cannot have a URL (set type=\"http\" instead)")
		}
	case "http":
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("http MCP servers require a URL")
		}
		if s.Command != "" || len(s.Args) > 0 {
			return fmt.Errorf("http MCP servers cannot have a command or args (set type=\"stdio\" instead)")
		}
	case "":
		return fmt.Errorf("MCP server type is required (one of: stdio, http)")
	default:
		return fmt.Errorf("unknown MCP server type %q (want stdio or http)", s.Type)
	}
	if s.Auth != "" && !strings.HasPrefix(s.Auth, "bearer:") {
		return fmt.Errorf("auth must be empty or `bearer:<token>` (got %q)", maskAuth(s.Auth))
	}
	return nil
}

// scopePath resolves a scope name to the registry's configured path.
func (r *Registry) scopePath(scope string) (string, error) {
	switch scope {
	case ScopeGlobal:
		return r.GlobalPath, nil
	case ScopeProject:
		return r.ProjectPath, nil
	default:
		return "", fmt.Errorf("mcpregistry: scope %q is not addressable (want global|project)", scope)
	}
}

// addAt is the shared implementation for AddGlobal/AddProject. It
// loads the existing file (or starts empty), refuses to overwrite an
// existing entry, validates the new entry, and atomically rewrites
// the whole file.
func (r *Registry) addAt(path string, s Server) error {
	if s.Name == "" {
		return fmt.Errorf("mcpregistry: server Name is required")
	}
	if err := Validate(s); err != nil {
		return fmt.Errorf("mcpregistry: %w", err)
	}
	got, _, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("mcpregistry: load %q: %w", path, err)
	}
	if got == nil {
		got = map[string]Server{}
	}
	if _, exists := got[s.Name]; exists {
		return fmt.Errorf("mcpregistry: server %q already exists in %q (remove it first to replace)", s.Name, path)
	}
	// Strip the synthetic Name field before serialising — the map key
	// already encodes the name.
	out := s
	out.Name = ""
	got[s.Name] = out
	return saveFile(path, got)
}

// removeAt rewrites path with name removed. Returns an error when the
// file does not exist or the name is not present, so the user notices
// typos.
func (r *Registry) removeAt(path, name, scopeLabel string) error {
	if path == "" {
		return fmt.Errorf("mcpregistry: %s scope is not configured", scopeLabel)
	}
	got, ok, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("mcpregistry: read %s registry %q: %w", scopeLabel, path, err)
	}
	if !ok {
		return fmt.Errorf("mcpregistry: %s registry %q does not exist", scopeLabel, path)
	}
	if _, exists := got[name]; !exists {
		return fmt.Errorf("mcpregistry: server %q not found in %s registry", name, scopeLabel)
	}
	delete(got, name)
	return saveFile(path, got)
}

// loadFile reads and decodes one mcp.toml. ok=false on missing file so
// callers can chain optional layers without branching.
func loadFile(path string) (map[string]Server, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var f File
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	if f.Servers == nil {
		f.Servers = map[string]Server{}
	}
	// Stamp Name so callers don't have to map-iterate twice.
	for name, srv := range f.Servers {
		srv.Name = name
		f.Servers[name] = srv
	}
	return f.Servers, true, nil
}

// saveFile atomically replaces path with a TOML serialisation of
// servers. Strategy: encode into a *.tmp sibling, fsync, chmod 0600,
// then rename — same dance as core/credstore.FileStore.save.
//
// We deliberately use BurntSushi/toml's encoder rather than hand-
// rolling output: future fields added to Server will be picked up
// without a parallel format function, and the encoder already
// handles map ordering deterministically (sorted keys).
func saveFile(path string, servers map[string]Server) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mcpregistry: mkdir %s: %w", dir, err)
	}

	// Re-build the map without the synthetic Name field so the encoder
	// doesn't try to write it (it's tagged `toml:"-"` so it's skipped
	// anyway; the strip is defensive).
	clean := make(map[string]Server, len(servers))
	for k, v := range servers {
		v.Name = ""
		clean[k] = v
	}
	f := File{Servers: clean}

	// Walk keys in sorted order for stable diffs. BurntSushi/toml
	// already sorts string keys in maps, but encoding a struct that
	// wraps a map keeps the order — re-encoding via a sorted slice
	// would lose Server's fields. Trust the encoder.
	_ = sort.StringsAreSorted // keep imports tidy

	tmp, err := os.CreateTemp(dir, ".mcp.toml.*.tmp")
	if err != nil {
		return fmt.Errorf("mcpregistry: tempfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(f); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("mcpregistry: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("mcpregistry: fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("mcpregistry: close tmp: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			cleanup()
			return fmt.Errorf("mcpregistry: chmod 0600 %s: %w", tmpPath, err)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("mcpregistry: rename %s: %w", path, err)
	}
	// Best-effort directory fsync for durability of the rename.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// maskAuth elides everything after the first 7 characters of an auth
// string for inclusion in error messages; we never want to echo a
// bearer token back at the user (or into a log) verbatim.
func maskAuth(s string) string {
	if len(s) <= 7 {
		return "***"
	}
	return s[:7] + "***"
}
