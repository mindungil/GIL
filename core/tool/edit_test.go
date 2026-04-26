package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/edit"
)

// makeEditArgs encodes the blocks argument as JSON for Run.
func makeEditArgs(blocks string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"blocks": blocks})
	return b
}

func TestEdit_AppliesBlock_Tier1(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "hello.go")
	require.NoError(t, os.WriteFile(fpath, []byte("package main\n\nfunc hello() string {\n\treturn \"hello\"\n}\n"), 0o644))

	blocks := "hello.go\n<<<<<<< SEARCH\n\treturn \"hello\"\n=======\n\treturn \"world\"\n>>>>>>> REPLACE\n"
	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected error: %s", res.Content)
	require.Contains(t, res.Content, "[block 1] hello.go: applied")
	require.Contains(t, res.Content, "1 applied, 0 failed")

	got, err := os.ReadFile(fpath)
	require.NoError(t, err)
	require.Contains(t, string(got), `"world"`)
}

func TestEdit_AppliesBlock_Tier2_PreservesIndent(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "code.go")
	// File has deeper indentation than the SEARCH block.
	content := "package main\n\nfunc run() {\n\t\tif true {\n\t\t\tx := 1\n\t\t\t_ = x\n\t\t}\n}\n"
	require.NoError(t, os.WriteFile(fpath, []byte(content), 0o644))

	// SEARCH uses less indentation — Tier2 should match and rebuild indent.
	blocks := "code.go\n<<<<<<< SEARCH\nif true {\n\tx := 1\n\t_ = x\n}\n=======\nif false {\n\tx := 2\n\t_ = x\n}\n>>>>>>> REPLACE\n"
	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected error: %s", res.Content)
	require.Contains(t, res.Content, "applied")

	got, err := os.ReadFile(fpath)
	require.NoError(t, err)
	// The replaced block should retain the deeper indentation from the file.
	require.Contains(t, string(got), "if false {")
}

func TestEdit_MissingFile_ReportsError(t *testing.T) {
	dir := t.TempDir()
	blocks := "nonexistent.go\n<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>> REPLACE\n"
	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "read failed")
	require.Contains(t, res.Content, "0 applied, 1 failed")
}

func TestEdit_NoMatch_SurfacesFindSimilarHint(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "app.go")
	// File contains a function. Use a MatchEngine with FuzzyThreshold=1.0 so that
	// even close matches are rejected by the engine, then FindSimilar (threshold=0.6)
	// should still surface a hint.
	content := "package main\n\nfunc greet(name string) {\n\tprintln(\"Hello, \" + name)\n}\n"
	require.NoError(t, os.WriteFile(fpath, []byte(content), 0o644))

	// Search block similar enough for FindSimilar (>=0.6) but won't exactly match.
	// By setting FuzzyThreshold=1.0 on the engine, Tier4 can't succeed either.
	blocks := "app.go\n<<<<<<< SEARCH\nfunc greet(name string) {\n\tprintln(\"Hi, \" + name)\n}\n=======\nfunc greet(name string) {\n\tprintln(\"Hey, \" + name)\n}\n>>>>>>> REPLACE\n"
	e := &Edit{WorkingDir: dir, Engine: &edit.MatchEngine{FuzzyThreshold: 1.0}}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "SEARCH not found")
	// The hint should be present since the file has similar content (above 0.6).
	require.Contains(t, res.Content, "Did you mean")
}

func TestEdit_NoMatch_NoHintWhenBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "data.go")
	// Short file with completely unrelated content.
	content := "package main\n\nvar x = 42\n"
	require.NoError(t, os.WriteFile(fpath, []byte(content), 0o644))

	// Search block with nothing in common with the file.
	blocks := "data.go\n<<<<<<< SEARCH\nfunc zzz() {\n\treturn qqq()\n}\n=======\nfunc zzz() {\n\treturn www()\n}\n>>>>>>> REPLACE\n"
	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "SEARCH not found")
	require.Contains(t, res.Content, "no similar chunk above threshold")
}

