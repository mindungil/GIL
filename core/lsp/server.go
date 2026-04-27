package lsp

import (
	"errors"
	"os/exec"
)

// ServerConfig describes how to spawn one language server. The Manager
// looks up a config by file extension via DefaultServerConfigs (or a
// caller-supplied override on Manager.Configs).
//
// Available is a hook so callers (and tests) can override the
// default-of-"check exec.LookPath". Returning false makes the manager skip
// the spawn and return a friendly "install X to use LSP" message.
type ServerConfig struct {
	// Language is a stable identifier ("go", "python", "typescript",
	// "rust") used as a map key inside Manager.servers so multiple
	// extensions sharing one server (e.g. .ts + .tsx) reuse the same
	// process.
	Language string

	// LanguageID is the LSP textDocument/didOpen languageId field. Many
	// servers (gopls, rust-analyzer) ignore it, but pyright /
	// typescript-language-server use it for routing.
	LanguageID string

	// Command is the argv used to spawn the server. The first element is
	// resolved against $PATH; the rest are passed verbatim. Empty Command
	// makes Available return false unconditionally.
	Command []string

	// Available reports whether the server is installable and runnable.
	// Defaults to "exec.LookPath(Command[0]) succeeds".
	Available func() bool

	// InstallHint is shown to the agent when Available returns false.
	// Aesthetic-spec compliant: dim, single line, no emoji.
	InstallHint string
}

// availableViaPath is the default for ServerConfig.Available: the binary
// must resolve on $PATH.
func availableViaPath(cmd []string) func() bool {
	return func() bool {
		if len(cmd) == 0 {
			return false
		}
		_, err := exec.LookPath(cmd[0])
		return err == nil
	}
}

// IsAvailable applies the default when Available is nil so callers (and
// the manager) never have to repeat the LookPath check.
func (s ServerConfig) IsAvailable() bool {
	if s.Available != nil {
		return s.Available()
	}
	return availableViaPath(s.Command)()
}

// DefaultServerConfigs maps file extensions to the language-server we
// should spawn for them. Keep entries sorted by language so the diff is
// readable when adding a new one.
//
// The mapping is intentionally minimal — Phase 18 supports the four
// languages the spec calls out (Go, Python, TypeScript/JavaScript, Rust).
// Additional languages can be wired by callers via Manager.Configs.
func DefaultServerConfigs() map[string]ServerConfig {
	go_ := ServerConfig{
		Language:    "go",
		LanguageID:  "go",
		Command:     []string{"gopls"},
		InstallHint: "install gopls: go install golang.org/x/tools/gopls@latest",
	}
	pyright := ServerConfig{
		Language:    "python",
		LanguageID:  "python",
		Command:     []string{"pyright-langserver", "--stdio"},
		InstallHint: "install pyright: npm install -g pyright (or pip install pyright)",
	}
	ts := ServerConfig{
		Language:    "typescript",
		LanguageID:  "typescript",
		Command:     []string{"typescript-language-server", "--stdio"},
		InstallHint: "install typescript-language-server: npm install -g typescript typescript-language-server",
	}
	rust := ServerConfig{
		Language:    "rust",
		LanguageID:  "rust",
		Command:     []string{"rust-analyzer"},
		InstallHint: "install rust-analyzer: rustup component add rust-analyzer",
	}
	return map[string]ServerConfig{
		".go":  go_,
		".py":  pyright,
		".ts":  ts,
		".tsx": withLangID(ts, "typescriptreact"),
		".js":  withLangID(ts, "javascript"),
		".jsx": withLangID(ts, "javascriptreact"),
		".mjs": withLangID(ts, "javascript"),
		".cjs": withLangID(ts, "javascript"),
		".mts": ts,
		".cts": ts,
		".rs":  rust,
	}
}

// withLangID returns a copy of cfg with LanguageID overridden — used so
// .tsx vs .ts both spawn the same typescript-language-server but with the
// right languageId on didOpen.
func withLangID(cfg ServerConfig, langID string) ServerConfig {
	cfg.LanguageID = langID
	return cfg
}

// ErrNoServer is returned when no language server is configured for a file
// extension (the agent gets a "language not supported by this build" hint).
var ErrNoServer = errors.New("no language server configured for this file extension")

// ErrServerUnavailable is returned when a server is configured but its
// binary isn't on $PATH (the agent gets the InstallHint).
var ErrServerUnavailable = errors.New("language server binary not found in PATH")
