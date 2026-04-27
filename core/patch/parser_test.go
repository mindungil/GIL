package patch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse_AddFile(t *testing.T) {
	in := `*** Begin Patch
*** Add File: hello.txt
+hello
+world
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 1)
	require.Equal(t, HunkAddFile, p.Hunks[0].Kind)
	require.Equal(t, "hello.txt", p.Hunks[0].Path)
	require.Equal(t, "hello\nworld\n", p.Hunks[0].AddContents)
}

func TestParse_DeleteFile(t *testing.T) {
	in := "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch"
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 1)
	require.Equal(t, HunkDeleteFile, p.Hunks[0].Kind)
	require.Equal(t, "gone.txt", p.Hunks[0].Path)
}

func TestParse_UpdateFile_SingleChunk(t *testing.T) {
	in := `*** Begin Patch
*** Update File: main.go
@@ func main()
-    fmt.Println("old")
+    fmt.Println("new")
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 1)
	h := p.Hunks[0]
	require.Equal(t, HunkUpdateFile, h.Kind)
	require.Equal(t, "main.go", h.Path)
	require.Empty(t, h.MovePath)
	require.Len(t, h.Chunks, 1)
	require.Equal(t, "func main()", h.Chunks[0].ChangeContext)
	require.Equal(t, []string{`    fmt.Println("old")`}, h.Chunks[0].OldLines)
	require.Equal(t, []string{`    fmt.Println("new")`}, h.Chunks[0].NewLines)
}

func TestParse_UpdateFile_WithContext(t *testing.T) {
	in := `*** Begin Patch
*** Update File: x.go
@@
 unchanged before
-removed
+added
 unchanged after
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	c := p.Hunks[0].Chunks[0]
	require.Equal(t, []string{"unchanged before", "removed", "unchanged after"}, c.OldLines)
	require.Equal(t, []string{"unchanged before", "added", "unchanged after"}, c.NewLines)
}

func TestParse_UpdateFile_MoveTo(t *testing.T) {
	in := `*** Begin Patch
*** Update File: old/path.go
*** Move to: new/path.go
@@
-x
+y
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, "old/path.go", p.Hunks[0].Path)
	require.Equal(t, "new/path.go", p.Hunks[0].MovePath)
}

func TestParse_UpdateFile_MultipleChunks(t *testing.T) {
	in := `*** Begin Patch
*** Update File: x.go
@@ first
-a
+A
@@ second
-b
+B
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks[0].Chunks, 2)
	require.Equal(t, "first", p.Hunks[0].Chunks[0].ChangeContext)
	require.Equal(t, "second", p.Hunks[0].Chunks[1].ChangeContext)
}

func TestParse_UpdateFile_EOFMarker(t *testing.T) {
	in := `*** Begin Patch
*** Update File: x.go
@@
-tail line
+new tail line
*** End of File
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.True(t, p.Hunks[0].Chunks[0].IsEndOfFile)
}

