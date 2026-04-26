package credstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// fileSchemaVersion is bumped when the on-disk shape changes incompatibly.
// We deliberately tolerate a missing version in old files (treated as v1) so
// installations created before this constant was wired don't fail to load.
const fileSchemaVersion = 1

// fileFormat is the literal JSON shape written to auth.json. Keeping it as a
// distinct type from the public Credential map gives us room to add
// out-of-band fields (e.g. last-migrated-at) without breaking the public API.
type fileFormat struct {
	Version   int                            `json:"version"`
	Providers map[ProviderName]Credential    `json:"providers"`
	// Extra catches any fields we don't recognise so a forward-compatible
	// write doesn't drop them. JSON unmarshalling without this would silently
	// strip unknown keys.
	Extra map[string]json.RawMessage `json:"-"`
}

// FileStore is a JSON-on-disk Store. The on-disk file is owned by the
// invoking user and intended for single-user systems; cross-process
// concurrent writes use a "last writer wins" strategy because file locking
// portability across Linux/macOS/Windows is not worth the complexity for a
// human-driven `gil auth login` flow.
//
// Reference: opencode's auth/index.ts uses the same atomic-write + 0600
// strategy. The main divergence is that opencode keys the providers map
// directly at the top level whereas we wrap providers in a versioned
// envelope so future schema migrations don't require sniffing.
type FileStore struct {
	// Path is the absolute path to auth.json. Callers typically pass
	// `<base>/auth.json` (or in Phase 11 Track A, layout.AuthFile()).
	Path string

	// mu serialises in-process Set/Remove calls so concurrent goroutines
	// don't both read-modify-write and overwrite each other's changes. Get
	// also takes the read side for consistency with concurrent writers.
	mu sync.RWMutex
}

// NewFileStore returns a FileStore writing to path. The file and its parent
// directory are not created until the first Set call, so it is safe to
// construct a store for a path that does not yet exist.
func NewFileStore(path string) *FileStore {
	return &FileStore{Path: path}
}

// load reads and parses the auth.json file. A missing file is not an error —
// it simply yields an empty providers map, which is the correct state for a
// fresh install. A malformed file IS an error; we deliberately don't try to
// salvage it because silently dropping credentials would be worse than
// failing loudly.
func (s *FileStore) load() (fileFormat, error) {
	f := fileFormat{Version: fileSchemaVersion, Providers: map[ProviderName]Credential{}}

	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return f, nil
		}
		return f, fmt.Errorf("credstore: read %s: %w", s.Path, err)
	}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("credstore: parse %s: %w", s.Path, err)
	}
	if f.Providers == nil {
		f.Providers = map[ProviderName]Credential{}
	}
	if f.Version == 0 {
		// Legacy file (pre-version field) — treat as v1.
		f.Version = fileSchemaVersion
	}
	return f, nil
}

// save atomically writes the provided fileFormat to disk. The strategy is
// the standard "write tmp, fsync, rename" dance: even an unclean process
// exit between write and rename leaves the existing auth.json untouched.
//
// On POSIX systems we explicitly chmod the result to 0600 because os.OpenFile
// honours umask which could widen permissions. On Windows we cannot replicate
// the POSIX permission model with the standard library alone, so we emit a
// one-line warning to stderr — the user is informed but the operation
// proceeds (failing closed would mean Windows users couldn't store creds at
// all).
func (s *FileStore) save(f fileFormat) error {
	f.Version = fileSchemaVersion
	if f.Providers == nil {
		f.Providers = map[ProviderName]Credential{}
	}

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credstore: create dir %s: %w", dir, err)
	}

	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("credstore: marshal: %w", err)
	}
	body = append(body, '\n')

	tmp, err := os.CreateTemp(dir, ".auth.json.*.tmp")
	if err != nil {
		return fmt.Errorf("credstore: tempfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("credstore: write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("credstore: fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("credstore: close tmp: %w", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			cleanup()
			return fmt.Errorf("credstore: chmod 0600 %s: %w", tmpPath, err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "credstore: warning: 0600 file permissions are not enforced on Windows; ensure the file is not readable by other users")
	}

	if err := os.Rename(tmpPath, s.Path); err != nil {
		cleanup()
		return fmt.Errorf("credstore: rename tmp to %s: %w", s.Path, err)
	}

	// Best-effort fsync of the parent directory so the rename is durable.
	// Errors here are non-fatal: some filesystems (notably some FUSE
	// mounts) reject directory fsyncs, and the file content has already
	// been fsync'd to disk above.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// List returns the names of all providers with stored credentials.
func (s *FileStore) List(_ context.Context) ([]ProviderName, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]ProviderName, 0, len(f.Providers))
	for name := range f.Providers {
		out = append(out, name)
	}
	return out, nil
}

// Get returns the credential for name, or (nil, nil) if not configured.
func (s *FileStore) Get(_ context.Context, name ProviderName) (*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, err := s.load()
	if err != nil {
		return nil, err
	}
	c, ok := f.Providers[name]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

// Set writes the credential for name, stamping Updated to the current time.
func (s *FileStore) Set(_ context.Context, name ProviderName, cred Credential) error {
	if name == "" {
		return errors.New("credstore: empty provider name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.load()
	if err != nil {
		return err
	}
	cred.Updated = time.Now().UTC()
	f.Providers[name] = cred
	return s.save(f)
}

// Remove deletes the credential for name. Idempotent: removing a name with
// no entry succeeds.
func (s *FileStore) Remove(_ context.Context, name ProviderName) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := f.Providers[name]; !ok {
		return nil
	}
	delete(f.Providers, name)
	return s.save(f)
}

// Compile-time assertion that *FileStore satisfies Store.
var _ Store = (*FileStore)(nil)
