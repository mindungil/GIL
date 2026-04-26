package workspace

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the layered defaults shape — the subset of session
// parameters that can be pre-filled before the interview phase runs,
// so that a user opening their second / tenth gil project does not
// have to re-answer "which provider, which model, which autonomy" each
// time.
//
// All fields are zero-tolerant: an empty string or nil slice is
// interpreted as "this layer did not set this value", and the previous
// layer's value (or the hardcoded default) is preserved. This is the
// same convention Aider uses with `configargparse` (whose defaults are
// added back when a key is missing from the YAML) and matches what
// most users intuit from "layered config" — set what you care about,
// inherit the rest.
type Config struct {
	// Provider is the LLM provider id, e.g. "anthropic" or "openai".
	// Empty means "use the harness default" (whatever providerFactory
	// resolves first). The interview asks this when no layer sets it.
	Provider string `toml:"provider"`

	// Model is the provider-specific model id (e.g. "claude-sonnet-4-6").
	// Empty means "use the provider's own default".
	Model string `toml:"model"`

	// WorkspaceBackend is the textual form of WorkspaceBackend enum:
	// "LOCAL_NATIVE", "LOCAL_SANDBOX", "DOCKER", "SSH", "MODAL",
	// "DAYTONA". Empty preserves the current default (LOCAL_NATIVE).
	WorkspaceBackend string `toml:"workspace_backend"`

	// Autonomy is the textual form of AutonomyDial: "PLAN_ONLY",
	// "ASK_PER_ACTION", "ASK_DESTRUCTIVE_ONLY", "FULL". Empty leaves
	// the current default in place.
	Autonomy string `toml:"autonomy"`

	// IgnoreGlobs are extra paths the repomap and file-walk tools
	// should treat as ignored. Merged additively across layers (project
	// extends global extends defaults) rather than overwriting, because
	// "ignore X" almost always means "in addition to the existing
	// ignores", never "instead of".
	IgnoreGlobs []string `toml:"ignore_globs"`
}

// Defaults returns the hardcoded sane base layer. These values are
// chosen to match what the rest of the harness already assumes:
// LOCAL_NATIVE is the only backend everyone has out of the box, and
// FULL autonomy keeps the loop usable in the existing e2e suite (which
// would block forever waiting for a permission answer otherwise).
func Defaults() Config {
	return Config{
		WorkspaceBackend: "LOCAL_NATIVE",
		Autonomy:         "FULL",
		// Provider / Model intentionally empty: the providerFactory
		// resolves them based on credstore / env, and we don't want to
		// pin a vendor here.
	}
}

// Resolve merges layered configs. Order, lowest priority first:
//
//  1. Defaults (the constants returned by Defaults()).
//  2. Global config at globalPath (typically
//     `$XDG_CONFIG_HOME/gil/config.toml`). Missing = no-op.
//  3. Project config at projectPath (typically
//     `<workspace>/.gil/config.toml`). Missing = no-op.
//
// Each layer overrides the previous for non-zero scalar fields.
// IgnoreGlobs is merged additively (deduplicated, preserving order
// from defaults → global → project).
//
// CLI flags / env vars are NOT consulted here — the caller is expected
// to apply them on top of the returned Config. This mirrors how aider
// keeps `configargparse` separate from its YAML loader: layered files
// produce a single struct, and the argument parser does its own pass
// after.
//
// Malformed TOML in either path returns an error wrapping the file
// path so the user can fix it without grepping.
func Resolve(globalPath, projectPath string) (Config, error) {
	cfg := Defaults()

	for _, layer := range []struct {
		path string
		name string
	}{
		{globalPath, "global"},
		{projectPath, "project"},
	} {
		if layer.path == "" {
			continue
		}
		next, ok, err := loadFile(layer.path)
		if err != nil {
			return Config{}, fmt.Errorf("workspace: load %s config %q: %w", layer.name, layer.path, err)
		}
		if !ok {
			continue
		}
		cfg = merge(cfg, next)
	}
	return cfg, nil
}

// loadFile reads and parses one TOML config file. Returns ok=false on
// missing file (so callers can chain optional layers), ok=true on a
// successful parse. The non-error "missing file" path is what makes
// `Resolve(globalPath, "")` cheap when no project-local config exists.
func loadFile(path string) (Config, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse: %w", err)
	}
	cfg = normalise(cfg)
	return cfg, true, nil
}

// normalise upper-cases the enum-style fields so that a user who wrote
// `autonomy = "full"` or `workspace_backend = "docker"` lower-cases is
// still understood. The underlying enums (gilv1.AutonomyDial,
// gilv1.WorkspaceBackend) are case-sensitive; doing the fold once here
// keeps the rest of the harness ignorant of case quirks.
func normalise(cfg Config) Config {
	cfg.Autonomy = strings.ToUpper(strings.TrimSpace(cfg.Autonomy))
	cfg.WorkspaceBackend = strings.ToUpper(strings.TrimSpace(cfg.WorkspaceBackend))
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return cfg
}

// merge applies `over` onto `base`. Non-empty scalar fields in `over`
// replace the corresponding field in `base`; IgnoreGlobs accumulates
// while deduplicating to keep the merged slice deterministic.
func merge(base, over Config) Config {
	out := base
	if over.Provider != "" {
		out.Provider = over.Provider
	}
	if over.Model != "" {
		out.Model = over.Model
	}
	if over.WorkspaceBackend != "" {
		out.WorkspaceBackend = over.WorkspaceBackend
	}
	if over.Autonomy != "" {
		out.Autonomy = over.Autonomy
	}
	if len(over.IgnoreGlobs) > 0 {
		seen := make(map[string]struct{}, len(base.IgnoreGlobs)+len(over.IgnoreGlobs))
		merged := make([]string, 0, len(base.IgnoreGlobs)+len(over.IgnoreGlobs))
		for _, g := range base.IgnoreGlobs {
			if _, ok := seen[g]; ok {
				continue
			}
			seen[g] = struct{}{}
			merged = append(merged, g)
		}
		for _, g := range over.IgnoreGlobs {
			if _, ok := seen[g]; ok {
				continue
			}
			seen[g] = struct{}{}
			merged = append(merged, g)
		}
		out.IgnoreGlobs = merged
	}
	return out
}
