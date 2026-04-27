package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/credstore"
)

// runAuthCmd executes a `gil auth ...` command in-process and returns the
// stdout/stderr buffers and any error from RunE. Tests use this to drive
// every subcommand without spawning subprocesses or touching the user's
// real auth.json.
func runAuthCmd(t *testing.T, authFile string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := authCmd()
	// Pass --auth-file to the eventually-executed leaf command. Cobra
	// propagates root args to leaf parsing, but the hidden flag is
	// declared on each leaf, so we inject it just before the leaf args.
	full := append([]string{}, args...)
	full = append(full, "--auth-file", authFile)
	root.SetArgs(full)

	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err = root.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

func TestAuthLogin_NonInteractive(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	stdout, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "sk-ant-test1234567890abcd")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(stdout, "Saved credential for anthropic") {
		t.Errorf("expected success message, got: %s", stdout)
	}

	store := credstore.NewFileStore(authFile)
	cred, err := store.Get(context.Background(), credstore.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.APIKey != "sk-ant-test1234567890abcd" {
		t.Fatalf("credential not persisted, got %+v", cred)
	}
}

func TestAuthLogin_PrefixWarning(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Wrong prefix should warn but still save.
	_, stderr, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "definitely-not-anthropic-shape")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected warning on wrong prefix, got stderr: %q", stderr)
	}
	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.Anthropic)
	if cred == nil {
		t.Fatalf("credential should still be saved despite warning")
	}
}

func TestAuthLogin_EmptyKey(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	_, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "   ")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got %v", err)
	}
}

func TestAuthLogin_VLLMRequiresBaseURL(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Without --base-url and without a TTY, this should fail (readLine
	// returns empty on closed stdin).
	_, _, err := runAuthCmd(t, authFile, "login", "vllm", "--api-key", "local")
	if err == nil {
		t.Fatal("expected error when vllm has no base-url")
	}

	// With --base-url it succeeds.
	stdout, _, err := runAuthCmd(t, authFile, "login", "vllm", "--api-key", "local", "--base-url", "http://localhost:8000/v1")
	if err != nil {
		t.Fatalf("vllm login: %v", err)
	}
	if !strings.Contains(stdout, "Saved credential for vllm") {
		t.Errorf("unexpected output: %s", stdout)
	}
	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.VLLM)
	if cred == nil || cred.BaseURL != "http://localhost:8000/v1" {
		t.Fatalf("expected base url to persist, got %+v", cred)
	}
}

func TestAuthList_Empty(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	stdout, _, err := runAuthCmd(t, authFile, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "No credentials configured") {
		t.Errorf("expected empty-state message, got: %s", stdout)
	}
}

func TestAuthList_Masked(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Seed two providers.
	if _, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "sk-ant-1234567890abcd3f2a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runAuthCmd(t, authFile, "login", "openai", "--api-key", "sk-test1234567890abcd"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runAuthCmd(t, authFile, "list")
	if err != nil {
		t.Fatal(err)
	}
	// Masked output must NOT contain the full keys.
	if strings.Contains(stdout, "1234567890abcd3f2a") {
		t.Errorf("full anthropic key leaked into list output: %s", stdout)
	}
	// Masked form must contain the short suffix.
	if !strings.Contains(stdout, "sk-ant-...3f2a") {
		t.Errorf("expected masked anthropic key, got: %s", stdout)
	}
	if !strings.Contains(stdout, "anthropic") || !strings.Contains(stdout, "openai") {
		t.Errorf("expected both providers in list, got: %s", stdout)
	}
}

func TestAuthLogout(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	if _, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "sk-ant-test1234567890ab"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runAuthCmd(t, authFile, "logout", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Removed credential for anthropic") {
		t.Errorf("expected removal message, got: %s", stdout)
	}

	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.Anthropic)
	if cred != nil {
		t.Fatalf("credential should be gone, got %+v", cred)
	}
}

func TestAuthLogout_Idempotent(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	stdout, _, err := runAuthCmd(t, authFile, "logout", "anthropic")
	if err != nil {
		t.Fatalf("logout on empty store: %v", err)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("expected 'nothing to remove', got: %s", stdout)
	}
}

