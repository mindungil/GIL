package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests use a fake "rsync" binary written to a temp dir. The binary writes
// its argv to a file so the test can assert on what would have been called.

func writeFakeRsync(t *testing.T, capturePath string) (binPath string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fakersync")
	script := "#!/bin/sh\necho \"$@\" > " + capturePath + "\nexit 0\n"
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	return binPath
}

func TestSyncer_Push_BuildsExpectedArgs(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "argv")
	fakeBin := writeFakeRsync(t, capturePath)
	s := &Syncer{
		Wrapper:   &Wrapper{Host: "user@host", Port: 2222, KeyPath: "/k.pem"},
		LocalDir:  "/local",
		RemoteDir: "/remote",
		RsyncBin:  fakeBin,
	}
	require.NoError(t, s.Push(context.Background()))
	raw, _ := os.ReadFile(capturePath)
	out := strings.TrimSpace(string(raw))
	require.Contains(t, out, "-az")
	require.Contains(t, out, "--delete")
	require.Contains(t, out, "ssh -i /k.pem -p 2222")
	// Source is local-with-trailing, dest is host:remote-with-trailing
	require.Contains(t, out, "/local/")
	require.Contains(t, out, "user@host:/remote/")
	// Order: src then dst
	srcIdx := strings.Index(out, "/local/")
	dstIdx := strings.Index(out, "user@host:")
	require.Less(t, srcIdx, dstIdx)
}

func TestSyncer_Pull_SwapsSrcAndDst(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "argv")
	fakeBin := writeFakeRsync(t, capturePath)
	s := &Syncer{
		Wrapper:   &Wrapper{Host: "u@h"},
		LocalDir:  "/local",
		RemoteDir: "/remote",
		RsyncBin:  fakeBin,
	}
	require.NoError(t, s.Pull(context.Background()))
	raw, _ := os.ReadFile(capturePath)
	out := strings.TrimSpace(string(raw))
	srcIdx := strings.Index(out, "u@h:")
	dstIdx := strings.Index(out, "/local/")
	require.Less(t, srcIdx, dstIdx, "Pull: remote → local")
}

func TestSyncer_NoHost_Errors(t *testing.T) {
	s := &Syncer{Wrapper: &Wrapper{}, LocalDir: "/l", RemoteDir: "/r"}
	err := s.Push(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Host required")
}

func TestSyncer_NoLocalRemote_Errors(t *testing.T) {
	s := &Syncer{Wrapper: &Wrapper{Host: "u@h"}}
	err := s.Push(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "LocalDir and RemoteDir")
}

func TestSyncer_ExtraArgs_Passed(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "argv")
	fakeBin := writeFakeRsync(t, capturePath)
	s := &Syncer{
		Wrapper:   &Wrapper{Host: "u@h"},
		LocalDir:  "/l", RemoteDir: "/r",
		RsyncBin:  fakeBin,
		ExtraArgs: []string{"--exclude=.git/", "--exclude=node_modules/"},
	}
	require.NoError(t, s.Push(context.Background()))
	raw, _ := os.ReadFile(capturePath)
	out := string(raw)
	require.Contains(t, out, "--exclude=.git/")
	require.Contains(t, out, "--exclude=node_modules/")
}

func TestSyncer_RsyncFailure_Wrapped(t *testing.T) {
	// Use a bin that exits non-zero with a message
	dir := t.TempDir()
	bin := filepath.Join(dir, "failrsync")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho 'mock failure' >&2\nexit 1\n"), 0o755))
	s := &Syncer{
		Wrapper:  &Wrapper{Host: "u@h"},
		LocalDir: "/l", RemoteDir: "/r",
		RsyncBin: bin,
	}
	err := s.Push(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "rsync")
	require.Contains(t, err.Error(), "mock failure")
}

func TestSyncer_TrailingSlashesNormalized(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "argv")
	fakeBin := writeFakeRsync(t, capturePath)
	// LocalDir without trailing slash; verify Push adds one
	s := &Syncer{
		Wrapper:   &Wrapper{Host: "u@h"},
		LocalDir:  "/l",  // no trailing
		RemoteDir: "/r",  // no trailing
		RsyncBin:  fakeBin,
	}
	require.NoError(t, s.Push(context.Background()))
	raw, _ := os.ReadFile(capturePath)
	out := string(raw)
	require.Contains(t, out, "/l/ ")  // trailing slash forces content-copy
	require.Contains(t, out, ":/r/")
}

func TestSyncAvailable_NoPanic(t *testing.T) {
	require.NotPanics(t, func() { _ = SyncAvailable() })
}
