package credstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestGet_MissingFile asserts that Get on a non-existent auth.json returns
// (nil, nil) — the canonical "not configured" signal that the CLI uses to
// fall back to env vars.
func TestGet_MissingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "auth.json"))
	cred, err := store.Get(context.Background(), Anthropic)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if cred != nil {
		t.Fatalf("expected nil credential for missing file, got %+v", cred)
	}
}

// TestSetGet_Roundtrip writes a credential and reads it back, verifying the
// happy-path persistence cycle within a single FileStore instance.
func TestSetGet_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "auth.json"))

	in := Credential{Type: CredAPI, APIKey: "sk-ant-test1234567890abcd"}
	if err := store.Set(context.Background(), Anthropic, in); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(context.Background(), Anthropic)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatalf("expected credential, got nil")
	}
	if got.APIKey != in.APIKey {
		t.Errorf("APIKey: got %q want %q", got.APIKey, in.APIKey)
	}
	if got.Type != CredAPI {
		t.Errorf("Type: got %q want %q", got.Type, CredAPI)
	}
	if got.Updated.IsZero() {
		t.Errorf("expected Updated to be stamped, got zero time")
	}
}

// TestPersistAcrossInstances is the test that actually proves we hit disk:
// a fresh FileStore at the same path must see what the previous one wrote.
func TestPersistAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	first := NewFileStore(path)
	if err := first.Set(context.Background(), OpenAI, Credential{Type: CredAPI, APIKey: "sk-test-abcdefghijkl"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	second := NewFileStore(path)
	got, err := second.Get(context.Background(), OpenAI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.APIKey != "sk-test-abcdefghijkl" {
		t.Fatalf("expected key to round-trip, got %+v", got)
	}
}

// TestList returns every provider that's been Set and stays in sync with
// Remove.
func TestList(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "auth.json"))
	ctx := context.Background()

	if err := store.Set(ctx, Anthropic, Credential{Type: CredAPI, APIKey: "sk-ant-aaaaaaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, OpenAI, Credential{Type: CredAPI, APIKey: "sk-bbbbbbbbbbbbbb"}); err != nil {
		t.Fatal(err)
	}

	names, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 providers, got %d (%v)", len(names), names)
	}

	if err := store.Remove(ctx, Anthropic); err != nil {
		t.Fatal(err)
	}
	names, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != OpenAI {
		t.Fatalf("expected just openai after remove, got %v", names)
	}
}

// TestRemove_Idempotent confirms removing an unknown provider succeeds (so
// the CLI can run `gil auth logout foo` without surprising errors).
func TestRemove_Idempotent(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "auth.json"))
	if err := store.Remove(context.Background(), Anthropic); err != nil {
		t.Fatalf("Remove on empty store: %v", err)
	}
}

// TestFilePermissions verifies the on-disk file is mode 0600 after Set on
// POSIX systems. Skipped on Windows where the standard library cannot
// enforce the POSIX permission model.
func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("0600 enforcement is not available on Windows")
	}
	// We point the store at a fresh nested directory we expect *credstore*
	// to create, so we can assert the auto-created dir is 0700 even if the
	// surrounding TempDir was created with a wider mode by the test runner.
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	path := filepath.Join(dir, "auth.json")
	store := NewFileStore(path)
	if err := store.Set(context.Background(), Anthropic, Credential{Type: CredAPI, APIKey: "sk-ant-1234567890ab"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("expected mode 0600, got %o", mode)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("auto-created parent dir mode %o is too permissive", mode)
	}
}

// TestAtomicWrite_ExistingFileSurvivesGarbageInPath confirms the temp-file
// strategy: even if a leftover *.tmp exists in the directory, the rename
// produces the canonical auth.json and the existing one is replaced
// atomically. This isn't a full crash simulation (Go can't kill -9 itself)
// but it covers the directory-clean invariant.
func TestAtomicWrite_LeavesNoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	store := NewFileStore(path)

	for i := 0; i < 5; i++ {
		if err := store.Set(context.Background(), Anthropic, Credential{Type: CredAPI, APIKey: "sk-ant-roundN"}); err != nil {
			t.Fatalf("Set #%d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found leftover tmp file: %s", e.Name())
		}
	}
}

