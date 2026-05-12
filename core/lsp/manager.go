package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager owns one Client per language, scoped to a single workspace
// directory. ClientFor lazily spawns the server on first use and reuses it
// for every subsequent request against the same language.
type Manager struct {
	// Workspace is the absolute root path passed to each spawned server
	// (becomes the rootUri / workspaceFolder).
	Workspace string

	// Configs overrides DefaultServerConfigs at construction time. Tests
	// pass this to swap in a mock-LSP-server-via-stub-binary; production
	// callers use NewManager which seeds it from DefaultServerConfigs.
	Configs map[string]ServerConfig

	mu       sync.Mutex
	clients  map[string]*Client      // keyed by language id
	failed   map[string]error        // keyed by language id; sticky after a failed spawn
}

// NewManager builds a manager seeded with the default extension→server
// map. Workspace must be an absolute path (the LSP `rootUri` requirement);
// the constructor accepts whatever string the caller passes — validation
// happens lazily in ClientFor so callers can construct a manager even when
// the workspace doesn't exist yet (test harnesses, deferred provisioning).
func NewManager(workspace string) *Manager {
	return &Manager{
		Workspace: workspace,
		Configs:   DefaultServerConfigs(),
		clients:   make(map[string]*Client),
		failed:    make(map[string]error),
	}
}

// ClientFor returns the Client responsible for `file`, spawning the server
// subprocess on first use. The returned error is one of:
//
//   - ErrNoServer: the file extension has no entry in m.Configs
//   - ErrServerUnavailable: the server is configured but not in PATH
//     (with the helpful install hint wrapped in)
//   - any spawn / initialize error from the underlying server
//
// Lazy-spawn semantics: if a server fails to start, we cache the error so
// subsequent calls fail fast (without re-spawning) until Shutdown is
// called. This matters for `pyright` etc. that aren't installed: the
// agent shouldn't pay the spawn cost on every call.
func (m *Manager) ClientFor(ctx context.Context, file string) (*Client, error) {
	cfg, err := m.configFor(file)
	if err != nil {
		return nil, err
	}
	if !cfg.IsAvailable() {
		hint := cfg.InstallHint
		if hint == "" {
			hint = strings.Join(cfg.Command, " ") + " not found in PATH"
		}
		return nil, fmt.Errorf("%w: %s (%s)", ErrServerUnavailable, cfg.Language, hint)
	}

	m.mu.Lock()
	if cached, ok := m.clients[cfg.Language]; ok {
		m.mu.Unlock()
		return cached, nil
	}
	if prevErr, ok := m.failed[cfg.Language]; ok {
		m.mu.Unlock()
		return nil, prevErr
	}
	m.mu.Unlock()

	client, err := m.spawn(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		m.failed[cfg.Language] = err
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Lost a race with a concurrent ClientFor call: prefer the first one
	// in the map, drop the duplicate.
	if existing, ok := m.clients[cfg.Language]; ok {
		go client.Shutdown(context.Background())
		return existing, nil
	}
	m.clients[cfg.Language] = client
	return client, nil
}

// LanguageIDFor returns the LSP languageId for `file` based on the
// configured extension map. Used by the tool layer when calling didOpen.
// Returns "" when the extension isn't configured.
func (m *Manager) LanguageIDFor(file string) string {
	cfg, err := m.configFor(file)
	if err != nil {
		return ""
	}
	return cfg.LanguageID
}

// configFor resolves the file extension to the configured ServerConfig.
func (m *Manager) configFor(file string) (ServerConfig, error) {
	ext := strings.ToLower(filepath.Ext(file))
	if ext == "" {
		return ServerConfig{}, fmt.Errorf("%w: %s (no extension)", ErrNoServer, filepath.Base(file))
	}
	cfg, ok := m.Configs[ext]
	if !ok {
		return ServerConfig{}, fmt.Errorf("%w: %s", ErrNoServer, ext)
	}
	return cfg, nil
}

// spawn starts the language server subprocess and runs the LSP initialize
// handshake. Returns a fully-initialised Client on success.
func (m *Manager) spawn(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("empty command")
	}
	bin, err := exec.LookPath(cfg.Command[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrServerUnavailable, err)
	}
	cmd := exec.Command(bin, cfg.Command[1:]...)
	cmd.Dir = m.Workspace
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// stderr is captured but currently discarded. Servers like gopls emit
	// progress notifications there; surfacing them would clutter the
	// agent's tool output. A future enhancement can plumb them into the
	// event stream as KindNote.
	cmd.Stderr = os.NewFile(0, os.DevNull)
	if devnull, derr := os.OpenFile(os.DevNull, os.O_WRONLY, 0); derr == nil {
		cmd.Stderr = devnull
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Language, err)
	}

	rootURI := pathToURI(m.Workspace)
	client := NewClient(cfg.Language, rootURI, cmd, stdin, stdout)
	if err := client.Initialize(ctx); err != nil {
		_ = client.Shutdown(context.Background())
		return nil, fmt.Errorf("initialize %s: %w", cfg.Language, err)
	}
	return client, nil
}

// Shutdown cleanly stops every spawned server. Safe to call multiple
// times — subsequent calls are no-ops because the clients map is cleared
// after the first call.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	clients := m.clients
	m.clients = make(map[string]*Client)
	m.failed = make(map[string]error)
	m.mu.Unlock()

	var firstErr error
	for _, c := range clients {
		if err := c.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// HasServerFor reports whether the manager is configured to handle `file`
// without checking PATH. Used by the tool layer to decide whether to
// return ErrNoServer ("language not supported in this build") vs let
// ClientFor return ErrServerUnavailable ("language supported but install
// the binary").
func (m *Manager) HasServerFor(file string) bool {
	_, err := m.configFor(file)
	return err == nil
}

// ActiveClients returns a snapshot of every spawned (warm) client. Used
// by workspace_symbols, which has no file to extension-route against and
// therefore queries every already-spawned server. Returns the underlying
// pointers (not copies) — callers must not mutate Client state but may
// call its methods concurrently.
func (m *Manager) ActiveClients() []*Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		out = append(out, c)
	}
	return out
}
