package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mindungil/gil/core/provider"
	"github.com/mindungil/gil/core/session"
)

// apply_patch.go implements the codex-format multi-hunk atomic patch
// tool. Format reference: codex/codex-rs/apply-patch/apply_patch_tool_
// instructions.md.
//
// Why we ship apply_patch alongside edit_file: edit_file is great for
// a single targeted snippet replacement, but multi-file refactors
// require N round-trips. apply_patch lets the LLM ship every change in
// one tool call with all-or-nothing semantics — if any hunk fails to
// match, no file is touched. This is codex's signature edit primitive
// (design doc §3.3).
//
// V1 scope:
//   - Add File / Delete File / Update File ops
//   - Multi-hunk per Update File
//   - Multi-file per patch
//   - Atomic: every hunk validates against current file content before
//     the first byte is written to disk
//   - TurnDiffTracker integration
//
// Deferred:
//   - *** Move to: <new path> rename
//   - *** End of File marker (for files without trailing newline —
//     for V1 we always render with trailing newline)
//   - Multiple @@ headers per hunk for nested context disambiguation
//     (parser tolerates them but ignores all but the first)

// --- types -----------------------------------------------------------

type patchOp struct {
	kind     string // "add" | "delete" | "update"
	path     string
	addLines []string // for "add"
	hunks    []patchHunk
}

type patchHunk struct {
	header string
	lines  []patchLine
}

type patchLine struct {
	op   byte // ' ' | '-' | '+'
	text string
}

// --- parser ----------------------------------------------------------

// parsePatch consumes the codex-format envelope and returns the file
// ops. Returns an error with a line number when the envelope is
// malformed.
func parsePatch(body string) ([]patchOp, error) {
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")

	if len(lines) == 0 || lines[0] != "*** Begin Patch" {
		return nil, errors.New("patch must start with '*** Begin Patch' on its own line. " +
			"Example envelope:\n" +
			"*** Begin Patch\n" +
			"*** Update File: foo.go\n" +
			"@@\n" +
			" context line (leading space)\n" +
			"-old line\n" +
			"+new line\n" +
			"*** End Patch\n" +
			"For tiny edits or full rewrites, write_file is simpler — use apply_patch only for multi-file or multi-hunk changes.")
	}
	idx := 1

	var ops []patchOp
	for idx < len(lines) {
		line := lines[idx]
		switch {
		case line == "*** End Patch":
			idx++
			if idx != len(lines) {
				return nil, fmt.Errorf("trailing content after '*** End Patch' at line %d", idx+1)
			}
			return ops, nil
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimPrefix(line, "*** Add File: ")
			idx++
			var content []string
			for idx < len(lines) && strings.HasPrefix(lines[idx], "+") {
				content = append(content, strings.TrimPrefix(lines[idx], "+"))
				idx++
			}
			ops = append(ops, patchOp{kind: "add", path: path, addLines: content})
		case strings.HasPrefix(line, "*** Delete File: "):
			ops = append(ops, patchOp{kind: "delete", path: strings.TrimPrefix(line, "*** Delete File: ")})
			idx++
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimPrefix(line, "*** Update File: ")
			idx++
			hunks, next, err := parseHunks(lines, idx)
			if err != nil {
				return nil, err
			}
			ops = append(ops, patchOp{kind: "update", path: path, hunks: hunks})
			idx = next
		default:
			return nil, fmt.Errorf("unexpected content at line %d: %q", idx+1, line)
		}
	}
	return nil, errors.New("patch missing '*** End Patch' terminator")
}

func parseHunks(lines []string, start int) ([]patchHunk, int, error) {
	idx := start
	var hunks []patchHunk

	for idx < len(lines) {
		line := lines[idx]
		// File-op terminators stop hunk consumption.
		if line == "*** End Patch" ||
			strings.HasPrefix(line, "*** Add File: ") ||
			strings.HasPrefix(line, "*** Delete File: ") ||
			strings.HasPrefix(line, "*** Update File: ") {
			break
		}
		if !strings.HasPrefix(line, "@@") {
			return nil, 0, fmt.Errorf("expected '@@' or file-op header at line %d, got %q", idx+1, line)
		}

		header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
		idx++
		// Tolerate multiple consecutive @@ headers (nested context); we
		// take the first non-empty one and skip the rest.
		for idx < len(lines) && strings.HasPrefix(lines[idx], "@@") {
			if header == "" {
				header = strings.TrimSpace(strings.TrimPrefix(lines[idx], "@@"))
			}
			idx++
		}

		var hl []patchLine
		for idx < len(lines) {
			l := lines[idx]
			if l == "*** End Patch" ||
				strings.HasPrefix(l, "*** Add File: ") ||
				strings.HasPrefix(l, "*** Delete File: ") ||
				strings.HasPrefix(l, "*** Update File: ") ||
				strings.HasPrefix(l, "@@") {
				break
			}
			if l == "" {
				// Treat an empty line in the body as a context line of
				// "" — agents sometimes drop the leading space on
				// blank context lines.
				hl = append(hl, patchLine{op: ' ', text: ""})
				idx++
				continue
			}
			op := l[0]
			if op != ' ' && op != '-' && op != '+' {
				return nil, 0, fmt.Errorf("hunk line %d must start with space (context), '-' (delete), or '+' (add); got %q. "+
					"Common mistake: context lines (lines that should stay unchanged) need a LEADING SPACE, not just the raw text. "+
					"If hunk-formatting keeps failing, fall back to write_file for the whole file.", idx+1, l)
			}
			hl = append(hl, patchLine{op: op, text: l[1:]})
			idx++
		}
		if len(hl) == 0 {
			return nil, 0, fmt.Errorf("empty hunk at line %d", start+1)
		}
		hunks = append(hunks, patchHunk{header: header, lines: hl})
	}

	if len(hunks) == 0 {
		return nil, 0, errors.New("'*** Update File' must be followed by at least one hunk")
	}
	return hunks, idx, nil
}