// TestConcurrent_NoRace runs many concurrent Set/Get/Remove against a single
// FileStore. The race detector (`go test -race`) is the actual assertion
// here — we just need to keep the goroutines busy long enough for it to
// notice anything wrong.
func TestConcurrent_NoRace(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "auth.json"))
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = store.Set(ctx, ProviderName(provName(i)), Credential{Type: CredAPI, APIKey: "sk-test-xxxxxxxxxx"})
				_, _ = store.Get(ctx, ProviderName(provName(i)))
				if j%5 == 0 {
					_ = store.Remove(ctx, ProviderName(provName(i)))
				}
			}
		}(i)
	}
	wg.Wait()
}

func provName(i int) string {
	return "p" + string(rune('0'+i))
}

// TestCorruptFile_ReturnsError makes the failure mode explicit: a malformed
// auth.json yields an error from load rather than silently overwriting the
// file with empty contents.
func TestCorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(path)
	if _, err := store.List(context.Background()); err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

// TestEmptyFile_TreatedAsNoCreds ensures a zero-byte auth.json (which can
// happen if the user touched the file manually) is treated as "no
// credentials" rather than a parse error.
func TestEmptyFile_TreatedAsNoCreds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(path)
	names, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for empty file, got %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no providers, got %v", names)
	}
}

// TestFileSchema verifies the on-disk JSON has the documented top-level
// shape: a "version" field and a "providers" object. This is the primary
// guard against accidental schema drift.
func TestFileSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	store := NewFileStore(path)
	if err := store.Set(context.Background(), Anthropic, Credential{Type: CredAPI, APIKey: "sk-ant-aaaabbbbccccdddd"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["version"]; !ok {
		t.Errorf("expected top-level 'version' field, got keys: %v", keysOf(raw))
	}
	if _, ok := raw["providers"]; !ok {
		t.Errorf("expected top-level 'providers' field, got keys: %v", keysOf(raw))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestMaskedKey covers the user-visible masking logic: long keys keep prefix
// and suffix, short keys star-out completely, OAuth/wellknown stay redacted.
func TestMaskedKey(t *testing.T) {
	tests := []struct {
		name string
		cred Credential
		want string
	}{
		{
			name: "long anthropic key",
			cred: Credential{Type: CredAPI, APIKey: "sk-ant-1234567890abcdef3f2a"},
			want: "sk-ant-...3f2a",
		},
		{
			name: "long openai key",
			cred: Credential{Type: CredAPI, APIKey: "sk-1234567890abcdef9999"},
			want: "sk-1234...9999",
		},
		{
			name: "short key",
			cred: Credential{Type: CredAPI, APIKey: "sk-short"},
			want: "***",
		},
		{
			name: "empty key",
			cred: Credential{Type: CredAPI, APIKey: ""},
			want: "",
		},
		{
			name: "oauth",
			cred: Credential{Type: CredOAuth, OAuth: &OAuthCred{AccessToken: "abcdefghijklmnop"}},
			want: "abcdefg...mnop",
		},
		{
			name: "oauth nil",
			cred: Credential{Type: CredOAuth},
			want: "***",
		},
		{
			name: "wellknown",
			cred: Credential{Type: CredWellKnown, APIKey: "anything"},
			want: "***",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cred.MaskedKey(); got != tc.want {
				t.Errorf("MaskedKey: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestKnownProviders is a sanity check that the well-known set contains the
// providers gil currently advertises in its picker.
func TestKnownProviders(t *testing.T) {
	want := map[ProviderName]bool{Anthropic: false, OpenAI: false, OpenRouter: false, VLLM: false}
	for _, p := range KnownProviders() {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected provider %q in KnownProviders", p)
			continue
		}
		want[p] = true
	}
	for p, found := range want {
		if !found {
			t.Errorf("KnownProviders missing %q", p)
		}
	}
}
