package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jedutools/gil/core/paths"
)

// runImport invokes the import cobra command with a fresh stdout buffer.
// We replicate the runExport helper here rather than share state because
// tests are clearer when each cmd has its own self-contained setup.
func runImport(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	cmd := importCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestImport_RoundTrip exercises the headline round-trip property: export a
// synthetic session, import the resulting JSONL into a brand-new id, then
// re-export the import — the second export should match the first modulo
// the session id and any timestamp normalisation.
func TestImport_RoundTrip(t *testing.T) {
	disableDaemon(t)
	srcID := "01TESTSESSION0010"
	writeFakeSession(t, srcID, 5)

	// 1. First export → temp file.
	exportPath := filepath.Join(t.TempDir(), "export.jsonl")
	_, err := runExport(t, []string{
		srcID,
		"--format", "jsonl",
		"--socket", "/nonexistent/sock",
		"--output", exportPath,
	})
	require.NoError(t, err)

	original, err := os.ReadFile(exportPath)
	require.NoError(t, err)
	originalLines := strings.Split(strings.TrimRight(string(original), "\n"), "\n")
	require.GreaterOrEqual(t, len(originalLines), 2, "export must have header + ≥1 event")

	// 2. Import into the same GIL_HOME (so we can `gil export` the new id).
	stdout, _, err := runImport(t, []string{
		exportPath,
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)
	require.Contains(t, stdout, "Imported")

	// Parse new id from stdout — it follows the literal "New session id: ".
	var newID string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "New session id: ") {
			newID = strings.TrimPrefix(line, "New session id: ")
			break
		}
	}
	require.NotEmpty(t, newID)
	require.NotEqual(t, srcID, newID, "import must allocate a fresh id")

	// 3. Verify the on-disk events file is byte-for-byte equivalent to the
	//    source events.jsonl (we only re-wrote header lines for spec, not
	//    event payloads).
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	srcEvents, err := os.ReadFile(filepath.Join(layout.SessionsDir(), srcID, "events", "events.jsonl"))
	require.NoError(t, err)
	dstEvents, err := os.ReadFile(filepath.Join(layout.SessionsDir(), newID, "events", "events.jsonl"))
	require.NoError(t, err)
	require.Equal(t, srcEvents, dstEvents, "imported events.jsonl must match the source byte-for-byte")

	// 4. Re-export the imported session and compare event lines.
	reExportPath := filepath.Join(t.TempDir(), "re-export.jsonl")
	_, err = runExport(t, []string{
		newID,
		"--format", "jsonl",
		"--socket", "/nonexistent/sock",
		"--output", reExportPath,
	})
	require.NoError(t, err)

	reExport, err := os.ReadFile(reExportPath)
	require.NoError(t, err)
	reLines := strings.Split(strings.TrimRight(string(reExport), "\n"), "\n")

	// Same number of lines (header + N events).
	require.Equal(t, len(originalLines), len(reLines), "re-export line count must match")

	// Event lines (everything past the header) must match exactly.
	for i := 1; i < len(originalLines); i++ {
		require.Equal(t, originalLines[i], reLines[i], "event line %d must round-trip", i)
	}

	// Header carries a different session_id and possibly different metadata
	// (e.g. status missing because no daemon row), but the spec_yaml field
	// must round-trip.
	var origHeader, reHeader jsonlMetadata
	require.NoError(t, json.Unmarshal([]byte(originalLines[0]), &origHeader))
	require.NoError(t, json.Unmarshal([]byte(reLines[0]), &reHeader))
	require.Equal(t, origHeader.SpecYAML, reHeader.SpecYAML)
	require.NotEqual(t, origHeader.SessionID, reHeader.SessionID)
	require.Equal(t, newID, reHeader.SessionID)
}

// TestImport_RejectsNonExport ensures the import refuses an arbitrary JSONL
// file that lacks the export sentinel — we don't want users to accidentally
// "import" a random log and get a half-broken session id back.
func TestImport_RejectsNonExport(t *testing.T) {
	disableDaemon(t)
	t.Setenv("GIL_HOME", t.TempDir())

	bogus := filepath.Join(t.TempDir(), "bogus.jsonl")
	require.NoError(t, os.WriteFile(bogus, []byte(`{"hello":"world"}`+"\n"), 0o644))

	_, _, err := runImport(t, []string{bogus, "--socket", "/nonexistent/sock"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a gil session export")
}

func TestImport_MissingFile(t *testing.T) {
	disableDaemon(t)
	t.Setenv("GIL_HOME", t.TempDir())

	_, _, err := runImport(t, []string{"/nonexistent/file.jsonl", "--socket", "/nonexistent/sock"})
	require.Error(t, err)
}

// TestImport_PreservesSpec checks that spec.yaml from the export header is
// written to the new session directory.
func TestImport_PreservesSpec(t *testing.T) {
	disableDaemon(t)
	srcID := "01TESTSESSION0011"
	writeFakeSession(t, srcID, 3)

	exportPath := filepath.Join(t.TempDir(), "export.jsonl")
	_, err := runExport(t, []string{
		srcID,
		"--format", "jsonl",
		"--socket", "/nonexistent/sock",
		"--output", exportPath,
	})
	require.NoError(t, err)

	stdout, _, err := runImport(t, []string{
		exportPath,
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)

	var newID string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "New session id: ") {
			newID = strings.TrimPrefix(line, "New session id: ")
			break
		}
	}
	require.NotEmpty(t, newID)

	layout, err := paths.FromEnv()
	require.NoError(t, err)
	specPath := filepath.Join(layout.SessionsDir(), newID, "spec.yaml")
	got, err := os.ReadFile(specPath)
	require.NoError(t, err)
	require.Contains(t, string(got), "synthetic test goal")
}
