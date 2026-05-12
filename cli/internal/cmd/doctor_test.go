package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/credstore"
)

// withDoctorEnv pins HOME, GIL_HOME, and PATH to a controlled tmpdir
// trio so doctor's view of the world is hermetic. It seeds PATH with a
// `git` shim (always — git is a hard requirement and tests should not
// have to special-case its absence) and optionally a `gild` shim. The
// "gild missing" case is exercised by passing withGild=false. Returns
// the GIL_HOME root so tests can assert on the layout dirs doctor will
// check.
func withDoctorEnv(t *testing.T, withGild bool) (gilHome, home, pathDir string) {
	t.Helper()
	gilHome = t.TempDir()
	home = t.TempDir()
	pathDir = t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("GIL_HOME", gilHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	// Provider env vars must be cleared so they don't pollute the
	// "no env fallbacks set" assertion.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("VLLM_API_KEY", "")
	t.Setenv("VLLM_BASE_URL", "")

	// PATH manipulation: doctor's gild + sandbox lookups go through
	// exec.LookPath which honours PATH. We set PATH to a single
	// directory we control so we can choose what's "installed". git
	// is always present so the universal "git: FAIL" doesn't pollute
	// every test's exit-code expectation; tests that explicitly want
	// to verify the git-missing path drop the shim afterwards.
	writeShim(t, filepath.Join(pathDir, "git"))
	if withGild {
		writeShim(t, filepath.Join(pathDir, "gild"))
	}
	t.Setenv("PATH", pathDir)
	return gilHome, home, pathDir
}

// writeShim drops a no-op executable at path. The shim simply exits 0
// — doctor only checks for presence, never invokes the binary, so a
// minimal shell stub is enough.
func writeShim(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runDoctorCmd executes `gil doctor <args...>` in-process, recording the
// exit code that doctorExitFn would have produced. Production paths use
// os.Exit; tests swap it for a writeback so the test binary survives.
func runDoctorCmd(t *testing.T, args ...string) (stdout, stderr string, exitCode int, err error) {
	t.Helper()
	exitCode = 0
	saved := doctorExitFn
	doctorExitFn = func(code int) { exitCode = code }
	t.Cleanup(func() { doctorExitFn = saved })

	cmd := doctorCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), exitCode, err
}

func TestDoctor_FreshGilHome_WarnsOnMissingDirs(t *testing.T) {
	withDoctorEnv(t, true)

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// Layout dirs don't exist yet — doctor must surface them as WARN
	// with "run gil init" hint, NOT FAIL (so a fresh-machine user
	// still gets exit 0 from doctor).
	if !strings.Contains(stdout, "Layout") {
		t.Errorf("expected 'Layout' header, got: %s", stdout)
	}
	if !strings.Contains(stdout, "missing") {
		t.Errorf("expected 'missing' for unset dirs, got: %s", stdout)
	}
	if !strings.Contains(stdout, "gil init") {
		t.Errorf("expected 'gil init' hint, got: %s", stdout)
	}
	// No creds yet — also WARN, not FAIL.
	if !strings.Contains(stdout, "no credentials configured") {
		t.Errorf("expected 'no credentials configured' WARN, got: %s", stdout)
	}
	if exit != 0 {
		t.Errorf("expected exit 0 on fresh machine (only WARNs), got %d", exit)
	}
}

func TestDoctor_AfterInit_LayoutOK(t *testing.T) {
	gilHome, _, _ := withDoctorEnv(t, true)

	// Materialise the layout the way `gil init` would.
	for _, d := range []string{
		filepath.Join(gilHome, "config"),
		filepath.Join(gilHome, "data"),
		filepath.Join(gilHome, "state"),
		filepath.Join(gilHome, "cache"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, gilHome) {
		t.Errorf("expected layout paths in output, got: %s", stdout)
	}
	// All four layout dirs should be OK now — we should see four
	// checkmarks before any other group.
	layoutSection := stdout
	if idx := strings.Index(stdout, "Daemon:"); idx > 0 {
		layoutSection = stdout[:idx]
	}
	okCount := strings.Count(layoutSection, "✓")
	if okCount < 4 {
		t.Errorf("expected >=4 OK marks in Layout section, got %d: %s", okCount, layoutSection)
	}
	if exit != 0 {
		t.Errorf("expected exit 0 with valid layout, got %d", exit)
	}
}

func TestDoctor_GildMissing_Fails(t *testing.T) {
	withDoctorEnv(t, false) // no gild shim under PATH
	// Pin the dev-fallback seams to a tmpdir that has no gild, so the
	// FAIL we observe is purely the PATH absence (and not accidentally
	// rescued by a stray bin/gild in the dev tree where the test runs).
	pinDevGildSeams(t, t.TempDir(), t.TempDir())

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "gild binary") {
		t.Errorf("expected 'gild binary' check, got: %s", stdout)
	}
	if !strings.Contains(stdout, "not found") {
		t.Errorf("expected 'not found' for gild, got: %s", stdout)
	}
	// gild missing is FAIL and triggers exit 1.
	if exit != 1 {
		t.Errorf("expected exit 1 when gild missing, got %d", exit)
	}
	// Summary line should reflect a non-zero FAIL count.
	if !strings.Contains(stdout, "FAIL") {
		t.Errorf("expected FAIL counter > 0 in summary, got: %s", stdout)
	}
}

// TestDoctor_GildDevFallback_CwdBin verifies the dev fallback: when `gild`
// is absent from PATH but exists at `<cwd>/bin/gild`, doctor reports OK
// (not FAIL) and annotates the path with a "(dev)" suffix.
func TestDoctor_GildDevFallback_CwdBin(t *testing.T) {
	withDoctorEnv(t, false) // gild explicitly NOT on PATH
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	gildPath := filepath.Join(cwd, "bin", "gild")
	if err := os.WriteFile(gildPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pinDevGildSeams(t, cwd, t.TempDir())

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, gildPath) {
		t.Errorf("expected dev-fallback gild path %q in output, got: %s", gildPath, stdout)
	}
	if !strings.Contains(stdout, "(dev)") {
		t.Errorf("expected '(dev)' annotation on gild path, got: %s", stdout)
	}
	if exit != 0 {
		t.Errorf("expected exit 0 with dev-fallback gild present, got %d", exit)
	}
}

// TestDoctor_GildDevFallback_Sibling verifies the second dev-fallback
// probe: a `gild` next to the running gil executable. This is the
// `go install`-into-GOBIN case where the cwd has no `bin/`.
func TestDoctor_GildDevFallback_Sibling(t *testing.T) {
	withDoctorEnv(t, false)
	exeDir := t.TempDir()
	gildPath := filepath.Join(exeDir, "gild")
	if err := os.WriteFile(gildPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// cwd points somewhere with no bin/gild; the sibling probe must
	// be the one that succeeds.
	pinDevGildSeams(t, t.TempDir(), filepath.Join(exeDir, "gil"))

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, gildPath) {
		t.Errorf("expected sibling gild path %q in output, got: %s", gildPath, stdout)
	}
	if !strings.Contains(stdout, "(dev)") {
		t.Errorf("expected '(dev)' annotation on gild path, got: %s", stdout)
	}
	if exit != 0 {
		t.Errorf("expected exit 0 with sibling gild present, got %d", exit)
	}
}

// pinDevGildSeams overrides the package-level cwd and exe seams so dev
// fallback probes look at the test-controlled paths instead of wherever
// `go test` happens to run. Both seams are restored by t.Cleanup.
func pinDevGildSeams(t *testing.T, cwd, exePath string) {
	t.Helper()
	prevCwd := gildWorkingDirFn
	prevExe := gildExecutableFn
	gildWorkingDirFn = func() (string, error) { return cwd, nil }
	gildExecutableFn = func() (string, error) { return exePath, nil }
	t.Cleanup(func() {
		gildWorkingDirFn = prevCwd
		gildExecutableFn = prevExe
	})
}

func TestDoctor_GitMissing_Fails(t *testing.T) {
	// Drop a PATH that has gild (so the failure must come from git
	// alone) but explicitly remove git afterwards. This ensures the
	// FAIL we observe is attributable to git, not to gild.
	_, _, pathDir := withDoctorEnv(t, true)
	_ = os.Remove(filepath.Join(pathDir, "git"))

	stdout, _, exit, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "git") {
		t.Errorf("expected git check, got: %s", stdout)
	}
	if exit != 1 {
		t.Errorf("expected exit 1 when git missing, got %d", exit)
	}
	if !strings.Contains(stdout, "shadow git tree") {
		t.Errorf("expected git remediation hint, got: %s", stdout)
	}
}

