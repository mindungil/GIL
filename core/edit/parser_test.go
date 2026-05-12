package edit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse_SingleBlock(t *testing.T) {
	in := "main.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "main.go", blocks[0].File)
	require.Equal(t, "old\n", blocks[0].Search)
	require.Equal(t, "new\n", blocks[0].Replace)
}

func TestParse_MultipleBlocks_DifferentFiles(t *testing.T) {
	in := "a.go\n<<<<<<< SEARCH\nx\n=======\nX\n>>>>>>> REPLACE\n\nb.py\n<<<<<<< SEARCH\ny\n=======\nY\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	require.Equal(t, "a.go", blocks[0].File)
	require.Equal(t, "b.py", blocks[1].File)
}

func TestParse_FilenameFallbackBetweenBlocks(t *testing.T) {
	in := "shared.go\n<<<<<<< SEARCH\na\n=======\nA\n>>>>>>> REPLACE\n<<<<<<< SEARCH\nb\n=======\nB\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	require.Equal(t, "shared.go", blocks[0].File)
	require.Equal(t, "shared.go", blocks[1].File, "second block inherits filename from first")
}

func TestParse_MissingFilename(t *testing.T) {
	in := "<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Reason, "missing filename")
}

func TestParse_FlexibleMarkers_5To9Chars(t *testing.T) {
	// Test 5 < / = / > chars
	in := "x.go\n<<<<< SEARCH\nold\n=====\nnew\n>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)

	// Test 9 < / = / > chars
	in9 := "x.go\n<<<<<<<<< SEARCH\nold\n=========\nnew\n>>>>>>>>> REPLACE\n"
	blocks2, err := Parse(in9)
	require.NoError(t, err)
	require.Len(t, blocks2, 1)
}

func TestParse_FilenameInFencedBlock(t *testing.T) {
	in := "```go\nmain.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n```\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "main.go", blocks[0].File)
}

func TestParse_FilenameStripsBackticksAndAsterisks(t *testing.T) {
	in := "`main.go`\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, "main.go", blocks[0].File)

	in2 := "**foo.py**:\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks2, err := Parse(in2)
	require.NoError(t, err)
	require.Equal(t, "foo.py", blocks2[0].File)
}

func TestParse_MissingDivider_Errors(t *testing.T) {
	// No ======= between SEARCH and REPLACE — should error with REPLACE in message
	// (because without a divider, the parser reads until EOF looking for =======).
	in := "x.go\n<<<<<<< SEARCH\nold\nnew\n>>>>>>> REPLACE\n"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	// The error must mention either "divider" or "REPLACE" — it reached EOF or an
	// unexpected marker while scanning the search section.
	require.True(t,
		contains(pe.Reason, "divider") || contains(pe.Reason, "REPLACE"),
		"error reason %q should mention divider or REPLACE", pe.Reason)
}

func TestParse_MissingReplace_Errors(t *testing.T) {
	in := "x.go\n<<<<<<< SEARCH\nold\n=======\nnew\n"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Reason, "REPLACE")
}

func TestParse_PreservesLineEndings(t *testing.T) {
	in := "x.go\n<<<<<<< SEARCH\nfunc Foo() {\n    return 1\n}\n=======\nfunc Foo() {\n    return 2\n}\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, "func Foo() {\n    return 1\n}\n", blocks[0].Search)
	require.Equal(t, "func Foo() {\n    return 2\n}\n", blocks[0].Replace)
}

func TestParse_EmptyContent(t *testing.T) {
	blocks, err := Parse("")
	require.NoError(t, err)
	require.Empty(t, blocks)
}

func TestParse_NoBlocks_PlainText(t *testing.T) {
	blocks, err := Parse("just a normal markdown response\nwith no edit blocks at all.\n")
	require.NoError(t, err)
	require.Empty(t, blocks)
}

func TestParse_PartialBlocks_ReturnedOnError(t *testing.T) {
	// First block valid; second block missing a divider (UPDATED marker appears where divider should be).
	in := "a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n\nb.go\n<<<<<<< SEARCH\nbroken with no divider\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.Error(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "a.go", blocks[0].File)
}

