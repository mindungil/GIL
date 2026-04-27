package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/paths"
)

// writeFakeSession creates a sessions/<id>/ directory tree containing a
// spec.yaml and an events.jsonl populated with `n` events. It returns the
// absolute paths of the resolved layout so tests can pass GIL_HOME-style
// overrides as well as the per-session directory.
//
// We populate events directly via event.NewPersister so the JSONL bytes are
// produced exactly as gild would write them — including secret masking and
// timestamp formatting — without needing a live daemon.
func writeFakeSession(t *testing.T, id string, n int) (paths.Layout, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIL_HOME", root)
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	sessionDir := filepath.Join(layout.SessionsDir(), id)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// Minimal spec.yaml so the spec section renders.
	specYAML := []byte("goal:\n  one_liner: synthetic test goal\n  detailed: detailed test goal description\nverification:\n  checks:\n    - kind: CHECK_KIND_TEST\n      command: \"go test ./...\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spec.yaml"), specYAML, 0o644))

	// Events.
	persister, err := event.NewPersister(filepath.Join(sessionDir, "events"))
	require.NoError(t, err)
	defer persister.Close()

	tm := time.Date(2026, 4, 26, 19, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		var (
			src   event.Source
			kind  event.Kind
			etype string
			data  []byte
		)
		switch i {
		case 1:
			src, kind, etype = event.SourceSystem, event.KindNote, "iteration_start"
			data = []byte(`{"iter":1}`)
		case 2:
			src, kind, etype = event.SourceAgent, event.KindAction, "tool_call"
			data = []byte(`{"name":"bash","input":{"command":"ls"}}`)
		case 3:
			src, kind, etype = event.SourceEnvironment, event.KindObservation, "tool_result"
			data = []byte(`{"output":"README.md src/"}`)
		case 4:
			src, kind, etype = event.SourceAgent, event.KindObservation, "provider_response"
			data = []byte(`{"text":"Let me look at README.md..."}`)
		case 5:
			src, kind, etype = event.SourceSystem, event.KindAction, "verify_run"
			data = nil
		case 6:
			src, kind, etype = event.SourceEnvironment, event.KindObservation, "verify_result"
			data = []byte(`{"passed":true,"checks":3}`)
		default:
			src, kind, etype = event.SourceSystem, event.KindNote, "run_done"
			data = []byte(`{"iterations":1,"tokens":1234}`)
		}
		require.NoError(t, persister.Write(event.Event{
			ID:        int64(i),
			Timestamp: tm.Add(time.Duration(i) * time.Second),
			Source:    src,
			Kind:      kind,
			Type:      etype,
			Data:      data,
		}))
	}
	require.NoError(t, persister.Sync())

	return layout, sessionDir
}

func runExport(t *testing.T, args []string) (string, error) {
	t.Helper()
	cmd := exportCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

// loadSessionMeta tries to spawn gild and we don't want that in tests; force
// a non-existent socket so ensureDaemon fails fast (then loadSessionMeta
// falls back to disk-only).
func disableDaemon(t *testing.T) {
	t.Helper()
	// Point socket flag at a path that nothing listens on. ensureDaemon will
	// try to exec gild — if gild is not on PATH (typical for `go test`),
	// ensureDaemon returns an error and loadSessionMeta proceeds without
	// the row. To make the fail mode deterministic regardless of whether
	// gild happens to be on PATH we strip PATH for the duration of the test.
	t.Setenv("PATH", "")
}

func TestExport_MarkdownContainsAllSections(t *testing.T) {
	disableDaemon(t)
	layout, _ := writeFakeSession(t, "01TESTSESSION0001", 7)
	_ = layout

	out, err := runExport(t, []string{
		"01TESTSESSION0001",
		"--format", "markdown",
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)

	// Header and key sections.
	require.Contains(t, out, "# gil session 01TESTSESSION0001")
	require.Contains(t, out, "## Spec")
	require.Contains(t, out, "## Interview transcript")
	require.Contains(t, out, "## Run trace")
	require.Contains(t, out, "## Final result")

	// Spec content rendered as YAML block.
	require.Contains(t, out, "synthetic test goal")
	require.Contains(t, out, "```yaml")

	// Run trace content from synthetic events.
	require.Contains(t, out, "### Iteration 1")
	require.Contains(t, out, "tool_call")
	require.Contains(t, out, "tool_result")
	require.Contains(t, out, "verify_result")

	// Terminal event recognised.
	require.Contains(t, out, "**Status**: done")
}

func TestExport_JSONIsValid(t *testing.T) {
	disableDaemon(t)
	writeFakeSession(t, "01TESTSESSION0002", 5)

	out, err := runExport(t, []string{
		"01TESTSESSION0002",
		"--format", "json",
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)

	// Output must parse as a single JSON object with the expected shape.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Equal(t, "session", parsed["_gil_export"])
	require.NotNil(t, parsed["metadata"])
	events, ok := parsed["events"].([]any)
	require.True(t, ok, "events must be an array")
	require.Len(t, events, 5)
}

func TestExport_JSONLHasHeaderPlusEvents(t *testing.T) {
	disableDaemon(t)
	writeFakeSession(t, "01TESTSESSION0003", 4)

	out, err := runExport(t, []string{
		"01TESTSESSION0003",
		"--format", "jsonl",
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 5, "expected 1 header line + 4 event lines")

	// Header line must carry the export sentinel.
	var header map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &header))
	require.Equal(t, "session", header["_gil_export"])
	require.Equal(t, "01TESTSESSION0003", header["session_id"])

	// Each remaining line must parse as a JSON event.
	for i, line := range lines[1:] {
		var ev map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &ev), "line %d not JSON: %s", i, line)
		require.NotZero(t, ev["id"], "event id must be set on line %d", i)
	}
}

