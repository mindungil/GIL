package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/credstore"
)

// withInitEnv pins HOME, GIL_HOME, and the XDG_* env vars to a fresh
// tmpdir so `gil init` operates entirely under t.TempDir() without
// touching the developer's real ~/.config or ~/.local. Returns the
// GIL_HOME root (which is also where init will write its layout under
// {config,data,state,cache}).
func withInitEnv(t *testing.T) (gilHome, home string) {
	t.Helper()
	gilHome = t.TempDir()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIL_HOME", gilHome)
	// Belt-and-braces: clear XDG_* so they don't leak from the host.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	return gilHome, home
}

// runInitCmd executes `gil init <args...>` in-process, capturing stdout
// and stderr. We reach for initCmd() directly (not Root()) so the test
// is decoupled from sibling registrations.
func runInitCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := initCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	// Empty stdin so any accidental prompt path returns EOF instead of
	// hanging the test.
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

func TestInit_FreshGilHome(t *testing.T) {
	gilHome, _ := withInitEnv(t)

	stdout, _, err := runInitCmd(t, "--no-auth")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// All four roots should now exist on disk.
	for _, sub := range []string{"config", "data", "state", "cache"} {
		p := filepath.Join(gilHome, sub)
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}

	// config.toml should have been written with the documented stub.
	cfg := filepath.Join(gilHome, "config", "config.toml")
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("expected config.toml at %s: %v", cfg, err)
	}
	if !strings.Contains(string(body), `provider = "anthropic"`) {
		t.Errorf("config.toml stub missing provider default, got: %s", body)
	}
	if !strings.Contains(string(body), "ASK_DESTRUCTIVE_ONLY") {
		t.Errorf("config.toml stub missing autonomy default, got: %s", body)
	}

	// Output should advertise both the creation and the next-steps
	// guidance — these are the two contracts users rely on.
	if !strings.Contains(stdout, "Created:") {
		t.Errorf("expected 'Created:' header in init output, got: %s", stdout)
	}
	if !strings.Contains(stdout, "Next steps:") {
		t.Errorf("expected 'Next steps:' guidance, got: %s", stdout)
	}
	if !strings.Contains(stdout, "gil auth login") {
		t.Errorf("expected gil auth login guidance, got: %s", stdout)
	}
	if !strings.Contains(stdout, "No legacy ~/.gil to migrate") {
		t.Errorf("expected legacy-absent message, got: %s", stdout)
	}
}

func TestInit_Idempotent(t *testing.T) {
	withInitEnv(t)

	// First run.
	if _, _, err := runInitCmd(t, "--no-auth"); err != nil {
		t.Fatalf("first init: %v", err)
	}
	// Second run should succeed and NOT clobber the config.toml.
	stdout, _, err := runInitCmd(t, "--no-auth")
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if !strings.Contains(stdout, "already exists") {
		t.Errorf("expected 'already exists' on re-run, got: %s", stdout)
	}
	// The "Created:" header should NOT appear when nothing was created.
	if strings.Contains(stdout, "Created:") {
		t.Errorf("did not expect 'Created:' on idempotent run, got: %s", stdout)
	}
}

func TestInit_NoConfigNoAuth_DirsOnly(t *testing.T) {
	gilHome, _ := withInitEnv(t)

	stdout, _, err := runInitCmd(t, "--no-auth", "--no-config")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Dirs created.
	for _, sub := range []string{"config", "data", "state", "cache"} {
		if _, err := os.Stat(filepath.Join(gilHome, sub)); err != nil {
			t.Errorf("expected %s to exist", sub)
		}
	}
	// config.toml NOT created.
	if _, err := os.Stat(filepath.Join(gilHome, "config", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("expected no config.toml under --no-config, stat err: %v", err)
	}
	if !strings.Contains(stdout, "skipped config.toml") {
		t.Errorf("expected --no-config note, got: %s", stdout)
	}
	if !strings.Contains(stdout, "Auth: skipped") {
		t.Errorf("expected --no-auth note, got: %s", stdout)
	}
}

func TestInit_LegacyMigration(t *testing.T) {
	gilHome, home := withInitEnv(t)

	// Drop a fake ~/.gil tree with content the migrator recognises.
	legacy := filepath.Join(home, ".gil")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions", "01abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "sessions", "01abc", "spec.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "sessions.db"), []byte("sqlite_marker"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runInitCmd(t, "--no-auth")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(stdout, "Migrated:") {
		t.Errorf("expected 'Migrated:' block in output, got: %s", stdout)
	}
	// The destination should now hold the sessions.
	dst := filepath.Join(gilHome, "data", "sessions", "01abc", "spec.json")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("expected migrated session at %s: %v", dst, err)
	}
	dbDst := filepath.Join(gilHome, "data", "sessions.db")
	if body, err := os.ReadFile(dbDst); err != nil {
		t.Errorf("expected migrated db at %s: %v", dbDst, err)
	} else if string(body) != "sqlite_marker" {
		t.Errorf("expected sqlite_marker after migration, got: %q", body)
	}
}

func TestInit_AlreadyHasCredentials(t *testing.T) {
	gilHome, _ := withInitEnv(t)

	// Pre-seed auth.json under the XDG config dir before running init.
	store := credstore.NewFileStore(filepath.Join(gilHome, "config", "auth.json"))
	if err := store.Set(context.Background(), credstore.Anthropic, credstore.Credential{
		Type:   credstore.CredAPI,
		APIKey: "sk-ant-existing12345",
	}); err != nil {
		t.Fatal(err)
	}

	// init should detect the existing credential and skip the prompt
	// even WITHOUT --no-auth.
	stdout, _, err := runInitCmd(t)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(stdout, "already configured") {
		t.Errorf("expected 'already configured' message, got: %s", stdout)
	}
	// Must NOT have launched the prompt path (which would print
	// "Select a provider" or "Saved credential").
	if strings.Contains(stdout, "Select a provider") {
		t.Errorf("init should not prompt when creds exist, got: %s", stdout)
	}
}

func TestInit_ConfigTOMLNotOverwritten(t *testing.T) {
	gilHome, _ := withInitEnv(t)
	cfg := filepath.Join(gilHome, "config", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := []byte("# user-customised config\n[defaults]\nprovider = \"openai\"\n")
	if err := os.WriteFile(cfg, custom, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runInitCmd(t, "--no-auth")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	body, _ := os.ReadFile(cfg)
	if string(body) != string(custom) {
		t.Errorf("init clobbered user's config.toml; got: %s", body)
	}
	if !strings.Contains(stdout, "already exists") {
		t.Errorf("expected 'already exists' note for config.toml, got: %s", stdout)
	}
}
