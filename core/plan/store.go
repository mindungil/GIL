package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrTooDeep is returned by validateAndAssignIDs when an Item is nested
// more than one level (Sub on a sub-item). Only one level is allowed.
var ErrTooDeep = errors.New("plan: sub-items may not nest deeper than one level")

// ErrInvalidStatus is returned when a tool caller supplies a status
// outside {pending, in_progress, completed}. Empty string is treated as
// pending by Normalize.
var ErrInvalidStatus = errors.New("plan: invalid status (must be pending|in_progress|completed)")

// Store persists per-session plans to <Dir>/<sessionID>/plan.json.
//
// Concurrency: a single global lock serialises all Save operations so
// concurrent calls (e.g. parallel sub-agents both calling the plan
// tool) don't interleave atomic-rename windows. Reads do not hold the
// lock — JSON unmarshal of a fully-written file is safe.
type Store struct {
	Dir string

	mu sync.Mutex
}

// NewStore returns a Store rooted at dir (typically layout.SessionsDir()).
func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

// path returns the on-disk plan.json for sessionID.
func (s *Store) path(sessionID string) string {
	return filepath.Join(s.Dir, sessionID, "plan.json")
}

// Load returns the plan for sessionID. When the file is missing returns
// an empty plan (Items==nil, Version==0) — never an error. Callers that
// want to distinguish "missing" from "empty" should stat the path first.
func (s *Store) Load(sessionID string) (*Plan, error) {
	if sessionID == "" {
		return nil, errors.New("plan.Store.Load: empty session ID")
	}
	body, err := os.ReadFile(s.path(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return &Plan{SessionID: sessionID}, nil
		}
		return nil, fmt.Errorf("plan.Store.Load: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("plan.Store.Load: unmarshal: %w", err)
	}
	if p.SessionID == "" {
		p.SessionID = sessionID
	}
	return &p, nil
}

// Save writes p atomically (tmpfile + rename) and returns the version
// it stamped (incremented from the on-disk version, or 1 for a brand
// new file). p.SessionID must match the on-disk dir owner; if empty,
// Save fills it from sessionID.
//
// Mutating side effects on p:
//   - Items are normalized: blank statuses become Pending, sub-items
//     deeper than one level cause ErrTooDeep.
//   - IDs are assigned to any Item / sub-Item whose ID is empty. The
//     scheme is "i1", "i2", ... (top-level) and "i1.1", "i1.2", ... for
//     sub-items, scoped per-save.
//   - UpdatedAt is set to now() (UTC).
//   - Version is set to (previous version + 1).
func (s *Store) Save(sessionID string, p *Plan) error {
	if sessionID == "" {
		return errors.New("plan.Store.Save: empty session ID")
	}
	if p == nil {
		return errors.New("plan.Store.Save: nil plan")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Normalize + validate.
	if err := normalizeAndAssignIDs(p.Items); err != nil {
		return err
	}

	// Bump version off of whatever's currently on disk.
	prev, _ := s.loadLocked(sessionID)
	p.SessionID = sessionID
	p.UpdatedAt = time.Now().UTC()
	if prev != nil {
		p.Version = prev.Version + 1
	} else {
		p.Version = 1
	}

	dir := filepath.Join(s.Dir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("plan.Store.Save: mkdir: %w", err)
	}

	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("plan.Store.Save: marshal: %w", err)
	}
	body = append(body, '\n')

	final := s.path(sessionID)
	tmp, err := os.CreateTemp(dir, "plan.json.tmp.*")
	if err != nil {
		return fmt.Errorf("plan.Store.Save: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails — Rename will replace
	// the tmp atomically on success, so the os.Remove is a no-op then.
	defer os.Remove(tmpName)

	if _, werr := tmp.Write(body); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("plan.Store.Save: write: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("plan.Store.Save: close: %w", cerr)
	}
	// Plan content is not secret — 0644 matches memory bank files.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("plan.Store.Save: chmod: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("plan.Store.Save: rename: %w", err)
	}
	return nil
}

// loadLocked is Load minus the input checks; called internally while the
// store lock is held so Save can read the previous version atomically.
func (s *Store) loadLocked(sessionID string) (*Plan, error) {
	body, err := os.ReadFile(s.path(sessionID))
	if err != nil {
		return nil, err
	}
	var p Plan
	if uerr := json.Unmarshal(body, &p); uerr != nil {
		return nil, uerr
	}
	return &p, nil
}

// Clear removes the plan.json for sessionID. Missing file is not an
// error. Used by tests; production code prefers Save with empty Items
// so the version counter keeps increasing.
func (s *Store) Clear(sessionID string) error {
	if sessionID == "" {
		return errors.New("plan.Store.Clear: empty session ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("plan.Store.Clear: %w", err)
	}
	return nil
}

// normalizeAndAssignIDs walks items in place: empty statuses become
// Pending, invalid statuses error out, sub-items deeper than one level
// error out, and missing IDs get assigned. Sub-item IDs are namespaced
// under their parent ("i1.1", "i1.2", ...) so they remain stable when
// the agent reorders top-level items between calls.
func normalizeAndAssignIDs(items []Item) error {
	topCounter := 0
	for i := range items {
		if err := normalizeItem(&items[i], false); err != nil {
			return err
		}
		if items[i].ID == "" {
			topCounter++
			items[i].ID = fmt.Sprintf("i%d", topCounter)
		} else {
			// If the agent passed an ID that looks like ours, advance
			// the counter past it so newly assigned IDs don't collide.
			if n, ok := parseTopID(items[i].ID); ok && n > topCounter {
				topCounter = n
			}
		}
		// Sub-items.
		subCounter := 0
		for j := range items[i].Sub {
			if err := normalizeItem(&items[i].Sub[j], true); err != nil {
				return err
			}
			if items[i].Sub[j].ID == "" {
				subCounter++
				items[i].Sub[j].ID = fmt.Sprintf("%s.%d", items[i].ID, subCounter)
			}
		}
	}
	return nil
}

// normalizeItem fills a blank status with Pending and rejects invalid
// statuses + over-deep nesting. When isSub is true and the item carries
// further Sub items, returns ErrTooDeep.
func normalizeItem(it *Item, isSub bool) error {
	if it.Status == "" {
		it.Status = Pending
	}
	if !it.Status.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidStatus, string(it.Status))
	}
	if isSub && len(it.Sub) > 0 {
		return ErrTooDeep
	}
	return nil
}

// parseTopID extracts the numeric suffix of "iN" (returns N, true). For
// any other shape returns (0, false). Used so callers that pre-assign
// human-readable IDs ("plan-step", "x42") don't accidentally collide
// with the auto-generated counter on the next normalization pass.
func parseTopID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'i' {
		return 0, false
	}
	n := 0
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