func TestDoctor_CredsConfigured_OK(t *testing.T) {
	gilHome, _, _ := withDoctorEnv(t, true)

	// Pre-seed an anthropic credential — doctor should pick it up
	// without us running `gil auth login` first.
	cfgDir := filepath.Join(gilHome, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := credstore.NewFileStore(filepath.Join(cfgDir, "auth.json"))
	if err := store.Set(context.Background(), credstore.Anthropic, credstore.Credential{
		Type:   credstore.CredAPI,
		APIKey: "sk-ant-doctorseed12345",
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, _, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// The masked key MUST appear (so the user knows which key is
	// configured) but the full key must NOT (so terminal copy-paste
	// doesn't leak it).
	if !strings.Contains(stdout, "sk-ant-...2345") {
		t.Errorf("expected masked anthropic key, got: %s", stdout)
	}
	if strings.Contains(stdout, "doctorseed12345") {
		t.Errorf("full anthropic key leaked into doctor output: %s", stdout)
	}
	// And the WARN about missing creds should NOT appear.
	if strings.Contains(stdout, "no credentials configured") {
		t.Errorf("doctor still reports no creds despite seeded store: %s", stdout)
	}
}

func TestDoctor_LegacyTilde_Warns(t *testing.T) {
	_, home, _ := withDoctorEnv(t, true)

	// Drop a legacy ~/.gil tree without the MIGRATED stamp — doctor
	// should call this out as WARN with a "run gil init" hint.
	if err := os.MkdirAll(filepath.Join(home, ".gil"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gil", "sessions.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, _, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "legacy ~/.gil") {
		t.Errorf("expected legacy check, got: %s", stdout)
	}
	if !strings.Contains(stdout, "not migrated") {
		t.Errorf("expected 'not migrated' message, got: %s", stdout)
	}
}

func TestDoctor_JSONOutput(t *testing.T) {
	gilHome, _, _ := withDoctorEnv(t, true)
	for _, d := range []string{"config", "data", "state", "cache"} {
		_ = os.MkdirAll(filepath.Join(gilHome, d), 0o700)
	}

	stdout, _, _, err := runDoctorCmd(t, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var payload struct {
		Version string `json:"version"`
		GOOS    string `json:"goos"`
		Checks  []struct {
			Group   string `json:"group"`
			Name    string `json:"name"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("expected valid JSON, got: %s\n%v", stdout, err)
	}
	if payload.Version == "" {
		t.Errorf("expected non-empty version in JSON output")
	}
	if len(payload.Checks) < 5 {
		t.Errorf("expected several checks in JSON, got %d", len(payload.Checks))
	}
}

func TestDoctor_OutputJSONFlagAlias(t *testing.T) {
	// --output json should produce the same valid JSON document as the
	// legacy --json flag.
	gilHome, _, _ := withDoctorEnv(t, true)
	for _, d := range []string{"config", "data", "state", "cache"} {
		_ = os.MkdirAll(filepath.Join(gilHome, d), 0o700)
	}

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	stdout, _, _, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor --output json: %v", err)
	}
	var payload struct {
		Version string `json:"version"`
		Checks  []struct {
			Group  string `json:"group"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("expected valid JSON, got: %s\n%v", stdout, err)
	}
	if payload.Version == "" {
		t.Errorf("expected non-empty version in JSON output")
	}
	if len(payload.Checks) < 5 {
		t.Errorf("expected several checks in JSON, got %d", len(payload.Checks))
	}
}

func TestDoctor_Verbose_IncludesRuntime(t *testing.T) {
	withDoctorEnv(t, true)

	stdout, _, _, err := runDoctorCmd(t, "--verbose")
	if err != nil {
		t.Fatalf("doctor -v: %v", err)
	}
	if !strings.Contains(stdout, "Runtime:") {
		t.Errorf("expected verbose 'Runtime:' line, got: %s", stdout)
	}
}

func TestDoctor_EnvFallbacks(t *testing.T) {
	withDoctorEnv(t, true)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env-12345")

	stdout, _, _, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "ANTHROPIC_API_KEY") {
		t.Errorf("expected ANTHROPIC_API_KEY in env-fallbacks section, got: %s", stdout)
	}
}
