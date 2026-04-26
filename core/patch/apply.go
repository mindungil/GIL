package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Applier applies a parsed Patch to disk under WorkspaceDir. When DryRun
// is true, no filesystem mutations occur; Result entries still describe
// what would have happened.
//
// Lifted from codex-rs/apply-patch/src/lib.rs apply_hunks_to_files +
// derive_new_contents_from_chunks. The Codex implementation uses an
// async ExecutorFileSystem abstraction; we use the synchronous os
// package directly (no async needed in our run model).
type Applier struct {
	WorkspaceDir string
	DryRun       bool
}

// Result describes the outcome of one Hunk application.
type Result struct {
	Hunk    Hunk
	Path    string // resolved absolute path
	Applied bool
	Err     error
}

// Apply walks p.Hunks in order. Each hunk is reported in Results regardless
// of success. On the first hunk error, subsequent hunks are still attempted
// (Codex bails on the first error; we continue to give the agent better
// per-hunk feedback). Use HasError() to test the aggregate.
func (a *Applier) Apply(p *Patch) []Result {
	out := make([]Result, 0, len(p.Hunks))
	for _, h := range p.Hunks {
		r := a.applyOne(h)
		out = append(out, r)
	}
	return out
}

// HasError reports whether any result in `results` has a non-nil Err.
func HasError(results []Result) bool {
	for _, r := range results {
		if r.Err != nil {
			return true
		}
	}
	return false
}

func (a *Applier) resolve(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(a.WorkspaceDir, rel)
}

func (a *Applier) applyOne(h Hunk) Result {
	abs := a.resolve(h.Path)
	res := Result{Hunk: h, Path: abs}

	switch h.Kind {
	case HunkAddFile:
		if !a.DryRun {
			if err := writeFileMkdir(abs, []byte(h.AddContents)); err != nil {
				res.Err = fmt.Errorf("add %s: %w", h.Path, err)
				return res
			}
		}
		res.Applied = true
		return res

	case HunkDeleteFile:
		if !a.DryRun {
			info, err := os.Stat(abs)
			if err != nil {
				res.Err = fmt.Errorf("delete %s: %w", h.Path, err)
				return res
			}
			if info.IsDir() {
				res.Err = fmt.Errorf("delete %s: path is a directory", h.Path)
				return res
			}
			if err := os.Remove(abs); err != nil {
				res.Err = fmt.Errorf("delete %s: %w", h.Path, err)
				return res
			}
		}
		res.Applied = true
		return res

	case HunkUpdateFile:
		original, err := os.ReadFile(abs)
		if err != nil {
			res.Err = fmt.Errorf("update %s: read: %w", h.Path, err)
			return res
		}
		newContents, applyErr := deriveNewContents(string(original), h.Chunks)
		if applyErr != nil {
			res.Err = fmt.Errorf("update %s: %w", h.Path, applyErr)
			return res
		}
		if !a.DryRun {
			destPath := abs
			if h.MovePath != "" {
				destPath = a.resolve(h.MovePath)
			}
			if err := writeFileMkdir(destPath, []byte(newContents)); err != nil {
				res.Err = fmt.Errorf("update %s: write: %w", h.Path, err)
				return res
			}
			if h.MovePath != "" && destPath != abs {
				// Remove the original after successful write to new path.
				if err := os.Remove(abs); err != nil {
					res.Err = fmt.Errorf("update %s: remove original after move: %w", h.Path, err)
					return res
				}
			}
		}
		res.Applied = true
		return res
	}

	res.Err = fmt.Errorf("unknown hunk kind %d", h.Kind)
	return res
}

// deriveNewContents walks each chunk, locating it via seekSequence on the
// change_context line (when present) then on the OldLines, and splices in the
// NewLines. The cursor advances after each chunk so subsequent chunks find
// their context further along.
//
// Mirrors codex-rs/apply-patch/src/lib.rs compute_replacements +
// apply_replacements, collapsed into a single in-order splice loop (we don't
// need the sort step because we apply in document order).
func deriveNewContents(original string, chunks []UpdateChunk) (string, error) {
	// Preserve trailing-newline state so we can restore it after joining.
	hadTrailingNL := strings.HasSuffix(original, "\n")
	lines := splitLines(original)

	cursor := 0
	for i, chunk := range chunks {
		// 1) If the chunk has a change_context, locate it first to anchor the
		//    cursor just past that context line.
		if chunk.ChangeContext != "" {
			ctxIdx := seekSequence(lines, []string{chunk.ChangeContext}, cursor, false)
			if ctxIdx < 0 {
				return "", fmt.Errorf("chunk %d: change_context %q not found", i+1, chunk.ChangeContext)
			}
			cursor = ctxIdx + 1
		}

		// 2) Pure-addition chunk: no old lines to locate.
		if len(chunk.OldLines) == 0 {
			// Insert at cursor position (mirrors Codex end-of-file insertion).
			newLines := make([]string, 0, len(lines)+len(chunk.NewLines))
			newLines = append(newLines, lines[:cursor]...)
			newLines = append(newLines, chunk.NewLines...)
			newLines = append(newLines, lines[cursor:]...)
			lines = newLines
			cursor += len(chunk.NewLines)
			continue
		}

		// 3) Find the OldLines starting at cursor.
		pattern := chunk.OldLines
		newSegment := chunk.NewLines

		oldIdx := seekSequence(lines, pattern, cursor, chunk.IsEndOfFile)

		// Retry without trailing empty line (mirrors Codex's "sentinel newline"
		// handling in compute_replacements lines 498-512).
		if oldIdx < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			trimPattern := pattern[:len(pattern)-1]
			trimNew := newSegment
			if len(trimNew) > 0 && trimNew[len(trimNew)-1] == "" {
				trimNew = trimNew[:len(trimNew)-1]
			}
			oldIdx = seekSequence(lines, trimPattern, cursor, chunk.IsEndOfFile)
			if oldIdx >= 0 {
				pattern = trimPattern
				newSegment = trimNew
			}
		}

		if oldIdx < 0 {
			return "", fmt.Errorf("chunk %d: old lines not found at or after cursor=%d", i+1, cursor)
		}

		// 4) Splice: replace lines[oldIdx : oldIdx+len(pattern)] with newSegment.
		merged := make([]string, 0, len(lines)-len(pattern)+len(newSegment))
		merged = append(merged, lines[:oldIdx]...)
		merged = append(merged, newSegment...)
		merged = append(merged, lines[oldIdx+len(pattern):]...)
		lines = merged
		cursor = oldIdx + len(newSegment)
	}

	out := strings.Join(lines, "\n")
	if hadTrailingNL && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// splitLines splits s on '\n' without keeping the trailing empty entry that
// strings.Split produces when s ends with '\n'. So "a\nb\n" → ["a","b"].
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// writeFileMkdir writes data to path, creating parent directories as needed.
func writeFileMkdir(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
