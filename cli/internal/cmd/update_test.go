package cmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/cliutil"
)

// withSeams swaps the package-level test seams atomically and returns
// a cleanup that restores them. Centralising the swap keeps each test
// terse and prevents one test's leftover monkey patch from leaking
// into the next when -count is bumped.
func withSeams(t *testing.T,
	exe func() (string, error),
	read func(string) ([]byte, error),
	stat func(string) (fs.FileInfo, error),
	fetch func(context.Context) (string, error),
	shell func(context.Context, string, ...string) error,
) {
	t.Helper()
	origExe := executableFn
	origRead := readFileFn
	origStat := statFn
	origFetch := fetchLatestTagFn
	origShell := shellOut
	if exe != nil {
		executableFn = exe
	}
	if read != nil {
		readFileFn = read
	}
	if stat != nil {
		statFn = stat
	}
	if fetch != nil {
		fetchLatestTagFn = fetch
	}
	if shell != nil {
		shellOut = shell
	}
	t.Cleanup(func() {
		executableFn = origExe
		readFileFn = origRead
		statFn = origStat
		fetchLatestTagFn = origFetch
		shellOut = origShell
	})
}

// fakeExe writes a fake gil binary in a tmp dir and returns an
// executableFn that points os.Executable at it. Tests pair this with
// a real os.ReadFile so the marker-file lookup goes through the real
// filesystem (cheap + catches path-join bugs the pure-mock would miss).
func fakeExe(t *testing.T, contents string) (string, func() (string, error)) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "gil")
	require.NoError(t, os.WriteFile(exe, []byte("fake binary"), 0o755))
	if contents != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, markerFile), []byte(contents), 0o644))
	}
	return dir, func() (string, error) { return exe, nil }
}

// -------------------------------------------------------------------------
// detectInstallMethod
// -------------------------------------------------------------------------

func TestDetectInstallMethod_ScriptMarker(t *testing.T) {
	_, exe := fakeExe(t, "script\n")
	withSeams(t, exe, os.ReadFile, os.Stat, nil, nil)

	require.Equal(t, installerScript, detectInstallMethod())
}

func TestDetectInstallMethod_BrewMarker(t *testing.T) {
	_, exe := fakeExe(t, "brew")
	withSeams(t, exe, os.ReadFile, os.Stat, nil, nil)

	require.Equal(t, installerBrew, detectInstallMethod())
}

func TestDetectInstallMethod_NoMarkerFallsBackToManual(t *testing.T) {
	_, exe := fakeExe(t, "")
	withSeams(t, exe, os.ReadFile, os.Stat, nil, nil)

	require.Equal(t, installerManual, detectInstallMethod())
}

func TestDetectInstallMethod_UnknownMarkerContentsTreatedAsManual(t *testing.T) {
	// An unrecognised marker value (someone hand-edits the file or
	// future versions add a new channel we don't know about) must
	// fall through to "manual" rather than misclassifying.
	_, exe := fakeExe(t, "npm\n")
	withSeams(t, exe, os.ReadFile, os.Stat, nil, nil)

	require.Equal(t, installerManual, detectInstallMethod())
}

func TestDetectInstallMethod_BrewPrefixDetectedWithoutMarker(t *testing.T) {
	// Simulate /opt/homebrew/bin/gil with no marker file. We do this
	// by lying about os.Executable's return — the marker lookup will
	// miss (no file at that location) and the fallback brew-prefix
	// detector should still return "brew".
	exe := func() (string, error) { return "/opt/homebrew/bin/gil", nil }
	read := func(string) ([]byte, error) { return nil, errors.New("not found") }
	withSeams(t, exe, read, os.Stat, nil, nil)

	require.Equal(t, installerBrew, detectInstallMethod())
}

func TestDetectInstallMethod_HomebrewPrefixEnvOverride(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "/custom/brew")
	exe := func() (string, error) { return "/custom/brew/bin/gil", nil }
	read := func(string) ([]byte, error) { return nil, errors.New("nope") }
	withSeams(t, exe, read, os.Stat, nil, nil)

	require.Equal(t, installerBrew, detectInstallMethod())
}

func TestDetectInstallMethod_UsrLocalRequiresCellarSibling(t *testing.T) {
	// /usr/local/bin is shared with the curl-installer. Without a
	// /usr/local/Cellar directory we must NOT classify as brew.
	exe := func() (string, error) { return "/usr/local/bin/gil", nil }
	read := func(string) ([]byte, error) { return nil, errors.New("nope") }
	stat := func(p string) (fs.FileInfo, error) {
		return nil, errors.New("no cellar")
	}
	withSeams(t, exe, read, stat, nil, nil)

	require.Equal(t, installerManual, detectInstallMethod())
}

func TestDetectInstallMethod_UsrLocalWithCellarIsBrew(t *testing.T) {
	exe := func() (string, error) { return "/usr/local/bin/gil", nil }
	read := func(string) ([]byte, error) { return nil, errors.New("nope") }
	stat := func(p string) (fs.FileInfo, error) {
		if p == "/usr/local/Cellar" {
			return fakeFileInfo{}, nil
		}
		return nil, errors.New("not found")
	}
	withSeams(t, exe, read, stat, nil, nil)

	require.Equal(t, installerBrew, detectInstallMethod())
}

