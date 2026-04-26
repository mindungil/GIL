// Package instructions discovers persistent project context that other
// coding harnesses (Codex, Claude Code, opencode, Cline, Cursor) keep on
// disk under well-known filenames — primarily AGENTS.md, CLAUDE.md, and
// .cursor/rules/*.mdc. Discovered content is meant to be injected into
// the AgentLoop's system prompt so that the autonomous run inherits the
// user's persona, conventions, and project conventions without having to
// re-ask them at the interview stage.
//
// The walk order, framing, and per-file caps are deliberately aligned
// with codex-rs/core/src/agents_md.rs (tree walk to project root) and
// opencode/packages/opencode/src/session/instruction.ts (AGENTS+CLAUDE
// merge, OPENCODE_DISABLE_CLAUDE_CODE_PROMPT toggle, project-first
// precedence). See /home/ubuntu/research/{codex,opencode,cline} for the
// reference implementations.
//
// Discovery never aborts on missing or unreadable files: callers always
// receive a (possibly empty) []Source and a non-nil error only on
// catastrophic problems (e.g. workspace is itself unreadable). Empty
// files are dropped silently and oversized files are truncated.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one discovered instruction file.
type Source struct {
	Path   string  // absolute file path
	Origin string  // "workspace" | "ancestor" | "cursor" | "global" | "home"
	Body   string  // file contents (possibly truncated)
	SizeKB float64 // size of Body in kilobytes (1 KB = 1024 B)
}

// DiscoverOptions controls the tree walk.
type DiscoverOptions struct {
	// Workspace is the project root from which the walk starts. Required;
	// when empty Discover returns nil, nil so callers can pass through
	// "no workspace yet" without branching.
	Workspace string

	// StopAtGitRoot stops walking up at the first ancestor containing a
	// `.git` directory (inclusive). True is the right default for
	// monorepos — without it a deeply nested package would slurp instructions
	// from every parent shell. When false, the walk continues all the way to
	// the filesystem root.
	StopAtGitRoot bool

	// DisableClaudeMD skips CLAUDE.md at every layer of the walk. Mirrors
	// opencode's OPENCODE_DISABLE_CLAUDE_CODE_PROMPT flag — useful when the
	// user wants only AGENTS.md without the Anthropic-flavoured variant.
	DisableClaudeMD bool

	// DisableCursor skips .cursor/rules/*.mdc at every layer. Same shape as
	// DisableClaudeMD; mirrors what cline/cursor surface independently.
	DisableCursor bool

	// GlobalConfigDir, when non-empty, causes Discover to also read
	// $GlobalConfigDir/AGENTS.md as the global (lowest-but-one priority)
	// source. Typically set to paths.Layout.Config.
	GlobalConfigDir string

	// HomeDir, when non-empty, causes Discover to also read
	// $HomeDir/AGENTS.md as the lowest-priority source. Distinct from the
	// XDG global because some users keep one global AGENTS.md in $HOME by
	// hand. We mark this one with origin "home" so callers can warn that
	// it's outside the standard config location.
	HomeDir string

	// MaxBytes caps the total rendered size after concatenation. When the
	// sum of all sources exceeds this limit, Render drops lowest-priority
	// sources first (the slice is already in priority-low → priority-high
	// order). 0 means use the package default of 8 KB.
	MaxBytes int64
}

// Per-file cap. Hard-coded rather than exposed because a single 64 KB+
// instruction file is almost certainly a mistake (or worse, a leaked
// dependency lockfile renamed to AGENTS.md by accident).
const maxBytesPerFile = 64 * 1024

// Default total rendered budget.
const defaultMaxBytes = 8 * 1024

// Default ring of filenames per directory. Ordered AGENTS first because
// AGENTS.md is the cross-harness consensus filename and CLAUDE.md is the
// Anthropic-specific variant some teams keep alongside it.
var (
	agentsMDFilename = "AGENTS.md"
	claudeMDFilename = "CLAUDE.md"
	cursorRulesDir   = filepath.Join(".cursor", "rules")
)

