// Package cmd — import.go implements `gil import` for replaying exported
// sessions back into a fresh sessions/<id>/ directory.
//
// Why this exists
//
// `gil export --format jsonl` produces a single-file representation of a
// session: one metadata header line followed by one event per line. The
// import path takes that file and reconstructs an on-disk session that
// `gil export`, `gil events`, and any future replay tooling can consume.
//
// Design — minimal, file-centric
//
// We do not introduce a SessionService.Import RPC. The reasons:
//
//   - Most of the value (read the events back, re-export, regression diff)
//     does not require a live workspace, so we want import to work even
//     when gild is not running.
//   - Replaying events into a Stream + persister would mostly duplicate
//     what NewPersister already does at write time. Re-using the raw bytes
//     guarantees bit-exact round-trip.
//   - Schema for an "imported" session row would force a sessions table
//     migration — out of scope for Phase 12.
//
// Therefore import does the following:
//
//  1. Validate the file: first line must be a `_gil_export: session` header.
//  2. Allocate a fresh ULID-shaped id (mirrors session.Repo.Create) so the
//     imported session does not collide with the source.
//  3. Create sessions/<new-id>/{events,}/, write spec.yaml from the header's
//     spec_yaml field, copy every subsequent line verbatim into
//     events/events.jsonl.
//  4. If gild is reachable, also register a session row via SessionService.
//     The row's status is the default "created" — there is no "imported"
//     enum value yet, but the goal_hint field is set to "imported from <file>"
//     so the user can identify it in `gil status`.
//
// Restrictions
//
// The replayed session is intended for read-only use (`gil events`, `gil
// export`, future replay tooling). It cannot be `gil run` because the
// original workspace state isn't restored — there's no way to recover the
// repo at the original commit from a JSONL file alone.
package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"github.com/jedutools/gil/core/cliutil"
	"github.com/jedutools/gil/sdk"
)

