package event

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPersister_AppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersister(dir)
	require.NoError(t, err)
	defer p.Close()

	e1 := Event{ID: 1, Type: "first", Timestamp: time.Unix(1700000000, 0)}
	e2 := Event{ID: 2, Type: "second", Timestamp: time.Unix(1700000001, 0)}

	require.NoError(t, p.Write(e1))
	require.NoError(t, p.Write(e2))
	require.NoError(t, p.Sync())

	loaded, err := LoadAll(filepath.Join(dir, "events.jsonl"))
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	require.Equal(t, "first", loaded[0].Type)
	require.Equal(t, int64(2), loaded[1].ID)
}

func TestPersister_Close_Idempotent(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersister(dir)
	require.NoError(t, err)

	require.NoError(t, p.Close())
	require.NoError(t, p.Close()) // second close should not error
}

func TestLoadAll_NonexistentFile_ReturnsError(t *testing.T) {
	_, err := LoadAll(filepath.Join(t.TempDir(), "missing.jsonl"))
	require.Error(t, err)
}

func TestLoadAll_MalformedLine_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not json\n"), 0o644))

	_, err := LoadAll(path)
	require.Error(t, err)
}