// Discover walks the workspace + its ancestors looking for instruction
// files and returns them in priority-low → priority-high order. The
// caller can then concat (Render) and feed the result into a system
// prompt. Lower-priority sources appear first so the LLM ends up reading
// the most-specific (workspace-local) content last — which most
// instruction-following models give the most weight to.
//
// Concretely, the slice ordering is:
//
//  1. $HomeDir/AGENTS.md           (lowest)
//  2. $GlobalConfigDir/AGENTS.md
//  3. ancestors (git-root → workspace), each layer:
//     AGENTS.md, CLAUDE.md, .cursor/rules/*.mdc
//  4. workspace itself: AGENTS.md, CLAUDE.md, .cursor/rules/*.mdc (highest)
//
// When opts.Workspace is empty, Discover returns (nil, nil). Discovery
// never recurses below cursor/rules and never follows symlinks outside
// the workspace root (security: an AGENTS.md symlink pointing at
// /etc/passwd should not leak).
func Discover(opts DiscoverOptions) ([]Source, error) {
	if opts.Workspace == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return nil, fmt.Errorf("instructions: resolve workspace: %w", err)
	}
	wsInfo, err := os.Stat(abs)
	if err != nil {
		// Workspace itself missing is not fatal — caller may have passed
		// a path that hasn't been mkdir'd yet (e.g. interview-time).
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("instructions: stat workspace: %w", err)
	}
	if !wsInfo.IsDir() {
		return nil, fmt.Errorf("instructions: workspace is not a directory: %s", abs)
	}

	var sources []Source

	// 1. $HomeDir/AGENTS.md (lowest priority)
	if opts.HomeDir != "" {
		if s, ok := readFile(filepath.Join(opts.HomeDir, agentsMDFilename), "home"); ok {
			sources = append(sources, s)
		}
	}

	// 2. $GlobalConfigDir/AGENTS.md
	if opts.GlobalConfigDir != "" {
		if s, ok := readFile(filepath.Join(opts.GlobalConfigDir, agentsMDFilename), "global"); ok {
			sources = append(sources, s)
		}
	}

	// 3. Ancestors (excluding workspace), highest ancestor first so the
	//    workspace ends up last (= highest priority).
	ancestors := collectAncestors(abs, opts.StopAtGitRoot)
	for _, dir := range ancestors {
		layer := readLayer(dir, "ancestor", opts, abs)
		sources = append(sources, layer...)
	}

	// 4. Workspace layer (highest priority)
	wsLayer := readLayer(abs, "workspace", opts, abs)
	sources = append(sources, wsLayer...)

	return sources, nil
}

// Render concatenates Sources into a single string, framing each with
// BEGIN/END delimiters that include the origin and a workspace-relative
// (or basename-fallback) path so the model can cite which file said
// what. When the concatenation would exceed maxBytes, Render drops
// lowest-priority entries first (i.e. the front of the slice, which
// matches Discover's ordering convention). 0 means use the package
// default of 8 KB.
//
// The relpath used in delimiters is the source's basename when the
// workspace context isn't available — Render is intentionally simple and
// callers wanting a relative-to-workspace string should pre-rewrite
// Source.Path before calling. We chose basename over a synthesized
// abspath because the latter can leak machine usernames into the system
// prompt, which is a privacy footgun for shared transcripts.
func Render(sources []Source, maxBytes int64) string {
	if len(sources) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	// Drop lowest-priority sources first until the total fits. We render
	// each candidate fully then measure — simpler than streaming and the
	// budgets here are O(KB), not O(MB).
	keep := make([]Source, len(sources))
	copy(keep, sources)
	for {
		out := renderAll(keep)
		if int64(len(out)) <= maxBytes || len(keep) == 0 {
			return out
		}
		keep = keep[1:] // drop lowest-priority entry
	}
}