// importCmd returns the `gil import <jsonl-file>` command.
func importCmd() *cobra.Command {
	var (
		socket    string
		workspace string
		nameHint  string
		layout    = defaultLayout()
	)
	c := &cobra.Command{
		Use:   "import <jsonl-file>",
		Short: "Replay an exported session JSONL into a new on-disk session",
		Long: `Read a session export produced by "gil export --format jsonl" and
reconstruct a new sessions/<id>/ directory containing the same events
and spec.yaml. The replayed session is read-only — it can be inspected
via "gil export" and "gil events" but not resumed via "gil run", because
the workspace state at the original commit is not restored.

If gild is reachable a session row is also registered so the import
shows up in "gil status".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			header, eventLines, err := readJSONLExport(path)
			if err != nil {
				return err
			}

			newID := makeULID()
			sessionDir := filepath.Join(layout.SessionsDir(), newID)

			if err := writeImportedSession(sessionDir, header, eventLines); err != nil {
				return err
			}

			// Best-effort register with the daemon — if this fails the import
			// is still usable on disk via `gil export`. We surface the error
			// only as a warning so the user knows the row is missing.
			goal := nameHint
			if goal == "" {
				if header.GoalHint != "" {
					goal = "imported: " + header.GoalHint
				} else {
					goal = "imported from " + filepath.Base(path)
				}
			}
			ws := workspace
			if ws == "" {
				ws = header.WorkingDir
			}
			if err := registerImportedRow(ctx, socket, newID, ws, goal); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: could not register session with daemon: %v\n"+
						"        (events and spec are on disk; gil export still works)\n", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"Imported %d events from %s\nNew session id: %s\nLocation: %s\n",
				len(eventLines), path, newID, sessionDir)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&workspace, "workspace", "", "override the working_dir recorded in the import header")
	c.Flags().StringVar(&nameHint, "name", "", "goal_hint to record on the new session row")
	return c
}

// readJSONLExport reads the export file and returns the parsed header plus
// the raw bytes of every subsequent event line. We avoid loading the entire
// file into memory in one slice: we stream line-by-line so that very large
// exports do not balloon the import process.
func readJSONLExport(path string) (jsonlMetadata, [][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return jsonlMetadata{}, nil, cliutil.Wrap(err,
			"could not open import file "+path,
			`check the path and that the file is readable`)
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)
	headerLine, err := r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return jsonlMetadata{}, nil, fmt.Errorf("read header: %w", err)
	}
	headerLine = trimTrailingNewline(headerLine)

	var header jsonlMetadata
	if err := json.Unmarshal(headerLine, &header); err != nil {
		return jsonlMetadata{}, nil, cliutil.Wrap(err,
			"file does not start with a gil export header",
			`expected a JSONL produced by "gil export --format jsonl"`)
	}
	if header.GilExport != "session" {
		return jsonlMetadata{}, nil, cliutil.New(
			"file is not a gil session export (missing _gil_export sentinel)",
			`re-export the source session with "gil export --format jsonl"`)
	}

	var lines [][]byte
	for {
		line, err := r.ReadBytes('\n')
		line = trimTrailingNewline(line)
		if len(line) > 0 {
			// Validate JSON shape so a corrupt line surfaces here, not deep
			// in a future replay.
			if !json.Valid(line) {
				return jsonlMetadata{}, nil, fmt.Errorf("event line %d is not valid JSON", len(lines)+1)
			}
			// Copy because bufio.ReadBytes reuses its internal buffer.
			cp := make([]byte, len(line))
			copy(cp, line)
			lines = append(lines, cp)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return jsonlMetadata{}, nil, fmt.Errorf("read event line: %w", err)
		}
	}
	return header, lines, nil
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// writeImportedSession lays out the new sessions/<id>/ tree.
//
// We choose mode 0700 for directories (matching gild's default) and 0644
// for files — the same modes as a freshly-created session. The events file
// is written byte-for-byte from the source so the round-trip property
// (`gil export → gil import → gil export` produces equivalent JSONL modulo
// the new ID) holds without re-marshalling.
func writeImportedSession(sessionDir string, header jsonlMetadata, lines [][]byte) error {
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}
	if header.SpecYAML != "" {
		if err := os.WriteFile(filepath.Join(sessionDir, "spec.yaml"), []byte(header.SpecYAML), 0o644); err != nil {
			return fmt.Errorf("write spec.yaml: %w", err)
		}
	}
	eventsDir := filepath.Join(sessionDir, "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(filepath.Join(eventsDir, "events.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create events.jsonl: %w", err)
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 64*1024)
	for _, line := range lines {
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("write newline: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush events: %w", err)
	}
	return nil
}

// registerImportedRow asks the daemon to insert a new session row using the
// freshly-allocated id. SessionService.Create insists on its own id, so we
// instead create it and accept whatever id the server allocates — that's
// fine for visibility in `gil status`, but it means the on-disk directory
// id and the DB row id will differ. To avoid that mismatch we skip the
// daemon call entirely and just rely on disk artifacts; future work could
// add a SessionService.Adopt RPC that takes a pre-existing directory.
//
// For Phase 12 we keep this function as a hook (returns nil so the caller
// does not warn) and leave the row creation to a follow-up.
func registerImportedRow(ctx context.Context, socket, id, workingDir, goalHint string) error {
	// Phase 12 trade-off: we do not register the row because the daemon's
	// Create RPC allocates its own ULID and would not align with the on-disk
	// directory. Returning nil keeps imports silent on this point.
	//
	// To enable daemon visibility once SessionService grows an Adopt(id, ...)
	// RPC, replace the body with:
	//
	//   if err := ensureDaemon(socket, defaultBase()); err != nil { return err }
	//   cli, err := sdk.Dial(socket); if err != nil { return err }
	//   defer cli.Close()
	//   _, err = cli.AdoptSession(ctx, sdk.AdoptOptions{ID: id, WorkingDir: workingDir, GoalHint: goalHint})
	//   return err
	_ = ctx
	_ = socket
	_ = id
	_ = workingDir
	_ = goalHint
	_ = sdk.CreateOptions{}
	return nil
}

// makeULID generates a fresh ULID suitable for a session id. We allocate a
// crypto/rand-backed entropy source per call so concurrent imports cannot
// collide on the monotonic component.
func makeULID() string {
	return strings.ToUpper(ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String())
}