func TestParse_MultipleHunks_DifferentKinds(t *testing.T) {
	in := `*** Begin Patch
*** Add File: new.go
+package x
*** Delete File: old.go
*** Update File: same.go
@@
-x
+X
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 3)
	require.Equal(t, HunkAddFile, p.Hunks[0].Kind)
	require.Equal(t, HunkDeleteFile, p.Hunks[1].Kind)
	require.Equal(t, HunkUpdateFile, p.Hunks[2].Kind)
}

func TestParse_MissingBeginMarker(t *testing.T) {
	in := "*** Add File: x\n+y\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidPatch, pe.Kind)
	require.Contains(t, pe.Message, "Begin Patch")
}

func TestParse_MissingEndMarker(t *testing.T) {
	in := "*** Begin Patch\n*** Add File: x\n+y"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Message, "End Patch")
}

func TestParse_EmptyPatch(t *testing.T) {
	in := "*** Begin Patch\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Message, "no hunks")
}

func TestParse_InvalidHunkHeader_GoesToErr(t *testing.T) {
	in := "*** Begin Patch\n*** Make Sandwich: bread\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidHunk, pe.Kind)
	require.Greater(t, pe.LineNumber, 0)
}

func TestParse_UpdateChunk_NoContext_FirstAllowed(t *testing.T) {
	// First chunk inside an UpdateFile is allowed to omit @@
	in := "*** Begin Patch\n*** Update File: x.go\n-old\n+new\n*** End Patch"
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks[0].Chunks, 1)
	require.Empty(t, p.Hunks[0].Chunks[0].ChangeContext)
}

func TestParse_PreservesEmptyLinesInChunk(t *testing.T) {
	in := "*** Begin Patch\n*** Update File: x.go\n@@\n line1\n\n line3\n*** End Patch"
	p, err := Parse(in)
	require.NoError(t, err)
	c := p.Hunks[0].Chunks[0]
	require.Equal(t, []string{"line1", "", "line3"}, c.OldLines)
	require.Equal(t, []string{"line1", "", "line3"}, c.NewLines)
}

func TestParseError_Error(t *testing.T) {
	pe := &ParseError{Kind: ErrInvalidPatch, Message: "bad"}
	require.Equal(t, "invalid patch: bad", pe.Error())
	pe2 := &ParseError{Kind: ErrInvalidHunk, LineNumber: 5, Message: "bad"}
	require.Contains(t, pe2.Error(), "line 5")
}

// --- extra edge-case tests ---

func TestParse_AddFile_Empty(t *testing.T) {
	// AddFile with no + lines is valid (zero-byte file)
	in := "*** Begin Patch\n*** Add File: empty.txt\n*** End Patch"
	// The AddFile hunk loop stops when it sees "***", leaving AddContents empty.
	// However, the body between Begin and End is "*** Add File: empty.txt",
	// so parseOneHunk is called for that line; it produces an AddFile hunk
	// with empty contents. The "*** End Patch" is consumed by checkBoundaries.
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 1)
	require.Equal(t, HunkAddFile, p.Hunks[0].Kind)
	require.Equal(t, "", p.Hunks[0].AddContents)
}

func TestParse_UpdateFile_EmptyChunkError(t *testing.T) {
	// UpdateFile followed immediately by another *** marker → empty chunks → error
	in := "*** Begin Patch\n*** Update File: x.go\n*** Delete File: y.go\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidHunk, pe.Kind)
	require.True(t, strings.Contains(pe.Message, "empty") || strings.Contains(pe.Message, "x.go"))
}

// TestParse_MissingBegin_DirectiveErrorMessage verifies the
// self-correcting error message format: agent reads the failure and
// knows EXACTLY which header line is missing and what the canonical
// section layout looks like. Phase 20.A motivation: dogfood Run 3
// showed qwen3.6-27b couldn't recover from the old "first line of the
// patch must be '*** Begin Patch'" error.
func TestParse_MissingBegin_DirectiveErrorMessage(t *testing.T) {
	in := "*** Update File: x.go\n@@\n-a\n+A\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidPatch, pe.Kind)
	// Names the missing header.
	require.Contains(t, pe.Message, "*** Begin Patch")
	// Lists the valid section headers so the agent knows what comes
	// next.
	require.Contains(t, pe.Message, "*** Update File:")
	require.Contains(t, pe.Message, "*** Add File:")
	require.Contains(t, pe.Message, "*** Delete File:")
	require.Contains(t, pe.Message, "*** End Patch")
}

// TestParse_MissingEnd_DirectiveErrorMessage verifies the self-
// correcting message for a missing "*** End Patch" terminator.
func TestParse_MissingEnd_DirectiveErrorMessage(t *testing.T) {
	in := "*** Begin Patch\n*** Add File: x\n+y"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Message, "*** End Patch")
	require.Contains(t, pe.Message, "final line")
}

// TestParse_InvalidHunkHeader_DirectiveErrorMessage verifies the agent-
// directive error when the section header doesn't match any known
// kind (e.g., the agent invented "*** Make Sandwich:").
func TestParse_InvalidHunkHeader_DirectiveErrorMessage(t *testing.T) {
	in := "*** Begin Patch\n*** Make Sandwich: bread\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidHunk, pe.Kind)
	require.Contains(t, pe.Message, "*** Add File:")
	require.Contains(t, pe.Message, "*** Delete File:")
	require.Contains(t, pe.Message, "*** Update File:")
}

// TestParse_BadBodyLine_DirectiveErrorMessage verifies the directive
// when a body line doesn't start with ' '/+/- (Codex grammar).
// This was the root cause of one of the qwen apply_patch failures in
// Run 3 ("invalid hunk at line 28, unexpected line in update hunk").
func TestParse_BadBodyLine_DirectiveErrorMessage(t *testing.T) {
	in := "*** Begin Patch\n*** Update File: x.go\n@@\nthis line has no prefix\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, ErrInvalidHunk, pe.Kind)
	require.Contains(t, pe.Message, "' ' (one space")
	require.Contains(t, pe.Message, "'+'")
	require.Contains(t, pe.Message, "'-'")
	// The "@@ ..." vs body-line distinction is what tripped qwen — the
	// directive calls it out by name.
	require.Contains(t, pe.Message, "section header")
}

// TestParse_EmptyUpdateHunk_DirectiveErrorMessage verifies the
// directive when an Update File hunk has no body chunks at all.
func TestParse_EmptyUpdateHunk_DirectiveErrorMessage(t *testing.T) {
	in := "*** Begin Patch\n*** Update File: x.go\n*** Delete File: y.go\n*** End Patch"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Message, "@@")
	// Mentions adding at least one chunk and shows the canonical body
	// line shapes the agent should write.
	require.Contains(t, pe.Message, "<line to remove>")
	require.Contains(t, pe.Message, "<line to add>")
}

func TestParse_UpdateFile_EOFMarker_ThenNextHunk(t *testing.T) {
	// EOF marker terminates current chunk; next *** line starts a new hunk.
	in := `*** Begin Patch
*** Update File: a.go
@@
-old
+new
*** End of File
*** Delete File: b.go
*** End Patch`
	p, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, p.Hunks, 2)
	require.Equal(t, HunkUpdateFile, p.Hunks[0].Kind)
	require.True(t, p.Hunks[0].Chunks[0].IsEndOfFile)
	require.Equal(t, HunkDeleteFile, p.Hunks[1].Kind)
}
