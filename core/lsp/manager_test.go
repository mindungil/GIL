package lsp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManager_NoServer_ForUnknownExtension(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.ClientFor(context.Background(), "/tmp/foo.unknownlanguage")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoServer), "expected ErrNoServer, got %v", err)
}

func TestManager_NoServer_ForNoExtension(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.ClientFor(context.Background(), "/tmp/Makefile")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoServer))
}

func TestManager_ServerUnavailable_ReturnsInstallHint(t *testing.T) {
	m := NewManager(t.TempDir())
	// Force every config to be unavailable.
	for ext, cfg := range m.Configs {
		cfg.Available = func() bool { return false }
		m.Configs[ext] = cfg
	}
	_, err := m.ClientFor(context.Background(), "/tmp/foo.go")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrServerUnavailable))
	require.Contains(t, err.Error(), "gopls", "error should reference the missing server")
}

func TestManager_HasServerFor(t *testing.T) {
	m := NewManager(t.TempDir())
	require.True(t, m.HasServerFor("foo.go"))
	require.True(t, m.HasServerFor("foo.py"))
	require.True(t, m.HasServerFor("foo.ts"))
	require.True(t, m.HasServerFor("foo.tsx"))
	require.True(t, m.HasServerFor("foo.rs"))
	require.False(t, m.HasServerFor("foo.unknownlanguage"))
}

func TestManager_LanguageIDFor(t *testing.T) {
	m := NewManager(t.TempDir())
	require.Equal(t, "go", m.LanguageIDFor("x.go"))
	require.Equal(t, "typescript", m.LanguageIDFor("x.ts"))
	require.Equal(t, "typescriptreact", m.LanguageIDFor("x.tsx"))
	require.Equal(t, "javascript", m.LanguageIDFor("x.js"))
	require.Equal(t, "", m.LanguageIDFor("x.unknown"))
}

func TestManager_FailedSpawnIsSticky(t *testing.T) {
	m := NewManager(t.TempDir())
	// Configure a fake .xx that's "available" but the binary will fail to start.
	m.Configs[".xx"] = ServerConfig{
		Language:   "xx",
		LanguageID: "xx",
		Command:    []string{"/no/such/binary/anywhere"},
		Available:  func() bool { return true },
	}
	_, err1 := m.ClientFor(context.Background(), "/tmp/foo.xx")
	require.Error(t, err1)
	// Second call should hit the sticky-failure cache and return the same kind of error fast.
	_, err2 := m.ClientFor(context.Background(), "/tmp/foo.xx")
	require.Error(t, err2)
}

func TestManager_Shutdown_NoSpawnedServers(t *testing.T) {
	m := NewManager(t.TempDir())
	require.NoError(t, m.Shutdown(context.Background()))
}