func TestAuthStatus_ShowsEnvVars(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-env-1234567890")

	if _, _, err := runAuthCmd(t, authFile, "login", "openai", "--api-key", "sk-test123456789012"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runAuthCmd(t, authFile, "status")
	if err != nil {
		t.Fatal(err)
	}
	// auth file path is shown
	if !strings.Contains(stdout, authFile) {
		t.Errorf("expected auth file path in status, got: %s", stdout)
	}
	// configured providers
	if !strings.Contains(stdout, "openai") {
		t.Errorf("expected openai in status, got: %s", stdout)
	}
	// env vars
	if !strings.Contains(stdout, "ANTHROPIC_API_KEY") {
		t.Errorf("expected ANTHROPIC_API_KEY in status, got: %s", stdout)
	}
}

func TestAuthStatus_NoCreds(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Make sure no env vars leak in.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("VLLM_API_KEY", "")
	t.Setenv("VLLM_BASE_URL", "")

	stdout, _, err := runAuthCmd(t, authFile, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "(none configured)") {
		t.Errorf("expected '(none configured)' in status, got: %s", stdout)
	}
	if !strings.Contains(stdout, "(no provider env vars set)") {
		t.Errorf("expected '(no provider env vars set)' in status, got: %s", stdout)
	}
}

func TestAuthLogin_UnknownProvider(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Unknown providers ARE accepted (with a warning) so users can store
	// custom-gateway creds.
	stdout, stderr, err := runAuthCmd(t, authFile, "login", "my-custom-gw", "--api-key", "key1234567890abcd")
	if err != nil {
		t.Fatalf("login should accept unknown provider, got: %v", err)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected warning for unknown provider, got stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Saved credential for my-custom-gw") {
		t.Errorf("expected save message, got: %s", stdout)
	}
}

func TestAuthList_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Seed two providers — one with a base URL so we exercise both
	// shapes of the JSON entry.
	if _, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "sk-ant-1234567890abcd3f2a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runAuthCmd(t, authFile, "login", "vllm", "--api-key", "local1234567890abcd", "--base-url", "http://localhost:8000/v1"); err != nil {
		t.Fatal(err)
	}

	prev := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = prev })

	stdout, _, err := runAuthCmd(t, authFile, "list")
	if err != nil {
		t.Fatalf("list --output json: %v", err)
	}
	var parsed authListJSON
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if len(parsed.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(parsed.Providers))
	}
	if parsed.File != authFile {
		t.Errorf("expected file=%q, got %q", authFile, parsed.File)
	}
	// Raw key bytes must NOT appear anywhere in the JSON output.
	if strings.Contains(stdout, "1234567890abcd3f2a") {
		t.Errorf("anthropic key leaked into JSON output: %s", stdout)
	}
	for _, p := range parsed.Providers {
		if p.MaskedKey == "" {
			t.Errorf("provider %q has empty masked_key", p.Name)
		}
		if p.Name == "vllm" && p.BaseURL != "http://localhost:8000/v1" {
			t.Errorf("vllm base_url not propagated, got %q", p.BaseURL)
		}
	}
}

// TestAuthRoundTrip exercises the full login -> list -> logout -> status
// cycle via the CLI surface (not the store directly). This is the test that
// catches integration bugs between the subcommands.
func TestAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// 1. login
	if _, _, err := runAuthCmd(t, authFile, "login", "anthropic", "--api-key", "sk-ant-roundtrip12345"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// 2. list shows it
	stdout, _, _ := runAuthCmd(t, authFile, "list")
	if !strings.Contains(stdout, "anthropic") {
		t.Errorf("expected anthropic in list, got: %s", stdout)
	}

	// 3. status shows it
	stdout, _, _ = runAuthCmd(t, authFile, "status")
	if !strings.Contains(stdout, "anthropic") {
		t.Errorf("expected anthropic in status, got: %s", stdout)
	}

	// 4. logout
	if _, _, err := runAuthCmd(t, authFile, "logout", "anthropic"); err != nil {
		t.Fatalf("logout: %v", err)
	}

	// 5. list is empty
	stdout, _, _ = runAuthCmd(t, authFile, "list")
	if !strings.Contains(stdout, "No credentials configured") {
		t.Errorf("expected empty list after logout, got: %s", stdout)
	}
}