// -------------------------------------------------------------------------
// update --check
// -------------------------------------------------------------------------

func TestUpdateCheck_PrintsLatestTag(t *testing.T) {
	withSeams(t, nil, nil, nil,
		func(context.Context) (string, error) { return "v0.1.0", nil },
		func(context.Context, string, ...string) error {
			t.Fatal("shellOut must not be invoked under --check")
			return nil
		})

	var buf bytes.Buffer
	cmd := updateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--check"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	out := buf.String()
	require.Contains(t, out, "Latest gil release: v0.1.0")
	require.Contains(t, out, "Installed gil:")
}

func TestUpdateCheck_NetworkErrorBecomesUserError(t *testing.T) {
	withSeams(t, nil, nil, nil,
		func(context.Context) (string, error) { return "", errors.New("dial tcp: no route") },
		nil)

	var buf bytes.Buffer
	cmd := updateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--check"})
	err := cmd.ExecuteContext(context.Background())

	require.Error(t, err)
	var ue *cliutil.UserError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Msg, "GitHub releases API")
	require.Contains(t, ue.Hint, "github.com/mindungil/gil/releases")
}

func TestUpdateCheck_PromptsToUpgradeWhenNewerAvailable(t *testing.T) {
	// Stamp a fake "current" version so the version-comparison branch
	// runs. We restore injectedVersion via t.Cleanup.
	prev := injectedVersion
	injectedVersion = "0.0.9"
	t.Cleanup(func() { injectedVersion = prev })

	withSeams(t, nil, nil, nil,
		func(context.Context) (string, error) { return "v0.1.0", nil }, nil)

	var buf bytes.Buffer
	cmd := updateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--check"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	require.Contains(t, buf.String(), "Run 'gil update' to upgrade.")
}

// -------------------------------------------------------------------------
// update dispatch
// -------------------------------------------------------------------------

func TestUpdate_BrewMarkerDispatchesBrewUpgrade(t *testing.T) {
	_, exe := fakeExe(t, "brew")
	var got []string
	withSeams(t, exe, os.ReadFile, os.Stat, nil,
		func(_ context.Context, name string, args ...string) error {
			got = append([]string{name}, args...)
			return nil
		})

	cmd := updateCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(nil)
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	require.Equal(t, []string{"brew", "upgrade", "gil"}, got)
}

func TestUpdate_ScriptMarkerDispatchesCurlInstaller(t *testing.T) {
	_, exe := fakeExe(t, "script")
	var got []string
	withSeams(t, exe, os.ReadFile, os.Stat, nil,
		func(_ context.Context, name string, args ...string) error {
			got = append([]string{name}, args...)
			return nil
		})

	cmd := updateCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(nil)
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	require.Len(t, got, 3)
	require.Equal(t, "bash", got[0])
	require.Equal(t, "-c", got[1])
	require.Contains(t, got[2], "curl -fsSL")
	require.Contains(t, got[2], installScriptURL)
}

func TestUpdate_ManualReturnsUserErrorAndDoesNotShellOut(t *testing.T) {
	_, exe := fakeExe(t, "") // no marker
	withSeams(t, exe, os.ReadFile, os.Stat, nil,
		func(context.Context, string, ...string) error {
			t.Fatal("shellOut must not run for manual installs")
			return nil
		})

	cmd := updateCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(context.Background())

	require.Error(t, err)
	var ue *cliutil.UserError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Msg, "manually")
	require.Contains(t, ue.Hint, "docs/distribution.md")
}

func TestUpdate_ShellOutFailureBubblesUpAsUserError(t *testing.T) {
	_, exe := fakeExe(t, "brew")
	withSeams(t, exe, os.ReadFile, os.Stat, nil,
		func(context.Context, string, ...string) error {
			// Use the real shellOutExec error wrapping by calling it
			// — but we can't, since exec would actually run brew.
			// Instead return a wrapped UserError to mimic what
			// shellOutExec produces for a failing command.
			return cliutil.Wrap(errors.New("exit 1"),
				"upgrade command failed: brew upgrade gil",
				"check the output above; rerun with the failing command directly to see full error")
		})

	cmd := updateCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(context.Background())

	require.Error(t, err)
	var ue *cliutil.UserError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Msg, "upgrade command failed")
}

// -------------------------------------------------------------------------
// Wiring
// -------------------------------------------------------------------------

func TestUpdate_RegisteredOnRoot(t *testing.T) {
	root := Root()
	var found *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "update" {
			found = c
			break
		}
	}
	require.NotNil(t, found, "update subcommand must be registered on root")
	// --check must exist; it's the only flag we promise.
	require.NotNil(t, found.Flags().Lookup("check"), "--check flag must be wired")
}

// -------------------------------------------------------------------------
// helpers
// -------------------------------------------------------------------------

// fakeFileInfo is a minimal os.FileInfo for the stat seam. Only the
// methods callers in this package actually use are filled in; ModTime
// returns the zero value because none of the production paths inspect
// it.
type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "Cellar" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }

// Compile-time assertion that fakeFileInfo satisfies fs.FileInfo so a
// future signature drift in the interface fails at build time, not at
// test runtime.
var _ fs.FileInfo = fakeFileInfo{}
