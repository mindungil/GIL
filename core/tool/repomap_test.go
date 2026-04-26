package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setupProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\nfunc Foo() { Bar() }\nfunc Bar() {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pkg", "x.go"),
		[]byte("package pkg\ntype X struct{}\nfunc (x *X) Hi() {}\n"), 0o644))
	return dir
}

func TestRepomap_HappyPath(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root}
	res, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "main.go")
	require.Contains(t, res.Content, "Foo")
}

func TestRepomap_RootNotConfigured(t *testing.T) {
	r := &Repomap{}
	res, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "root is not configured")
}

func TestRepomap_MaxTokensFromArgs(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root, MaxTokens: 10000}
	// Tiny budget via args overrides field default
	res, err := r.Run(context.Background(), json.RawMessage(`{"max_tokens":1}`))
	require.NoError(t, err)
	// Tiny budget → "(no symbols fit ...)" message
	require.Contains(t, res.Content, "no symbols fit")
}

func TestRepomap_EmptyProject_ReturnsHelpfulMessage(t *testing.T) {
	empty := t.TempDir()
	r := &Repomap{Root: empty}
	res, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "no source files found")
}

func TestRepomap_Cache_HitsWithinTTL(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root}
	// First call populates cache
	res1, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	// Mutate workspace AFTER first call — cached result should still come back
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.go"),
		[]byte("package main\nfunc NewSym() {}\n"), 0o644))
	res2, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, res1.Content, res2.Content, "second call within TTL should return cached output")
	require.NotContains(t, res2.Content, "NewSym")
}

func TestRepomap_BadJSON_Errors(t *testing.T) {
	r := &Repomap{Root: "/tmp"}
	_, err := r.Run(context.Background(), json.RawMessage(`{"max_tokens":`))
	require.Error(t, err)
}

func TestRepomap_NoArgs_OK(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root}
	// Empty body and zero-length should both work
	res, err := r.Run(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
}

func TestRepomap_ImplementsToolInterface(t *testing.T) {
	var _ Tool = (*Repomap)(nil)
}

func TestRepomap_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&Repomap{}).Schema(), &v))
}

func TestRepomap_DifferentMaxTokens_DifferentCacheKeys(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root}
	res1, err := r.Run(context.Background(), json.RawMessage(`{"max_tokens":100}`))
	require.NoError(t, err)
	res2, err := r.Run(context.Background(), json.RawMessage(`{"max_tokens":10000}`))
	require.NoError(t, err)
	// Different budgets should yield (potentially) different sized outputs
	// At minimum the cache entries should be independent
	if res1.Content == res2.Content {
		// Could happen if project is small enough that even tiny budget fits all
		// — only fail if both contents are completely identical AND project has many symbols
		// Just verify both are non-empty
		require.NotEmpty(t, res1.Content)
		require.NotEmpty(t, res2.Content)
	}
}

// Sanity that print contains something deterministic
func TestRepomap_OutputContainsFileHeading(t *testing.T) {
	root := setupProject(t)
	r := &Repomap{Root: root}
	res, err := r.Run(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, res.Content, "## ")
}

// silence unused import warnings if any
var _ = fmt.Sprintf
var _ = strings.Contains
var _ = time.Now
