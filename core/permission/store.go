package permission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"
)

// PersistDecision is the user's choice when answering a permission_ask,
// including their persistence intent. The TUI/CLI layer maps a UI choice
// (single keystroke) to one of these values; RunService dispatches the
// allow/deny outcome to the runner AND, when the persistence is
// "session" or "always", records the rule in the appropriate store.
//
// Naming follows codex's `ApprovedForSession` 3-tier model
// (/home/ubuntu/research/codex/codex-rs/protocol/src/protocol.rs) but
// expanded to mirror cline's symmetric allow/deny lists
// (/home/ubuntu/research/cline/src/core/permissions/CommandPermissionController.ts).
//
// Decision (Phase 4 enum, kept for backwards compatibility) describes the
// runtime outcome of the rule evaluator (Allow/Ask/Deny). PersistDecision
// describes the user's *answer* and is intentionally a separate type so we
// don't conflate "what the evaluator decided" with "what the user told us
// to remember".
type PersistDecision string

const (
	PersistAllowOnce    PersistDecision = "allow_once"    // ephemeral; no storage write
	PersistAllowSession PersistDecision = "allow_session" // session-scoped (in-memory)
	PersistAllowAlways  PersistDecision = "allow_always"  // persisted to disk
	PersistDenyOnce     PersistDecision = "deny_once"     // ephemeral
	PersistDenySession  PersistDecision = "deny_session"  // session-scoped (in-memory)
	PersistDenyAlways   PersistDecision = "deny_always"   // persisted to disk
)

// IsAllow reports whether d is one of the three allow tiers.
func (d PersistDecision) IsAllow() bool {
	return d == PersistAllowOnce || d == PersistAllowSession || d == PersistAllowAlways
}

// IsDeny reports whether d is one of the three deny tiers.
func (d PersistDecision) IsDeny() bool {
	return d == PersistDenyOnce || d == PersistDenySession || d == PersistDenyAlways
}

// IsSession reports whether the decision should be recorded in the
// per-session in-memory list (for the lifetime of the run).
func (d PersistDecision) IsSession() bool {
	return d == PersistAllowSession || d == PersistDenySession
}

// IsAlways reports whether the decision should be persisted to disk so
// future runs against the same project skip the prompt entirely.
func (d PersistDecision) IsAlways() bool {
	return d == PersistAllowAlways || d == PersistDenyAlways
}

// ProjectRules is the on-disk shape for one project's persistent
// allow/deny lists. Patterns use the same wildcard semantics as the rest
// of the package (see wildcard.go) — most users will write literal
// command shapes ("git status") but globs ("ls *", "git diff *") are
// supported and idiomatic.
type ProjectRules struct {
	AlwaysAllow []string `toml:"always_allow"`
	AlwaysDeny  []string `toml:"always_deny"`
}

// storeFile is the wire / disk shape: a top-level `[project]` table
// keyed by absolute workspace path. BurntSushi/toml supports quoted keys
// containing slashes, which is what makes the
// `[project."/abs/path/to/proj"]` form work cleanly.
type storeFile struct {
	Project map[string]*ProjectRules `toml:"project"`
}

// PersistentStore reads / writes the always-allow / always-deny rules to
// a TOML file (typically `$XDG_STATE_HOME/gil/permissions.toml`). Rules
// are keyed by absolute workspace path: a permission granted in project
// A does NOT carry over to project B, even when the same command shape
// is involved. This is a deliberate defence-in-depth choice — a user who
// approved `rm -rf ./build` for one repo should not see that auto-allow
// the same command in an unrelated repo.
//
// Reference: codex's per-project auto-approval list
// (/home/ubuntu/research/codex/codex-rs/protocol/src/protocol.rs) and
// cline's CommandPermissionController allow/deny shape
// (/home/ubuntu/research/cline/src/core/permissions/CommandPermissionController.ts).
type PersistentStore struct {
	// Path is the absolute path to the TOML file. The directory must
	// already exist (callers typically pass `layout.State` which is
	// guaranteed by paths.EnsureDirs).
	Path string

	mu sync.Mutex
}

