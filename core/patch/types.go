// Package patch implements parsing and application of the apply_patch DSL,
// ported from Codex codex-rs/apply-patch/src/parser.rs (strict mode only;
// lenient and streaming modes deferred to Phase 8+).
//
// DSL grammar (from Codex parser.rs lines 5-23):
//
//	start: "*** Begin Patch" LF hunk+ "*** End Patch" LF?
//	hunk: add_hunk | delete_hunk | update_hunk
//	add_hunk:    "*** Add File: " filename LF ("+" line LF)+
//	delete_hunk: "*** Delete File: " filename LF
//	update_hunk: "*** Update File: " filename LF ("*** Move to: " filename LF)? change+
//	change:      ("@@" | "@@ " context) LF (("+" | "-" | " ") line LF)+ ("*** End of File" LF)?
package patch

import "fmt"

// HunkKind identifies the variant of a Hunk.
type HunkKind int

const (
	HunkAddFile HunkKind = iota + 1
	HunkDeleteFile
	HunkUpdateFile
)

// Hunk represents one operation in a parsed patch.
type Hunk struct {
	Kind HunkKind
	Path string

	// For HunkAddFile only:
	AddContents string

	// For HunkUpdateFile only:
	MovePath string        // empty when not moved
	Chunks   []UpdateChunk // at least one
}

// UpdateChunk is one diff hunk within an UpdateFile operation.
type UpdateChunk struct {
	// ChangeContext is the @@ <ctx> line (without the @@ prefix); empty when
	// the chunk used a bare @@ marker or no marker at all (first chunk only).
	ChangeContext string

	// OldLines and NewLines are the lines being replaced. OldLines comes
	// from ` ` (context) and `-` (removed) prefixed lines; NewLines comes
	// from ` ` (context) and `+` (added) prefixed lines. Empty input lines
	// become empty entries.
	OldLines []string
	NewLines []string

	// IsEndOfFile marks the chunk with a trailing "*** End of File".
	// The applier should anchor the chunk at the file's tail.
	IsEndOfFile bool
}

// Patch is the parser output: a flat list of hunks in document order.
type Patch struct {
	Hunks []Hunk
}

// ParseError carries either an invalid-patch error (no line context) or an
// invalid-hunk error (with line number).
type ParseError struct {
	Kind       ParseErrorKind
	Message    string
	LineNumber int // 1-indexed; 0 when not applicable
}

// ParseErrorKind distinguishes patch-level from hunk-level errors.
type ParseErrorKind int

const (
	ErrInvalidPatch ParseErrorKind = iota + 1
	ErrInvalidHunk
)

func (p *ParseError) Error() string {
	if p.Kind == ErrInvalidPatch {
		return "invalid patch: " + p.Message
	}
	return fmt.Sprintf("invalid hunk at line %d, %s", p.LineNumber, p.Message)
}