// renderAll is the unconditional concatenation used by Render.
func renderAll(sources []Source) string {
	var sb strings.Builder
	for _, s := range sources {
		label := filepath.Base(s.Path)
		sb.WriteString("--- BEGIN ")
		sb.WriteString(s.Origin)
		sb.WriteString(": ")
		sb.WriteString(label)
		sb.WriteString(" ---\n")
		sb.WriteString(s.Body)
		if !strings.HasSuffix(s.Body, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("--- END ")
		sb.WriteString(s.Origin)
		sb.WriteString(": ")
		sb.WriteString(label)
		sb.WriteString(" ---\n\n")
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// readLayer reads the per-directory ring (AGENTS.md, CLAUDE.md, cursor
// rules) at `dir` honouring opts.DisableClaudeMD / opts.DisableCursor.
// `origin` is the label slapped on every Source returned. `workspaceRoot`
// is used to refuse symlinks that escape the workspace; ancestors of the
// workspace are always allowed (we already walk up to git root).
func readLayer(dir, origin string, opts DiscoverOptions, workspaceRoot string) []Source {
	var out []Source

	// AGENTS.md
	if s, ok := readFile(filepath.Join(dir, agentsMDFilename), origin); ok {
		if !escapesWorkspace(s.Path, workspaceRoot, origin) {
			out = append(out, s)
		}
	}

	// CLAUDE.md (optional)
	if !opts.DisableClaudeMD {
		if s, ok := readFile(filepath.Join(dir, claudeMDFilename), origin); ok {
			if !escapesWorkspace(s.Path, workspaceRoot, origin) {
				out = append(out, s)
			}
		}
	}

	// .cursor/rules/*.mdc (optional)
	if !opts.DisableCursor {
		rulesDir := filepath.Join(dir, cursorRulesDir)
		entries, err := os.ReadDir(rulesDir)
		if err == nil {
			// Sort for deterministic concat order (alphabetical) — otherwise
			// the system prompt would shift between runs based on inode order.
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if !strings.HasSuffix(e.Name(), ".mdc") {
					continue
				}
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				if s, ok := readFile(filepath.Join(rulesDir, name), "cursor"); ok {
					if !escapesWorkspace(s.Path, workspaceRoot, "cursor") {
						out = append(out, s)
					}
				}
			}
		}
	}

	return out
}

// readFile reads `path` honouring the per-file cap, drops empty files,
// and returns ok=false on any I/O error or absent file. We deliberately
// do NOT bubble errors up the chain — Discover is best-effort and
// shouldn't fail a Run because the user happened to have a flaky NFS
// mount under a parent directory.
func readFile(path, origin string) (Source, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return Source{}, false
	}
	if info.IsDir() {
		return Source{}, false
	}
	if info.Size() == 0 {
		return Source{}, false
	}

	// Cap how much we read per file. Bigger than MaxBytes total to keep
	// behaviour predictable even when the caller raises MaxBytes for a
	// specific run; truncation is signposted in-band so the model knows
	// the body it sees is partial.
	limit := int64(maxBytesPerFile)
	truncated := false
	if info.Size() > limit {
		truncated = true
	}

	f, err := os.Open(path)
	if err != nil {
		return Source{}, false
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	body := string(buf[:n])
	if truncated {
		body += "\n\n... [truncated]"
	}
	if strings.TrimSpace(body) == "" {
		return Source{}, false
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return Source{
		Path:   abs,
		Origin: origin,
		Body:   body,
		SizeKB: float64(len(body)) / 1024.0,
	}, true
}

// collectAncestors returns absolute ancestor paths of `workspace` in
// highest-ancestor → closest-to-workspace order, stopping at the git
// root if requested. The workspace itself is NOT included (the caller
// reads that layer separately so it always lands at the very end of the
// priority list).
func collectAncestors(workspace string, stopAtGitRoot bool) []string {
	var ancestors []string
	cur := filepath.Dir(workspace)
	for cur != "" && cur != "/" && cur != filepath.Dir(cur) {
		ancestors = append(ancestors, cur)
		// Stop if we've just included a directory that itself is a git root.
		if stopAtGitRoot {
			if info, err := os.Stat(filepath.Join(cur, ".git")); err == nil && info.IsDir() {
				break
			}
		}
		cur = filepath.Dir(cur)
	}
	// Reverse so highest ancestor comes first (lowest priority of the
	// ancestor chain).
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	return ancestors
}

// escapesWorkspace returns true when `path` is a symlink whose resolved
// target sits outside `workspaceRoot`. Used as a security gate so a
// malicious AGENTS.md → /etc/passwd symlink in the workspace can't pull
// arbitrary files into the system prompt. Ancestor- and global-origin
// sources are not gated (they're outside the workspace by definition).
func escapesWorkspace(path, workspaceRoot, origin string) bool {
	if origin != "workspace" && origin != "cursor" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false // can't resolve → trust os.Stat already accepted it
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return true
	}
	return false
}
