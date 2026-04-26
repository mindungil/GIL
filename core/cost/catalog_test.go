package cost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultCatalog_LoadsKnownModels(t *testing.T) {
	c := DefaultCatalog()
	require.NotEmpty(t, c)
	for _, model := range []string{
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
		"gpt-4o",
		"gpt-4o-mini",
	} {
		p, ok := c[model]
		require.True(t, ok, "missing model %s", model)
		require.Greater(t, p.InputPerM, 0.0, "model %s has zero InputPerM", model)
		require.Greater(t, p.OutputPerM, 0.0, "model %s has zero OutputPerM", model)
	}
}

func TestDefaultCatalog_FreshCopyEachCall(t *testing.T) {
	a := DefaultCatalog()
	a["mutated"] = ModelPrice{InputPerM: 99}
	b := DefaultCatalog()
	_, leaked := b["mutated"]
	require.False(t, leaked, "DefaultCatalog must return a fresh map")
}

func TestLoadCatalog_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadCatalog(filepath.Join(dir, "does-not-exist.json"))
	require.NoError(t, err)
	require.Equal(t, DefaultCatalog(), got)
}

func TestLoadCatalog_OverridesAndExtends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	body := []byte(`{
		"claude-opus-4-7": {"input_per_m": 99.99, "output_per_m": 100.00},
		"new-model":       {"input_per_m": 1.00,  "output_per_m":  2.00}
	}`)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	got, err := LoadCatalog(path)
	require.NoError(t, err)

	// override applied
	require.Equal(t, 99.99, got["claude-opus-4-7"].InputPerM)
	require.Equal(t, 100.00, got["claude-opus-4-7"].OutputPerM)
	// new entry added
	require.Equal(t, 1.00, got["new-model"].InputPerM)
	// unrelated default preserved
	require.Equal(t, 0.15, got["gpt-4o-mini"].InputPerM)
}

func TestLoadCatalog_InvalidJSONErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, err := LoadCatalog(path)
	require.Error(t, err)
}
