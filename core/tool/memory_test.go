package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/memory"
)

func setupBank(t *testing.T) *memory.Bank {
	t.Helper()
	b := memory.New(filepath.Join(t.TempDir(), "memory"))
	require.NoError(t, b.Init())
	return b
}

func TestMemoryUpdate_AppendDefault(t *testing.T) {
	b := setupBank(t)
	u := &MemoryUpdate{Bank: b}
	res, err := u.Run(context.Background(), json.RawMessage(`{"file":"progress","content":"step 1 done"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "appended")
	got, _ := b.Read(memory.FileProgress)
	require.Contains(t, got, "step 1 done")
}

func TestMemoryUpdate_Replace(t *testing.T) {
	b := setupBank(t)
	require.NoError(t, b.Write(memory.FileProgress, "OLD"))
	u := &MemoryUpdate{Bank: b}
	res, err := u.Run(context.Background(), json.RawMessage(`{"file":"progress","content":"NEW","replace":true}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	got, _ := b.Read(memory.FileProgress)
	require.Equal(t, "NEW", got)
}

func TestMemoryUpdate_AppendSection(t *testing.T) {
	b := setupBank(t)
	require.NoError(t, b.Write(memory.FileProgress, "## Done\n- a\n## Blocked\n- x\n"))
	u := &MemoryUpdate{Bank: b}
	res, err := u.Run(context.Background(), json.RawMessage(`{"file":"progress","content":"- b","section":"Done"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "Done")
	got, _ := b.Read(memory.FileProgress)
	require.Contains(t, got, "- a")
	require.Contains(t, got, "- b")
}

func TestMemoryUpdate_UnknownFile(t *testing.T) {
	b := setupBank(t)
	u := &MemoryUpdate{Bank: b}
	res, err := u.Run(context.Background(), json.RawMessage(`{"file":"evil","content":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "unknown memory file")
	require.Contains(t, res.Content, "valid:")
}

func TestMemoryUpdate_NilBank(t *testing.T) {
	u := &MemoryUpdate{}
	res, err := u.Run(context.Background(), json.RawMessage(`{"file":"progress","content":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "memory bank not configured")
}

func TestMemoryUpdate_MissingFile(t *testing.T) {
	b := setupBank(t)
	u := &MemoryUpdate{Bank: b}
	res, err := u.Run(context.Background(), json.RawMessage(`{"content":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, res.Content, "file is required")
}

func TestMemoryUpdate_BadJSON(t *testing.T) {
	u := &MemoryUpdate{Bank: setupBank(t)}
	_, err := u.Run(context.Background(), json.RawMessage(`{"file":`))
	require.Error(t, err)
}

func TestMemoryLoad_Success(t *testing.T) {
	b := setupBank(t)
	require.NoError(t, b.Write(memory.FileTechContext, "go 1.25\ncobra"))
	l := &MemoryLoad{Bank: b}
	res, err := l.Run(context.Background(), json.RawMessage(`{"file":"techContext"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "go 1.25\ncobra", res.Content)
}

func TestMemoryLoad_UnknownFile(t *testing.T) {
	b := setupBank(t)
	l := &MemoryLoad{Bank: b}
	res, err := l.Run(context.Background(), json.RawMessage(`{"file":"evil"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestMemoryLoad_NotFoundReturnsHelpfulMessage(t *testing.T) {
	b := memory.New(filepath.Join(t.TempDir(), "memory")) // Init NOT called
	l := &MemoryLoad{Bank: b}
	res, err := l.Run(context.Background(), json.RawMessage(`{"file":"progress"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, res.Content, "does not exist yet")
}

func TestMemoryLoad_NilBank(t *testing.T) {
	l := &MemoryLoad{}
	res, err := l.Run(context.Background(), json.RawMessage(`{"file":"progress"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestMemoryToolsImplementToolInterface(t *testing.T) {
	var _ Tool = (*MemoryUpdate)(nil)
	var _ Tool = (*MemoryLoad)(nil)
}

func TestMemoryUpdate_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&MemoryUpdate{}).Schema(), &v))
	require.Equal(t, "object", v["type"])
}

func TestMemoryLoad_SchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal((&MemoryLoad{}).Schema(), &v))
}
