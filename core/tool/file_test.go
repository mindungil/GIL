package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFile_RoundTripWithRead(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkingDir: dir}
	r := &ReadFile{WorkingDir: dir}

	wRes, err := w.Run(context.Background(), json.RawMessage(`{"path":"hello.txt","content":"hi there"}`))
	require.NoError(t, err)
	require.False(t, wRes.IsError)
	require.Contains(t, wRes.Content, "wrote 8 bytes")

	rRes, err := r.Run(context.Background(), json.RawMessage(`{"path":"hello.txt"}`))
	require.NoError(t, err)
	require.False(t, rRes.IsError)
	require.Equal(t, "hi there", rRes.Content)
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkingDir: dir}
	_, err := w.Run(context.Background(), json.RawMessage(`{"path":"a/b/c/deep.txt","content":"x"}`))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "a/b/c/deep.txt"))
	require.NoError(t, err)
}

func TestWriteFile_EmptyPath(t *testing.T) {
	w := &WriteFile{WorkingDir: t.TempDir()}
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"","content":"x"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestReadFile_Missing(t *testing.T) {
	r := &ReadFile{WorkingDir: t.TempDir()}
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"nope.txt"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestReadFile_Truncates(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkingDir: dir}
	r := &ReadFile{WorkingDir: dir}
	big := strings.Repeat("a", 20000)
	_, err := w.Run(context.Background(), json.RawMessage(`{"path":"big.txt","content":"`+big+`"}`))
	require.NoError(t, err)
	rRes, err := r.Run(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	require.NoError(t, err)
	require.Contains(t, rRes.Content, "(truncated)")
}

func TestWriteFile_NameAndSchema(t *testing.T) {
	w := &WriteFile{WorkingDir: "/tmp"}
	require.Equal(t, "write_file", w.Name())
	require.Contains(t, string(w.Schema()), "content")
}

func TestReadFile_NameAndSchema(t *testing.T) {
	r := &ReadFile{WorkingDir: "/tmp"}
	require.Equal(t, "read_file", r.Name())
	require.Contains(t, string(r.Schema()), "path")
}