func TestEdit_MultipleBlocks_DifferentFiles(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	require.NoError(t, os.WriteFile(f1, []byte("package a\n\nconst A = 1\n"), 0o644))
	require.NoError(t, os.WriteFile(f2, []byte("package b\n\nconst B = 2\n"), 0o644))

	blocks := strings.Join([]string{
		"a.go",
		"<<<<<<< SEARCH",
		"const A = 1",
		"=======",
		"const A = 10",
		">>>>>>> REPLACE",
		"b.go",
		"<<<<<<< SEARCH",
		"const B = 2",
		"=======",
		"const B = 20",
		">>>>>>> REPLACE",
		"",
	}, "\n")

	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected error: %s", res.Content)
	require.Contains(t, res.Content, "2 applied, 0 failed")

	gotA, _ := os.ReadFile(f1)
	require.Contains(t, string(gotA), "const A = 10")
	gotB, _ := os.ReadFile(f2)
	require.Contains(t, string(gotB), "const B = 20")
}

func TestEdit_PartialApply_OnParseError(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "valid.go")
	require.NoError(t, os.WriteFile(fpath, []byte("package main\n\nvar n = 1\n"), 0o644))

	// First block is valid; second block is malformed (no >>>>>>> REPLACE closer).
	blocks := strings.Join([]string{
		"valid.go",
		"<<<<<<< SEARCH",
		"var n = 1",
		"=======",
		"var n = 99",
		">>>>>>> REPLACE",
		"valid.go",
		"<<<<<<< SEARCH",
		"var n = 99",
		"=======",
		"var n = 100",
		// Missing >>>>>>> REPLACE — parse error
	}, "\n")

	e := &Edit{WorkingDir: dir}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	// IsError=true because parse error occurred.
	require.True(t, res.IsError)
	// The first valid block should still have been applied.
	require.Contains(t, res.Content, "1 applied")
	require.Contains(t, res.Content, "NOTE: parse error")

	// File should reflect the first block's application.
	got, _ := os.ReadFile(fpath)
	require.Contains(t, string(got), "var n = 99")
}

func TestEdit_NilBlocks_Errors(t *testing.T) {
	e := &Edit{WorkingDir: t.TempDir()}
	res, err := e.Run(context.Background(), makeEditArgs(""))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "blocks is empty")
}

func TestEdit_ToolInterface(t *testing.T) {
	// Compile-time check: Edit implements the Tool interface.
	var _ Tool = (*Edit)(nil)
}

func TestEdit_SchemaValidJSON(t *testing.T) {
	e := &Edit{WorkingDir: "/tmp"}
	var v interface{}
	require.NoError(t, json.Unmarshal(e.Schema(), &v), "schema must be valid JSON")
	m, ok := v.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "object", m["type"])
}

func TestEdit_NilEngine_UsesDefaults(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("foo\nbar\nbaz\n"), 0o644))

	blocks := "x.txt\n<<<<<<< SEARCH\nbar\n=======\nQUX\n>>>>>>> REPLACE\n"
	// Engine is nil — should default internally.
	e := &Edit{WorkingDir: dir, Engine: nil}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected error: %s", res.Content)

	got, _ := os.ReadFile(fpath)
	require.Contains(t, string(got), "QUX")
}

func TestEdit_CustomEngine_Respected(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "eng.go")
	require.NoError(t, os.WriteFile(fpath, []byte("package main\n\nvar v = \"old\"\n"), 0o644))

	blocks := "eng.go\n<<<<<<< SEARCH\nvar v = \"old\"\n=======\nvar v = \"new\"\n>>>>>>> REPLACE\n"
	// Provide an explicit engine with a custom threshold.
	e := &Edit{WorkingDir: dir, Engine: &edit.MatchEngine{FuzzyThreshold: 0.9}}
	res, err := e.Run(context.Background(), makeEditArgs(blocks))
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected error: %s", res.Content)
	require.Contains(t, res.Content, "applied")
}
