package service

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/pmezard/go-difflib/difflib"
)

// turnDiffTracker accumulates per-session in-memory file deltas for the
// current chat turn. The chat write tools (write_file, edit_file,
// apply_patch) call recordPreWrite before mutating the FS and
// recordPostWrite after the mutation lands; show_diff drains the
// tracker via snapshot().
//
// Why in-memory rather than asking shadow-git: chat sessions don't
// produce shadow-git checkpoints (only run sessions do). Pre-tracker,
// show_diff returned "no checkpoints yet" for every chat session,
// silently hiding the agent's own edits from itself. The tracker is the
// chat-mode replacement — turn-scoped, no I/O on the read side.
//
// Reset semantics: the tracker is reset per session at the start of
// each SessionService.Prompt RPC (see session_prompt.go). Each turn
// shows only what *this* invocation of the agent did, not history.
//
// Limitations:
//   - run_bash output is not parsed back into the tracker. Instead the
//     polluted flag is set so show_diff can append a "fs may have
//     changed outside the tracker" note — the agent stays informed
//     without us re-implementing patch detection on arbitrary stdout.
//   - The Original snapshot is captured at first observation. Subsequent
//     edits on the same path mutate Current only. show_diff therefore
//     shows the net Original→Current delta, collapsing intermediate
//     states. This matches what the user actually wants to see.
type turnDiffTracker struct {
	mu       sync.Mutex
	sessions map[string]*sessionTurnDiff
}

type sessionTurnDiff struct {
	files    map[string]*fileTurnSnapshot
	polluted bool
}

type fileTurnSnapshot struct {
	Original        string
	OriginalExisted bool
	Current         string
	CurrentExists   bool
}

func newTurnDiffTracker() *turnDiffTracker {
	return &turnDiffTracker{sessions: make(map[string]*sessionTurnDiff)}
}

// reset drops all tracked state for sessionID. Called at the start of
// each Prompt turn so the tracker only ever shows the current turn's
// edits.
func (t *turnDiffTracker) reset(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, sessionID)
}

// markExternal flags that an external command (run_bash) has executed
// during this turn. show_diff appends a caveat so the agent knows the
// tracker may be incomplete.
func (t *turnDiffTracker) markExternal(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensure(sessionID).polluted = true
}

// recordPreWrite captures the pre-mutation state of a file. Idempotent
// per (sessionID, relPath): the first observation wins so that the
// Original snapshot reflects the file as it was at the *start* of the
// turn even after multiple edits.
//
// absPath is the real on-disk path used to read the original content;
// relPath is what show_diff will display. Pass empty content + existed=
// false when the file doesn't exist yet (e.g. write_file creating new
// file).
func (t *turnDiffTracker) recordPreWrite(sessionID, relPath, absPath string) {
	t.mu.Lock()
	s := t.ensure(sessionID)
	if _, ok := s.files[relPath]; ok {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	original := ""
	existed := false
	if absPath != "" {
		if b, err := os.ReadFile(absPath); err == nil {
			original = string(b)
			existed = true
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	s = t.ensure(sessionID)
	if _, ok := s.files[relPath]; ok {
		return
	}
	s.files[relPath] = &fileTurnSnapshot{
		Original:        original,
		OriginalExisted: existed,
	}
}

// recordPostWrite updates the current state after a successful write.
// If recordPreWrite wasn't called first the snapshot is created with an
// empty Original — the diff will then show the entire file as added,
// which is the right behavior when a prior step missed the pre-write
// hook.
func (t *turnDiffTracker) recordPostWrite(sessionID, relPath, newContent string, exists bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.ensure(sessionID)
	snap, ok := s.files[relPath]
	if !ok {
		snap = &fileTurnSnapshot{}
		s.files[relPath] = snap
	}
	snap.Current = newContent
	snap.CurrentExists = exists
}

func (t *turnDiffTracker) ensure(sessionID string) *sessionTurnDiff {
	s, ok := t.sessions[sessionID]
	if !ok {
		s = &sessionTurnDiff{files: make(map[string]*fileTurnSnapshot)}
		t.sessions[sessionID] = s
	}
	return s
}

// snapshot returns a copy of the current tracker state for sessionID.
// Returns nil files map when the session has no tracked deltas.
func (t *turnDiffTracker) snapshot(sessionID string) (files map[string]*fileTurnSnapshot, polluted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[sessionID]
	if !ok {
		return nil, false
	}
	out := make(map[string]*fileTurnSnapshot, len(s.files))
	for k, v := range s.files {
		cp := *v
		out[k] = &cp
	}
	return out, s.polluted
}

// renderUnifiedDiff produces a unified-diff string for one file. Empty
// when Original==Current (file touched but unchanged). Add/delete cases
// are tagged with /dev/null on the appropriate side, matching git's
// convention.
func renderUnifiedDiff(relPath string, snap *fileTurnSnapshot) string {
	if snap.Original == snap.Current && snap.OriginalExisted == snap.CurrentExists {
		return ""
	}
	from := "a/" + relPath
	to := "b/" + relPath
	if !snap.OriginalExisted {
		from = "/dev/null"
	}
	if !snap.CurrentExists {
		to = "/dev/null"
	}
	d := difflib.UnifiedDiff{
		A:        difflib.SplitLines(snap.Original),
		B:        difflib.SplitLines(snap.Current),
		FromFile: from,
		ToFile:   to,
		Context:  3,
	}
	out, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		return fmt.Sprintf("(diff render failed for %s: %v)\n", relPath, err)
	}
	return out
}

// renderTrackerSummary produces the show_diff body from a tracker
// snapshot. Returns ("", 0, 0, 0) when there are no diffable files.
func renderTrackerSummary(files map[string]*fileTurnSnapshot, polluted bool) (body string, fileCount, added, removed int) {
	if len(files) == 0 {
		if polluted {
			return "[no tracker deltas this turn — run_bash executed, fs state outside the tracker may have changed]", 0, 0, 0
		}
		return "", 0, 0, 0
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		d := renderUnifiedDiff(p, files[p])
		if d == "" {
			continue
		}
		fileCount++
		for _, line := range strings.Split(d, "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
				continue
			case strings.HasPrefix(line, "+"):
				added++
			case strings.HasPrefix(line, "-"):
				removed++
			}
		}
		b.WriteString(d)
	}
	if polluted {
		b.WriteString("\n[note: run_bash executed during this turn; fs changes outside the tracker may exist — use git status for the full picture]\n")
	}
	return strings.TrimRight(b.String(), "\n"), fileCount, added, removed
}