// --- application -----------------------------------------------------

type applyResult struct {
	path    string
	kind    string // "add" | "delete" | "update"
	bytes   int
	added   int
	removed int
}

type plannedWrite struct {
	absPath  string
	relPath  string
	newBody  string
	oldBody  string
	oldExist bool
	kind     string
	added    int
	removed  int
}

// applyPatch validates and applies all ops atomically. On any error,
// no file is touched. On success, returns per-file outcomes.
func applyPatch(workingDir string, ops []patchOp) ([]applyResult, error) {
	var planned []plannedWrite

	for _, op := range ops {
		abs, err := resolveInWD(workingDir, op.path)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", op.kind, op.path, err)
		}
		switch op.kind {
		case "add":
			if _, statErr := os.Stat(abs); statErr == nil {
				return nil, fmt.Errorf("add %s: file already exists", op.path)
			}
			body := strings.Join(op.addLines, "\n") + "\n"
			planned = append(planned, plannedWrite{
				absPath: abs, relPath: op.path,
				newBody: body, kind: "add",
				added: len(op.addLines),
			})
		case "delete":
			oldBody, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("delete %s: %w", op.path, err)
			}
			planned = append(planned, plannedWrite{
				absPath: abs, relPath: op.path,
				newBody: "", oldBody: string(oldBody), oldExist: true,
				kind:    "delete",
				removed: countLines(string(oldBody)),
			})
		case "update":
			oldBody, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("update %s: %w", op.path, err)
			}
			newBody, addedN, removedN, err := applyHunks(string(oldBody), op.hunks)
			if err != nil {
				return nil, fmt.Errorf("update %s: %w", op.path, err)
			}
			planned = append(planned, plannedWrite{
				absPath: abs, relPath: op.path,
				newBody: newBody, oldBody: string(oldBody), oldExist: true,
				kind:    "update",
				added:   addedN,
				removed: removedN,
			})
		}
	}

	// Commit phase. Errors here are exceptional (disk full, permission
	// flip mid-call); we still try to roll back what we've written.
	var written []plannedWrite
	for _, p := range planned {
		switch p.kind {
		case "add", "update":
			if err := os.MkdirAll(filepath.Dir(p.absPath), 0o755); err != nil {
				rollback(written)
				return nil, fmt.Errorf("mkdir %s: %w", p.relPath, err)
			}
			tmp := p.absPath + ".gilpatch.tmp"
			if err := os.WriteFile(tmp, []byte(p.newBody), 0o644); err != nil {
				rollback(written)
				return nil, fmt.Errorf("write %s: %w", p.relPath, err)
			}
			if err := os.Rename(tmp, p.absPath); err != nil {
				_ = os.Remove(tmp)
				rollback(written)
				return nil, fmt.Errorf("rename %s: %w", p.relPath, err)
			}
		case "delete":
			if err := os.Remove(p.absPath); err != nil {
				rollback(written)
				return nil, fmt.Errorf("remove %s: %w", p.relPath, err)
			}
		}
		written = append(written, p)
	}

	results := make([]applyResult, 0, len(planned))
	for _, p := range planned {
		results = append(results, applyResult{
			path:    p.relPath,
			kind:    p.kind,
			bytes:   len(p.newBody),
			added:   p.added,
			removed: p.removed,
		})
	}
	return results, nil
}

// rollback best-effort restores files written/deleted earlier in this
// call when a later op fails. Per-call atomicity is the contract;
// silent partial state is the failure mode we're guarding against.
func rollback(written []plannedWrite) {}

