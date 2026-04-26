package repomap_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/repomap"
)

func TestParseFile_Go_ExtractsSymbols(t *testing.T) {
	fs, err := repomap.ParseFile("testdata/sample.go")
	require.NoError(t, err)
	names := defNamesOf(fs.Defs)
	require.Contains(t, names, "Greeter")
	require.Contains(t, names, "Hello")
	require.Contains(t, names, "NewHello")
	require.Contains(t, names, "Greet")
	require.Contains(t, names, "Default")
	require.Contains(t, names, "Counter")
	// kind sanity
	requireKind(t, fs.Defs, "Greeter", "interface")
	requireKind(t, fs.Defs, "Hello", "struct")
	requireKind(t, fs.Defs, "NewHello", "func")
	requireKind(t, fs.Defs, "Greet", "method")
}

func TestParseFile_Python_ExtractsSymbols(t *testing.T) {
	fs, err := repomap.ParseFile("testdata/sample.py")
	require.NoError(t, err)
	names := defNamesOf(fs.Defs)
	require.Contains(t, names, "greet")
	require.Contains(t, names, "Greeter")
	requireKind(t, fs.Defs, "Greeter", "class")
}

func TestParseFile_TypeScript_ExtractsSymbols(t *testing.T) {
	fs, err := repomap.ParseFile("testdata/sample.ts")
	require.NoError(t, err)
	names := defNamesOf(fs.Defs)
	require.Contains(t, names, "Hello")
	require.Contains(t, names, "Greeter")
	require.Contains(t, names, "make")
	requireKind(t, fs.Defs, "Greeter", "interface")
	requireKind(t, fs.Defs, "Hello", "class")
}

func TestParseFile_UnsupportedLang(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "x.rs")
	require.NoError(t, os.WriteFile(tmp, []byte("fn main() {}"), 0o644))
	_, err := repomap.ParseFile(tmp)
	require.ErrorIs(t, err, repomap.ErrUnsupportedLanguage)
}

func TestParseFile_FileMissing(t *testing.T) {
	_, err := repomap.ParseFile("/nonexistent/x.go")
	require.Error(t, err)
}

func TestLanguageOf(t *testing.T) {
	require.Equal(t, "go", repomap.LanguageOf("/x/y.go"))
	require.Equal(t, "python", repomap.LanguageOf("/x/y.py"))
	require.Equal(t, "javascript", repomap.LanguageOf("/x/y.js"))
	require.Equal(t, "typescript", repomap.LanguageOf("/x/y.ts"))
	require.Equal(t, "", repomap.LanguageOf("/x/y.rs"))
}

func TestParseFile_Go_RefsIncludeExternalCalls(t *testing.T) {
	// create a file that calls an external function
	tmp := filepath.Join(t.TempDir(), "x.go")
	src := `package x
import "fmt"
func F() { fmt.Println("hi") }`
	require.NoError(t, os.WriteFile(tmp, []byte(src), 0o644))
	fs, err := repomap.ParseFile(tmp)
	require.NoError(t, err)
	refNames := refNamesOf(fs.Refs)
	require.Contains(t, refNames, "Println")
}

// helpers

func defNamesOf(defs []repomap.Symbol) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

func refNamesOf(refs []repomap.Reference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}

func requireKind(t *testing.T, defs []repomap.Symbol, name, kind string) {
	t.Helper()
	for _, d := range defs {
		if d.Name == name {
			require.Equal(t, kind, d.Kind, "expected %s to be %s, got %s", name, kind, d.Kind)
			return
		}
	}
	t.Fatalf("symbol %s not found", name)
}
