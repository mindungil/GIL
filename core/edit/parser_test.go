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
