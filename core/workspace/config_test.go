package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolve_DefaultsOnly(t *testing.T) {
	cfg, err := Resolve("", "")
	require.NoError(t, err)
	require.Equal(t, Defaults(), cfg)
	require.Equal(t, "LOCAL_NATIVE", cfg.WorkspaceBackend)
	require.Equal(t, "FULL", cfg.Autonomy)
	require.Empty(t, cfg.Provider)
	require.Empty(t, cfg.Model)
}

func TestResolve_GlobalMergesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(global, []byte(`
provider = "anthropic"
model    = "claude-sonnet-4-6"
autonomy = "ASK_DESTRUCTIVE_ONLY"
`), 0o644))

	cfg, err := Resolve(global, "")
	require.NoError(t, err)
	require.Equal(t, "anthropic", cfg.Provider)
	require.Equal(t, "claude-sonnet-4-6", cfg.Model)
	require.Equal(t, "ASK_DESTRUCTIVE_ONLY", cfg.Autonomy)
	// WorkspaceBackend untouched by global → falls back to default.
	require.Equal(t, "LOCAL_NATIVE", cfg.WorkspaceBackend)
}

func TestResolve_ProjectOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")

	require.NoError(t, os.WriteFile(global, []byte(`
provider = "anthropic"
model    = "claude-sonnet-4-6"
autonomy = "ASK_PER_ACTION"
`), 0o644))
	require.NoError(t, os.WriteFile(project, []byte(`
model    = "claude-opus-4-7"
autonomy = "FULL"
`), 0o644))

	cfg, err := Resolve(global, project)
	require.NoError(t, err)
	// project's model wins over global's; provider stays as global's
	// because project did not set it.
	require.Equal(t, "anthropic", cfg.Provider)
	require.Equal(t, "claude-opus-4-7", cfg.Model)
	require.Equal(t, "FULL", cfg.Autonomy)
}

func TestResolve_ZeroValuesDoNotOverride(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")

	require.NoError(t, os.WriteFile(global, []byte(`
provider = "openai"
model    = "gpt-5"
`), 0o644))
	// Project explicitly sets model to "" — should NOT clobber.
	require.NoError(t, os.WriteFile(project, []byte(`
provider = "anthropic"
model    = ""
`), 0o644))

	cfg, err := Resolve(global, project)
	require.NoError(t, err)
	require.Equal(t, "anthropic", cfg.Provider)
	require.Equal(t, "gpt-5", cfg.Model, "empty model in project must not erase global value")
}

func TestResolve_IgnoreGlobsMergeAdditively(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")

	require.NoError(t, os.WriteFile(global, []byte(`
ignore_globs = ["node_modules/", "dist/"]
`), 0o644))
	require.NoError(t, os.WriteFile(project, []byte(`
ignore_globs = ["dist/", "secrets/"]
`), 0o644))

	cfg, err := Resolve(global, project)
	require.NoError(t, err)
	require.Equal(t, []string{"node_modules/", "dist/", "secrets/"}, cfg.IgnoreGlobs,
		"merge should preserve order and dedupe `dist/`")
}

func TestResolve_MissingFilesAreNoOp(t *testing.T) {
	cfg, err := Resolve("/nope/does-not-exist.toml", "/also/missing.toml")
	require.NoError(t, err)
	require.Equal(t, Defaults(), cfg)
}

func TestResolve_MalformedTOMLReturnsErrorWithPath(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.toml")
	require.NoError(t, os.WriteFile(bad, []byte("this is = not = valid toml ["), 0o644))

	_, err := Resolve(bad, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), bad, "error message must mention the offending file path")
	require.Contains(t, err.Error(), "global", "error message must identify the layer")
}

func TestResolve_MalformedProjectIdentifiesProjectLayer(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.toml")
	require.NoError(t, os.WriteFile(bad, []byte("nope ["), 0o644))

	_, err := Resolve("", bad)
	require.Error(t, err)
	require.Contains(t, err.Error(), "project")
}

func TestResolve_CaseFoldedEnums(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "lower.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(`
autonomy = "full"
workspace_backend = "docker"
`), 0o644))

	got, err := Resolve(cfg, "")
	require.NoError(t, err)
	require.Equal(t, "FULL", got.Autonomy)
	require.Equal(t, "DOCKER", got.WorkspaceBackend)
}