func TestExport_LargeToolOutputIsTruncated(t *testing.T) {
	disableDaemon(t)
	root := t.TempDir()
	t.Setenv("GIL_HOME", root)
	layout, err := paths.FromEnv()
	require.NoError(t, err)
	sessionID := "01TESTSESSION0004"
	sessionDir := filepath.Join(layout.SessionsDir(), sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// Spec stub.
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "spec.yaml"),
		[]byte("goal:\n  one_liner: t\n"), 0o644))

	// One event with a very large output payload.
	large := strings.Repeat("X", truncateBytes*4)
	persister, err := event.NewPersister(filepath.Join(sessionDir, "events"))
	require.NoError(t, err)
	bigData, err := json.Marshal(map[string]string{"output": large})
	require.NoError(t, err)
	require.NoError(t, persister.Write(event.Event{
		ID: 1, Timestamp: time.Now(),
		Source: event.SourceEnvironment, Kind: event.KindObservation,
		Type: "tool_result", Data: bigData,
	}))
	require.NoError(t, persister.Sync())
	require.NoError(t, persister.Close())

	out, err := runExport(t, []string{
		sessionID,
		"--format", "markdown",
		"--socket", "/nonexistent/sock",
	})
	require.NoError(t, err)

	// Must contain truncation marker but not the full payload.
	require.Contains(t, out, "bytes truncated]")
	require.NotContains(t, out, strings.Repeat("X", truncateBytes*2))
}

func TestExport_OutputFileHas0644Perms(t *testing.T) {
	disableDaemon(t)
	writeFakeSession(t, "01TESTSESSION0005", 2)

	dst := filepath.Join(t.TempDir(), "out.md")
	_, err := runExport(t, []string{
		"01TESTSESSION0005",
		"--format", "markdown",
		"--socket", "/nonexistent/sock",
		"--output", dst,
	})
	require.NoError(t, err)

	info, err := os.Stat(dst)
	require.NoError(t, err)
	// On most filesystems the file mode is exactly 0644 (umask 022 won't
	// affect it because OpenFile passes the explicit mode). We still check
	// for the read bits being set in case a tighter umask is in effect.
	mode := info.Mode().Perm()
	require.Equal(t, os.FileMode(0o644), mode, "expected 0644, got %o", mode)
}

func TestExport_SessionNotFound(t *testing.T) {
	disableDaemon(t)
	// Point GIL_HOME at an empty dir so nothing exists for the session id.
	t.Setenv("GIL_HOME", t.TempDir())

	_, err := runExport(t, []string{
		"01NOSUCHSESSION00",
		"--format", "markdown",
		"--socket", "/nonexistent/sock",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestExport_InvalidFormat(t *testing.T) {
	disableDaemon(t)
	writeFakeSession(t, "01TESTSESSION0006", 1)

	_, err := runExport(t, []string{
		"01TESTSESSION0006",
		"--format", "xml",
		"--socket", "/nonexistent/sock",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --format")
}

func TestSplitLinesKeep(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\n", []string{"a"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\nb", []string{"a", "b"}},
		{"\n\na\n", []string{"a"}}, // empty lines are dropped
	}
	for _, tc := range cases {
		got := splitLinesKeep([]byte(tc.in))
		var gotS []string
		for _, b := range got {
			gotS = append(gotS, string(b))
		}
		require.Equal(t, tc.want, gotS, "input=%q", tc.in)
	}
}

func TestTruncateMarker(t *testing.T) {
	require.Equal(t, "abc", truncate("abc", 10))
	got := truncate("abcdefghij", 5)
	require.Contains(t, got, "abcde")
	require.Contains(t, got, "5 bytes truncated")
}

func TestDecodeHelpers(t *testing.T) {
	data := []byte(`{"name":"bash","passed":true,"iter":3,"input":{"cmd":"ls"}}`)
	require.Equal(t, "bash", decodeString(data, "name"))
	require.Equal(t, "", decodeString(data, "missing"))
	require.True(t, decodeBool(data, "passed"))
	require.False(t, decodeBool(data, "missing"))
	require.Equal(t, int64(3), decodeInt(data, "iter"))
	require.Equal(t, int64(0), decodeInt(data, "missing"))
	require.Equal(t, `{"cmd":"ls"}`, decodeRaw(data, "input"))
}
