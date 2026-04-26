package specstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
)

func validSpec() *gilv1.FrozenSpec {
	return &gilv1.FrozenSpec{
		SpecId:    "01TEST",
		SessionId: "01SESS",
		Goal: &gilv1.Goal{
			OneLiner:               "x",
			SuccessCriteriaNatural: []string{"a", "b", "c"},
		},
		Constraints: &gilv1.Constraints{TechStack: []string{"go"}},
		Verification: &gilv1.Verification{
			Checks: []*gilv1.Check{{Name: "build", Kind: gilv1.CheckKind_SHELL, Command: "go build"}},
		},
		Workspace: &gilv1.Workspace{Backend: gilv1.WorkspaceBackend_LOCAL_SANDBOX},
		Models:    &gilv1.ModelConfig{Main: &gilv1.ModelChoice{Provider: "anthropic", ModelId: "claude-opus-4-7"}},
		Risk:      &gilv1.RiskProfile{Autonomy: gilv1.AutonomyDial_FULL},
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))

	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, fs.SpecId, got.SpecId)
	require.Equal(t, fs.Goal.OneLiner, got.Goal.OneLiner)
	require.Equal(t, fs.Goal.SuccessCriteriaNatural, got.Goal.SuccessCriteriaNatural)
}

func TestStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_, err := s.Load()
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStore_FreezeWritesLock(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))
	require.NoError(t, s.Freeze())

	// lock 파일 존재
	_, err := os.Stat(filepath.Join(dir, "spec.lock"))
	require.NoError(t, err)

	// IsFrozen returns true
	require.True(t, s.IsFrozen())

	// freeze 후 Save 시도하면 ErrFrozen
	fs.Goal.OneLiner = "tampered"
	err = s.Save(fs)
	require.ErrorIs(t, err, ErrFrozen)
}

func TestStore_VerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))
	require.NoError(t, s.Freeze())

	// Tamper: change a real field value in yaml that survives round-trip
	yamlPath := filepath.Join(dir, "spec.yaml")
	data, err := os.ReadFile(yamlPath)
	require.NoError(t, err)
	tampered := bytes.Replace(data, []byte("oneLiner: x"), []byte("oneLiner: tampered"), 1)
	require.NoError(t, os.WriteFile(yamlPath, tampered, 0o644))

	_, err = s.Load()
	require.ErrorIs(t, err, ErrLockMismatch)
}

func TestStore_LoadAfterFreeze_NoTamper_OK(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	fs := validSpec()
	require.NoError(t, s.Save(fs))
	require.NoError(t, s.Freeze())

	// 변형 없이 그대로 Load
	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, fs.SpecId, got.SpecId)
}
