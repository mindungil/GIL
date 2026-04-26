package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyPatch_AddFile(t *testing.T) {
	dir := t.TempDir()
	a := &ApplyPatch{WorkspaceDir: dir}
	in := "*** Begin Patch\n*** Add File: greet.txt\n+hello\n+world\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": in})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError, "content: %s", res.Content)
	got, _ := os.ReadFile(filepath.Join(dir, "greet.txt"))
	require.Equal(t, "hello\nworld\n", string(got))
	require.Contains(t, res.Content, "1 applied, 0 failed")
}

func TestApplyPatch_UpdateFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("alpha\nbeta\ngamma\n"), 0o644))
	a := &ApplyPatch{WorkspaceDir: dir}
	in := "*** Begin Patch\n*** Update File: x.go\n@@\n-beta\n+BETA\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": in})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError, "content: %s", res.Content)
	got, _ := os.ReadFile(filepath.Join(dir, "x.go"))
	require.Equal(t, "alpha\nBETA\ngamma\n", string(got))
}

func TestApplyPatch_MultiHunk_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("orig\n"), 0o644))
	a := &ApplyPatch{WorkspaceDir: dir}
	in := "*** Begin Patch\n*** Add File: new.txt\n+ok\n*** Delete File: missing.txt\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": in})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.IsError) // overall error because one hunk failed
	require.Contains(t, res.Content, "1 applied, 1 failed")
	// First hunk still applied
	_, err = os.Stat(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
}

func TestApplyPatch_Move(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.go"), []byte("x\n"), 0o644))
	a := &ApplyPatch{WorkspaceDir: dir}
	in := "*** Begin Patch\n*** Update File: old.go\n*** Move to: new.go\n@@\n-x\n+X\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": in})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.False(t, res.IsError, "content: %s", res.Content)
	require.Contains(t, res.Content, "→ new.go")
	_, err = os.Stat(filepath.Join(dir, "old.go"))
	require.True(t, os.IsNotExist(err))
}

func TestApplyPatch_ParseError(t *testing.T) {
	a := &ApplyPatch{WorkspaceDir: t.TempDir()}
	args, _ := json.Marshal(map[string]string{"patch": "not a patch"})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "parse error")
}

func TestApplyPatch_EmptyPatch(t *testing.T) {
	a := &ApplyPatch{WorkspaceDir: t.TempDir()}
	args, _ := json.Marshal(map[string]string{"patch": ""})
	res, err := a.Run(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "patch is empty")
}

func TestApplyPatch_BadJSON(t *testing.T) {
	a := &ApplyPatch{WorkspaceDir: t.TempDir()}
	_, err := a.Run(context.Background(), json.RawMessage(`{"patch":`))
	require.Error(t, err)
}

func TestApplyPatch_ImplementsToolInterface(t *testing.T) {
	var _ Tool = (*ApplyPatch)(nil)
}

func TestApplyPatch_SchemaValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&ApplyPatch{}).Schema(), &v))
}