func TestParse_HeadMarker_WithOptionalAngle(t *testing.T) {
	// HEAD pattern allows optional trailing > — e.g. "<<<<<<< SEARCH>"
	in := "x.go\n<<<<<<< SEARCH>\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "x.go", blocks[0].File)
}

func TestParse_EmptySearchSection(t *testing.T) {
	// Empty search means "create new file" (append semantics) — parser should accept it.
	in := "newfile.go\n<<<<<<< SEARCH\n=======\npackage main\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "", blocks[0].Search)
	require.Equal(t, "package main\n", blocks[0].Replace)
}

func TestParse_FilenameFromLineWithColon(t *testing.T) {
	// "path/to/file.go:" — trailing colon should be stripped.
	in := "path/to/file.go:\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, "path/to/file.go", blocks[0].File)
}

func TestParse_ParseError_HasLineAndContext(t *testing.T) {
	in := "<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	_, err := Parse(in)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, 1, pe.Line)
	require.NotEmpty(t, pe.Context)
}

// TestParse_MissingFilename_DirectiveErrorMessage verifies the
// self-correcting error message: when a SEARCH block has no preceding
// filename, the parser tells the agent EXACTLY how to fix the format
// (filename on its own line BEFORE the marker, not 'path:' inside).
//
// Phase 20.A motivation: dogfood Run 3 showed qwen3.6-27b read the old
// "missing filename for SEARCH block" message and immediately
// re-emitted the same wrong format with a 'path:' label inside. The
// new message names the fix path explicitly so the next attempt lands
// on a working layout.
func TestParse_MissingFilename_DirectiveErrorMessage(t *testing.T) {
	in := "<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	// Original substring still present (back-compat for any caller
	// matching it).
	require.Contains(t, pe.Reason, "missing filename")
	// Format reminder: shows the canonical block shape.
	require.Contains(t, pe.Reason, "<<<<<<< SEARCH")
	require.Contains(t, pe.Reason, "=======")
	require.Contains(t, pe.Reason, ">>>>>>> REPLACE")
	// Names the specific 'path:' anti-pattern qwen tried.
	require.Contains(t, pe.Reason, "path:")
	require.Contains(t, pe.Reason, "BEFORE the SEARCH marker")
}

// TestParse_PathLabelPrefix_AcceptedAsFilename verifies the codex-compat
// leniency from Phase 20.C: a "path: <file>" line before the SEARCH
// marker parses as the filename "<file>" (the "path:" prefix gets
// stripped). Matches apply_patch's "*** Update File: <path>" idiom that
// some models bleed into edit-block syntax.
func TestParse_PathLabelPrefix_AcceptedAsFilename(t *testing.T) {
	in := "path: cli/internal/cmd/status_render.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	blocks, err := Parse(in)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "cli/internal/cmd/status_render.go", blocks[0].File)
}

// TestParse_PathLabelPrefix_CaseInsensitive verifies the strip works
// regardless of casing — agents output "Path:", "PATH:", "path:".
func TestParse_PathLabelPrefix_CaseInsensitive(t *testing.T) {
	for _, prefix := range []string{"path:", "Path:", "PATH:"} {
		in := prefix + " main.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
		blocks, err := Parse(in)
		require.NoError(t, err, "prefix %q", prefix)
		require.Len(t, blocks, 1, "prefix %q", prefix)
		require.Equal(t, "main.go", blocks[0].File, "prefix %q", prefix)
	}
}

// TestParse_PathLabelPrefix_BareLabelStillFails ensures the leniency
// doesn't also accept a bare "path:" with no filename — that's still a
// missing-filename error, with the directive message.
func TestParse_PathLabelPrefix_BareLabelStillFails(t *testing.T) {
	in := "path:\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	_, err := Parse(in)
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	require.Contains(t, pe.Reason, "missing filename")
}

// contains is a simple helper to avoid importing "strings" in tests
// (already available in Go builtins through package-level visibility).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
