package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindungil/gil/core/credstore"
	"github.com/mindungil/gil/core/provider"
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

// TestAuthLogin_NonInteractive_SavesModel checks the new --model flag:
// when supplied alongside --api-key, the model id round-trips through
// the credstore and shows up on `gil auth list`.
func TestAuthLogin_NonInteractive_SavesModel(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	if _, _, err := runAuthCmd(t, authFile,
		"login", "anthropic",
		"--api-key", "sk-ant-test1234567890abcd",
		"--model", "claude-sonnet-4-6",
		"--no-test",
	); err != nil {
		t.Fatalf("login: %v", err)
	}

	store := credstore.NewFileStore(authFile)
	cred, err := store.Get(context.Background(), credstore.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.Model != "claude-sonnet-4-6" {
		t.Fatalf("expected model on stored credential, got %+v", cred)
	}

	stdout, _, _ := runAuthCmd(t, authFile, "list")
	if !strings.Contains(stdout, "claude-sonnet-4-6") {
		t.Errorf("expected model in list output, got: %s", stdout)
	}
}

// TestAuthLogin_WizardInteractive_PicksProviderKeyModel exercises the
// full wizard flow with stdin replay. We force TTY mode via the test
// env override so the wizard renders its multi-step UI; stdin carries
// the keystrokes that drive each step.
//
// The smoke-test step is skipped via --no-test so we don't try to
// reach api.anthropic.com from the test process.
func TestAuthLogin_WizardInteractive_PicksProviderKeyModel(t *testing.T) {
	t.Setenv("GIL_TEST_FORCE_TTY", "1")
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Stdin replay: pick provider [1] anthropic → key → pick model
	// default (empty enter → choice 1 = haiku).
	stdin := strings.NewReader("1\nsk-ant-test1234567890abcd\n\n")

	root := authCmd()
	root.SetArgs([]string{"login", "--auth-file", authFile, "--no-test"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(stdin)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("wizard execute: %v\nstdout: %s\nstderr: %s", err, out.String(), errBuf.String())
	}

	// Banner + step labels rendered.
	for _, want := range []string{
		"Provider Setup",
		"Pick a provider",
		"Anthropic",
		"OpenAI",
		"OpenRouter",
		"vLLM",
		"API key",
		"Default model",
		"Saved",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("wizard output missing %q\nfull output:\n%s", want, out.String())
		}
	}

	// Credential persisted with the picked model (haiku is the default).
	store := credstore.NewFileStore(authFile)
	cred, err := store.Get(context.Background(), credstore.Anthropic)
	if err != nil || cred == nil {
		t.Fatalf("expected saved credential, got %v / %+v", err, cred)
	}
	if cred.Model != "claude-haiku-4-5" {
		t.Errorf("expected default model claude-haiku-4-5, got %q", cred.Model)
	}
	if cred.APIKey != "sk-ant-test1234567890abcd" {
		t.Errorf("expected api key persisted, got %q", cred.APIKey)
	}
}

// TestAuthLogin_WizardInteractive_OpenRouterCustomModel verifies the
// "Other" branch in the model picker: user picks the sentinel and types
// a model id verbatim.
func TestAuthLogin_WizardInteractive_OpenRouterCustomModel(t *testing.T) {
	t.Setenv("GIL_TEST_FORCE_TTY", "1")
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// 3 = OpenRouter, then key, then 6 = "Other", then custom model id.
	stdin := strings.NewReader("3\nsk-or-v1-test1234567890ab\n6\nmistralai/mistral-large\n")

	root := authCmd()
	root.SetArgs([]string{"login", "--auth-file", authFile, "--no-test"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(stdin)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("wizard execute: %v\nstdout: %s\nstderr: %s", err, out.String(), errBuf.String())
	}

	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.OpenRouter)
	if cred == nil {
		t.Fatal("expected openrouter credential")
	}
	if cred.Model != "mistralai/mistral-large" {
		t.Errorf("expected custom model, got %q", cred.Model)
	}
}

// TestAuthLogin_WizardInteractive_VLLMRequiresModel checks that the
// vllm flow prompts for base URL → key (optional) → model id, and that
// the saved credential carries all three.
func TestAuthLogin_WizardInteractive_VLLMRequiresModel(t *testing.T) {
	t.Setenv("GIL_TEST_FORCE_TTY", "1")
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// 4 = vllm, then base URL, then empty key (enter), then model id.
	stdin := strings.NewReader("4\nhttp://localhost:8000/v1\n\nqwen3-32b\n")

	root := authCmd()
	root.SetArgs([]string{"login", "--auth-file", authFile, "--no-test"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(stdin)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("wizard execute: %v\nstdout: %s\nstderr: %s", err, out.String(), errBuf.String())
	}

	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.VLLM)
	if cred == nil {
		t.Fatal("expected vllm credential")
	}
	if cred.BaseURL != "http://localhost:8000/v1" {
		t.Errorf("expected base url persisted, got %q", cred.BaseURL)
	}
	if cred.Model != "qwen3-32b" {
		t.Errorf("expected model qwen3-32b, got %q", cred.Model)
	}
	if !strings.Contains(out.String(), "vLLM endpoint") {
		t.Errorf("expected vllm endpoint step header, got: %s", out.String())
	}
}

// TestAuthEdit_RoundTripsModelChange exercises the new `gil auth edit`
// subcommand: registering a credential, then changing only the model
// without re-typing the key, leaves the API key untouched.
func TestAuthEdit_RoundTripsModelChange(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Step 1: register with model A.
	if _, _, err := runAuthCmd(t, authFile,
		"login", "anthropic",
		"--api-key", "sk-ant-orig1234567890abcd",
		"--model", "claude-haiku-4-5",
		"--no-test",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Step 2: edit, changing only --model. We pass the same --api-key
	// to avoid the interactive key prompt, but in real interactive use
	// the user would press enter to keep the existing key. We test
	// the "model swap" outcome — the headline use case.
	stdout, _, err := runAuthCmd(t, authFile,
		"edit", "anthropic",
		"--api-key", "sk-ant-orig1234567890abcd",
		"--model", "claude-sonnet-4-6",
		"--no-test",
	)
	if err != nil {
		t.Fatalf("edit: %v\nstdout: %s", err, stdout)
	}

	store := credstore.NewFileStore(authFile)
	cred, _ := store.Get(context.Background(), credstore.Anthropic)
	if cred == nil {
		t.Fatal("credential should still exist after edit")
	}
	if cred.Model != "claude-sonnet-4-6" {
		t.Errorf("expected updated model, got %q", cred.Model)
	}
	if cred.APIKey != "sk-ant-orig1234567890abcd" {
		t.Errorf("expected key preserved, got %q", cred.APIKey)
	}
}

// TestAuthEdit_RejectsMissingProvider — editing a provider that has no
// credential is an error with a clear "register first" hint.
func TestAuthEdit_RejectsMissingProvider(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	_, _, err := runAuthCmd(t, authFile, "edit", "anthropic", "--no-test")
	if err == nil {
		t.Fatal("expected error editing nonexistent provider")
	}
	if !strings.Contains(err.Error(), "no credential") {
		t.Errorf("expected 'no credential' in error, got %v", err)
	}
}

// TestAuthTest_NoCredential — `gil auth test` on a provider with no
// stored credential errors with the same "register first" message as
// edit, so the two surfaces share vocabulary.
func TestAuthTest_NoCredential(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	_, _, err := runAuthCmd(t, authFile, "test", "anthropic")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no credential") {
		t.Errorf("expected 'no credential' in error, got %v", err)
	}
}

// TestAuthTest_RoutesThroughTestProvider plumbs a fake builder and
// checks that `gil auth test` actually exercises credstore.TestProvider
// — same code path the wizard's smoke test uses.
func TestAuthTest_RoutesThroughTestProvider(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")

	// Register a credential.
	if _, _, err := runAuthCmd(t, authFile,
		"login", "anthropic",
		"--api-key", "sk-ant-test1234567890abcd",
		"--model", "claude-haiku-4-5",
		"--no-test",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Swap the provider builder for a fake that returns "ok".
	called := 0
	restore := credstore.SetTestProviderBuilder(func(name credstore.ProviderName, _ credstore.Credential) (provider.Provider, string, error) {
		called++
		return fakeOKProvider{}, "claude-haiku-4-5", nil
	})
	t.Cleanup(func() { credstore.SetTestProviderBuilder(restore) })

	stdout, _, err := runAuthCmd(t, authFile, "test", "anthropic")
	if err != nil {
		t.Fatalf("test: %v\nstdout: %s", err, stdout)
	}
	if called != 1 {
		t.Errorf("expected 1 call to test builder, got %d", called)
	}
	if !strings.Contains(stdout, "OK") || !strings.Contains(stdout, "claude-haiku-4-5") {
		t.Errorf("expected OK + model in output, got: %s", stdout)
	}
}

// fakeOKProvider is a stand-in provider for `gil auth test` integration
// tests — it always returns "ok" with no token usage so the assertions
// don't depend on a real API.
type fakeOKProvider struct{}

func (fakeOKProvider) Name() string { return "fake" }
func (fakeOKProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{Text: "ok", StopReason: "end_turn"}, nil
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