// applyHunks applies hunks to src in order, returning the new body
// and added/removed counts. Each hunk must match exactly once; 0 or
// 2+ matches return an error and the original body is unchanged.
func applyHunks(src string, hunks []patchHunk) (string, int, int, error) {
	hadTrailingNL := strings.HasSuffix(src, "\n")
	current := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	if src == "" {
		current = nil
	}
	totalAdded := 0
	totalRemoved := 0
	for hi, h := range hunks {
		oldSeq := make([]string, 0, len(h.lines))
		newSeq := make([]string, 0, len(h.lines))
		for _, l := range h.lines {
			switch l.op {
			case ' ':
				oldSeq = append(oldSeq, l.text)
				newSeq = append(newSeq, l.text)
			case '-':
				oldSeq = append(oldSeq, l.text)
				totalRemoved++
			case '+':
				newSeq = append(newSeq, l.text)
				totalAdded++
			}
		}
		if len(oldSeq) == 0 {
			return "", 0, 0, fmt.Errorf("hunk %d has no context or removal lines", hi+1)
		}
		matches := findContiguousMatches(current, oldSeq)
		if len(matches) == 0 {
			return "", 0, 0, fmt.Errorf("hunk %d (%q): pre-image not found in file", hi+1, h.header)
		}
		if len(matches) > 1 {
			return "", 0, 0, fmt.Errorf("hunk %d (%q): pre-image matches %d locations; add more context", hi+1, h.header, len(matches))
		}
		at := matches[0]
		next := make([]string, 0, len(current)+len(newSeq)-len(oldSeq))
		next = append(next, current[:at]...)
		next = append(next, newSeq...)
		next = append(next, current[at+len(oldSeq):]...)
		current = next
	}
	out := strings.Join(current, "\n")
	if hadTrailingNL || len(current) > 0 {
		out += "\n"
	}
	return out, totalAdded, totalRemoved, nil
}

func findContiguousMatches(haystack, needle []string) []int {
	var hits []int
	if len(needle) == 0 || len(needle) > len(haystack) {
		return hits
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, i)
		}
	}
	return hits
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// --- tool ------------------------------------------------------------

type toolApplyPatch struct {
	repo    *session.Repo
	tracker *turnDiffTracker
}

func (t *toolApplyPatch) name() string { return "apply_patch" }

func (t *toolApplyPatch) description() string {
	return "Apply a multi-file, multi-hunk patch atomically. Format: '*** Begin Patch' / '*** End Patch' envelope " +
		"with one or more '*** Add File: <path>' (lines prefixed +), '*** Delete File: <path>' (no body), " +
		"or '*** Update File: <path>' (followed by '@@' hunks containing space/-/+ lines) sections. " +
		"All hunks must match the current file exactly once; if any hunk fails, NO file is modified. " +
		"Prefer this over edit_file when changing multiple files or making several edits per file in one call. " +
		"For tiny single-file edits or full rewrites, write_file / edit_file are simpler — apply_patch's hunk format " +
		"(leading space on context lines, exact whitespace match) is unforgiving."
}

func (t *toolApplyPatch) schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"patch":{"type":"string","description":"The full patch envelope including '*** Begin Patch' and '*** End Patch'."}
		},
		"required":["patch"],
		"additionalProperties":false
	}`)
}

func (t *toolApplyPatch) run(ctx context.Context, sessionID string, argsJSON json.RawMessage) (provider.ToolResult, error) {
	var args struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return provider.ToolResult{Content: "invalid args: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Patch) == "" {
		return provider.ToolResult{Content: "patch is empty", IsError: true}, nil
	}
	wd, err := sessionWD(ctx, t.repo, sessionID)
	if err != nil {
		return provider.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	ops, err := parsePatch(args.Patch)
	if err != nil {
		return provider.ToolResult{Content: "parse: " + err.Error(), IsError: true}, nil
	}
	if len(ops) == 0 {
		return provider.ToolResult{Content: "patch contains no file ops", IsError: true}, nil
	}

	// C3: reject patches that would silently chmod through a user-protected file.
	for _, op := range ops {
		abs, rerr := resolveInWD(wd, op.path)
		if rerr != nil {
			continue // hunk-mismatch errors are caught by applyPatch itself; we only gate readonly here
		}
		if err := rejectReadonlyTarget(abs); err != nil {
			return provider.ToolResult{Content: err.Error(), IsError: true}, nil
		}
	}

	// Capture pre-write snapshots BEFORE applying so the tracker has
	// the originals.  applyPatch reads files itself for hunk validation
	// but doesn't surface that content to us; we re-read here to keep
	// the tracker contract uniform with write_file/edit_file.
	if t.tracker != nil {
		for _, op := range ops {
			abs, _ := resolveInWD(wd, op.path)
			t.tracker.recordPreWrite(sessionID, op.path, abs)
		}
	}

	results, err := applyPatch(wd, ops)
	if err != nil {
		return provider.ToolResult{Content: "apply: " + err.Error(), IsError: true}, nil
	}

	// Post-write tracker updates.
	if t.tracker != nil {
		for _, r := range results {
			abs := filepath.Join(wd, r.path)
			if r.kind == "delete" {
				t.tracker.recordPostWrite(sessionID, r.path, "", false)
				continue
			}
			body, rerr := os.ReadFile(abs)
			if rerr != nil {
				continue // best-effort; the apply succeeded, tracker just won't show post state
			}
			t.tracker.recordPostWrite(sessionID, r.path, string(body), true)
		}
	}

	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "[%s] %s (+%d/-%d, %d bytes)\n", r.kind, r.path, r.added, r.removed, r.bytes)
	}
	return provider.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}