// Load returns the rules for `project`. A missing file or missing
// project entry returns (nil, nil) — callers treat that the same as "no
// persistent rules" without branching on errors. Returns an error only
// when the file exists but cannot be read or parsed (which signals a
// corrupted store the user must fix manually).
//
// The returned *ProjectRules is a fresh copy — mutating it does not
// affect the on-disk state until Append/Remove is called.
func (s *PersistentStore) Load(project string) (*ProjectRules, error) {
	if !filepath.IsAbs(project) {
		return nil, fmt.Errorf("permission: project key %q must be absolute", project)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	rules, ok := file.Project[project]
	if !ok {
		return nil, nil
	}
	// Defensive copy.
	out := &ProjectRules{
		AlwaysAllow: append([]string(nil), rules.AlwaysAllow...),
		AlwaysDeny:  append([]string(nil), rules.AlwaysDeny...),
	}
	return out, nil
}

// Append adds `pattern` to the named list ("always_allow" or
// "always_deny") under `project`. Duplicates are silently skipped (a
// rule is either present or not — appending it twice has no effect).
//
// The on-disk file is rewritten atomically: write to a sibling temp file
// and rename. Mode is 0600 because the file may contain command shapes
// (mildly sensitive — e.g., a deploy script with embedded host names).
func (s *PersistentStore) Append(project, list, pattern string) error {
	if !filepath.IsAbs(project) {
		return fmt.Errorf("permission: project key %q must be absolute", project)
	}
	if pattern == "" {
		return errors.New("permission: pattern is empty")
	}
	if list != "always_allow" && list != "always_deny" {
		return fmt.Errorf("permission: unknown list %q (want always_allow|always_deny)", list)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.readLocked()
	if err != nil {
		return err
	}
	rules := file.Project[project]
	if rules == nil {
		rules = &ProjectRules{}
		file.Project[project] = rules
	}
	target := &rules.AlwaysAllow
	if list == "always_deny" {
		target = &rules.AlwaysDeny
	}
	for _, existing := range *target {
		if existing == pattern {
			return nil // already present; no-op
		}
	}
	*target = append(*target, pattern)
	return s.writeLocked(file)
}

// Remove deletes `pattern` from the named list under `project`. A
// missing pattern is not an error (idempotent — the caller's intent is
// "make sure this rule is not present", which is already true).
//
// Removing the last rule from a project leaves an empty `[project."..."]`
// table. We keep it intentionally so the user can see the project once
// had rules; `gil permissions list` will simply show no entries. (A
// future cleanup pass can prune empty entries if the file ever grows
// unwieldy — at the moment, persistent permission stores stay small.)
func (s *PersistentStore) Remove(project, list, pattern string) error {
	if !filepath.IsAbs(project) {
		return fmt.Errorf("permission: project key %q must be absolute", project)
	}
	if list != "always_allow" && list != "always_deny" {
		return fmt.Errorf("permission: unknown list %q (want always_allow|always_deny)", list)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.readLocked()
	if err != nil {
		return err
	}
	rules := file.Project[project]
	if rules == nil {
		return nil
	}
	target := &rules.AlwaysAllow
	if list == "always_deny" {
		target = &rules.AlwaysDeny
	}
	out := (*target)[:0]
	removed := false
	for _, existing := range *target {
		if existing == pattern && !removed {
			removed = true
			continue
		}
		out = append(out, existing)
	}
	if !removed {
		return nil
	}
	*target = out
	return s.writeLocked(file)
}

// Projects returns every project key currently tracked in the store.
// Result is sorted for deterministic output (used by `gil permissions list`).
// Missing file returns an empty slice.
func (s *PersistentStore) Projects() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(file.Project))
	for k := range file.Project {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// readLocked reads and parses the TOML file. A missing file returns an
// empty (initialised) storeFile so callers can mutate-and-write without
// nil-checking. Caller must hold s.mu.
func (s *PersistentStore) readLocked() (*storeFile, error) {
	out := &storeFile{Project: map[string]*ProjectRules{}}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("permission: read store %q: %w", s.Path, err)
	}
	if len(data) == 0 {
		return out, nil
	}
	if err := toml.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("permission: parse store %q: %w", s.Path, err)
	}
	if out.Project == nil {
		out.Project = map[string]*ProjectRules{}
	}
	return out, nil
}

// writeLocked atomically writes file to disk with mode 0600. The
// directory containing s.Path is assumed to exist (paths.EnsureDirs
// guarantees it for $XDG_STATE_HOME/gil). Atomic = write to ".tmp" then
// os.Rename — same pattern used by event.Persister.
func (s *PersistentStore) writeLocked(file *storeFile) error {
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("permission: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we fail before rename.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("permission: chmod tmp: %w", err)
	}
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(file); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("permission: encode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("permission: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("permission: rename: %w", err)
	}
	tmpPath = "" // suppress cleanup
	return nil
}
