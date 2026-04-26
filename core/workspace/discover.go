// Package workspace resolves the project root for a gil session and
// surfaces the project-local `.gil/` overlay (config.toml, mcp.toml,
// AGENTS.md). Discovery walks upward from the user's current working
// directory looking for a marker that establishes "this is a project I
// am working on" — the same trick aider, opencode, and Cline use to
// pick a stable root regardless of where inside the tree the user
// invoked the CLI.
//
// The walk order is intentionally aligned with how aider's main.py
// ranks default config files (cwd → git_root → home, then reversed so
// the closest wins) and how opencode and Cline anchor their per-project
// state under the closest VCS / package-manager directory. References:
//
//   - /home/ubuntu/research/aider/aider/main.py:464-498 — `.aider.conf.yml`
//     search order is cwd, then `git_root`, then `Path.home()`. We add
//     `.gil/` ahead of `.git/` so that an explicit gil project (the
//     user has already initialised one with `gil init` or similar)
//     takes precedence over its enclosing repo.
//   - /home/ubuntu/research/opencode/packages/opencode/src/global/index.ts —
//     opencode keeps a single `xdg-basedir`-derived config root and a
//     per-project state directory; the project root itself is implied by
//     the cwd because Bun resolves the binary relative to it.
//   - /home/ubuntu/research/cline/src/core/context/instructions/user-instructions/cline-rules.ts —
//     `.clinerules` is discovered relative to the workspace folder; the
//     workspace folder is whatever VS Code opened, which is morally the
//     same as the topmost ancestor with a project marker.
//
// Discover never escapes the user's $HOME — even if no marker is found
// before reaching it, the walk returns the original cwd rather than
// reading `/etc/.gil/config.toml` (or anything else outside the user's
// directory). This mirrors aider's reliance on `Path.home()` as the
// outermost layer and keeps surprise minimal on multi-tenant boxes.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// markerNames lists the directory / file names that flag a project
// root, in priority order. `.gil/` wins because its presence is an
// explicit user opt-in: the only way that directory exists is if the
// user (or a previous gil run) put it there. `.git/` is the next
// strongest signal — the same heuristic aider, codex, and almost every
// other coding agent uses. The trailing entries are package-manager
// manifests that pin a module / package root in language ecosystems
// where `.git/` may live one level higher (monorepos, vendored repos).
var markerNames = []string{
	".gil",
	".git",
	"package.json",
	"go.mod",
	"Cargo.toml",
	"pyproject.toml",
}

// Discover walks upward from cwd looking for the closest project-root
// marker (see markerNames). The first ancestor containing any marker
// wins; if no marker is found before the walk crosses $HOME, the walk
// stops and Discover returns cwd unchanged.
//
// `cwd` is taken verbatim — callers are responsible for resolving
// symlinks if they care (the walk uses lexical parents).
//
// Errors are returned only when the cwd itself cannot be statted or
// $HOME cannot be resolved. A "no marker found" outcome is not an
// error; it returns (cwd, nil) so callers can keep going with the
// hardcoded defaults.
func Discover(cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("workspace: cwd is empty")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve cwd: %w", err)
	}

	// Resolve the safety boundary. We never walk above $HOME because
	// project markers in /, /etc, /var, etc. would let one user read
	// another's settings (or worse, let a sandbox escape via a
	// `/etc/.gil/config.toml`). When HOME is unset (uncommon — unset
	// only in tightly-controlled containers) we fall back to "no
	// boundary" and walk to the filesystem root, which is the
	// principle-of-least-surprise behaviour for those environments.
	home, _ := os.UserHomeDir()

	dir := abs
	for {
		for _, name := range markerNames {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return dir, nil
			}
		}
		// Stop conditions, in order of safety:
		//   1. We just checked $HOME itself — go no higher (would leak
		//      onto a shared user's parent).
		//   2. We've reached the filesystem root.
		if home != "" && dir == home {
			return abs, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
}

// LocalDir returns "<workspace>/.gil". It does not check whether the
// directory exists; pair with IsConfigured for that check.
func LocalDir(workspace string) string {
	return filepath.Join(workspace, ".gil")
}

// IsConfigured reports whether the workspace has a project-local `.gil/`
// directory. Used by SENSING / interview to decide whether to read
// project overrides at all.
func IsConfigured(workspace string) bool {
	info, err := os.Stat(LocalDir(workspace))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// LocalConfigFile returns "<workspace>/.gil/config.toml". The file may
// not exist; Resolve treats a missing file the same as an empty one.
func LocalConfigFile(workspace string) string {
	return filepath.Join(LocalDir(workspace), "config.toml")
}

// LocalMCPFile returns "<workspace>/.gil/mcp.toml". The mcp registry
// (Track B) is the consumer; the path is co-located here so workspace
// callers don't have to reach into another package for the convention.
func LocalMCPFile(workspace string) string {
	return filepath.Join(LocalDir(workspace), "mcp.toml")
}

// LocalAgentsFile returns "<workspace>/.gil/AGENTS.md". The
// instructions package's tree-walk already picks up
// "<workspace>/AGENTS.md" at the workspace root, so this path is the
// fallback location for users who prefer to keep gil-specific guidance
// out of the repo's top-level AGENTS.md (which other tools also read).
func LocalAgentsFile(workspace string) string {
	return filepath.Join(LocalDir(workspace), "AGENTS.md")
}
