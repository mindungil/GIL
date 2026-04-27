// Package specstore persists FrozenSpec as spec.yaml in a session directory,
// with optional spec.lock for tamper detection.
package specstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/mindungil/gil/core/spec"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// ErrFrozen is returned when Save is called on a store that has a spec.lock.
var ErrFrozen = errors.New("spec is frozen; create new session to modify")

// ErrLockMismatch is returned by Load when the on-disk spec content does not match
// the SHA-256 in spec.lock (tamper detection).
var ErrLockMismatch = errors.New("spec content does not match lock (tampered)")

// ErrNotFound is returned when no spec.yaml exists in the store directory.
var ErrNotFound = errors.New("spec not found")

// Store reads/writes spec.yaml and spec.lock under dir.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) yamlPath() string { return filepath.Join(s.dir, "spec.yaml") }
func (s *Store) lockPath() string { return filepath.Join(s.dir, "spec.lock") }

// IsFrozen reports whether spec.lock exists.
func (s *Store) IsFrozen() bool {
	_, err := os.Stat(s.lockPath())
	return err == nil
}

// Save writes the spec as YAML. Returns ErrFrozen if spec.lock exists.
func (s *Store) Save(fs *gilv1.FrozenSpec) error {
	if s.IsFrozen() {
		return ErrFrozen
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("specstore.Save mkdir: %w", err)
	}
	data, err := protoToYAML(fs)
	if err != nil {
		return fmt.Errorf("specstore.Save marshal: %w", err)
	}
	if err := os.WriteFile(s.yamlPath(), data, 0o644); err != nil {
		return fmt.Errorf("specstore.Save write: %w", err)
	}
	return nil
}

// Load reads spec.yaml and, if spec.lock exists, verifies the proto hash.
// Returns ErrNotFound if missing, ErrLockMismatch on tamper.
func (s *Store) Load() (*gilv1.FrozenSpec, error) {
	data, err := os.ReadFile(s.yamlPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("specstore.Load read: %w", err)
	}
	fs, err := yamlToProto(data)
	if err != nil {
		return nil, fmt.Errorf("specstore.Load unmarshal: %w", err)
	}
	if s.IsFrozen() {
		ok, err := spec.VerifyLock(fs)
		if err != nil {
			return nil, fmt.Errorf("specstore.Load verify: %w", err)
		}
		if !ok {
			return nil, ErrLockMismatch
		}
	}
	return fs, nil
}

// Freeze loads the current spec, computes its proto hash via spec.Freeze,
// persists the hash in fs.ContentSha256, re-saves spec.yaml with the hash
// field set, and writes spec.lock containing the proto hash hex for tamper detection.
func (s *Store) Freeze() error {
	if s.IsFrozen() {
		return nil // already frozen, idempotent
	}
	fs, err := s.Load()
	if err != nil {
		return err
	}
	hex, err := spec.Freeze(fs)
	if err != nil {
		return fmt.Errorf("specstore.Freeze: %w", err)
	}
	// Re-save yaml with ContentSha256 populated.
	data, err := protoToYAML(fs)
	if err != nil {
		return fmt.Errorf("specstore.Freeze marshal: %w", err)
	}
	if err := os.WriteFile(s.yamlPath(), data, 0o644); err != nil {
		return fmt.Errorf("specstore.Freeze write yaml: %w", err)
	}
	// Write proto hash hex for tamper detection.
	if err := os.WriteFile(s.lockPath(), []byte(hex), 0o644); err != nil {
		return fmt.Errorf("specstore.Freeze write lock: %w", err)
	}
	return nil
}

// protoToYAML converts a FrozenSpec to YAML via JSON intermediate (so that
// proto field names and enum values are preserved consistently).
func protoToYAML(fs *gilv1.FrozenSpec) ([]byte, error) {
	jsonBytes, err := protojson.Marshal(fs)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(jsonBytes, &generic); err != nil {
		return nil, err
	}
	return yaml.Marshal(generic)
}

// yamlToProto is the inverse of protoToYAML.
func yamlToProto(data []byte) (*gilv1.FrozenSpec, error) {
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	fs := &gilv1.FrozenSpec{}
	if err := protojson.Unmarshal(jsonBytes, fs); err != nil {
		return nil, err
	}
	return fs, nil
}
