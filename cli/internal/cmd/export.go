// Package cmd — export.go implements `gil export` for session sharing.
//
// Why this exists
//
// When an autonomous run completes (or fails), the user needs a single-file
// representation of what happened: spec + interview transcript + run trace +
// final result. This drives bug reports, peer review, archival, regression
// replay (via gil import), and for-the-record sharing of agent decisions.
//
// Three formats are supported:
//
//   - markdown — human-readable single-file dump. Default.
//   - json     — typed snapshot {metadata, spec, events:[...]}.
//   - jsonl    — raw event stream with one metadata header line. Lossless.
//
// Data sources
//
// The exporter prefers reading directly from disk (sessions/<id>/events/...,
// sessions/<id>/spec.yaml) so that `gil export` can run when the daemon is
// stopped. Session metadata (status, working_dir, ...) lives in the SQLite
// session DB which is owned by gild, so we contact gild via the existing
// SDK pattern (auto-spawn through ensureDaemon) for that piece. If the
// daemon happens to be unreachable but the on-disk session directory exists,
// we still emit a best-effort export with metadata zeroed — better than
// refusing to share an archived session.
//
// Secret hygiene is enforced at write time by core/event/persist.go (every
// event is masked through MaskSecrets before landing in events.jsonl), so
// the exporter does not double-mask. The spec YAML is loaded straight from
// disk; secrets do not normally appear there but we still pipe its rendered
// text through MaskSecrets in markdown mode for defence in depth.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/core/specstore"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

// truncateBytes is the threshold past which embedded tool/result blobs are
// truncated in markdown mode. The cutoff is intentionally conservative — a
// full bash output of a `find /` would otherwise dominate the report.
// JSON / JSONL formats are not truncated since they are meant for machine
// consumption (and downstream pipelines can grep / sort the full payload).
const truncateBytes = 2048

// exportFormat is the rendered output format selected via --format.
type exportFormat string

const (
	formatMarkdown exportFormat = "markdown"
	formatJSON     exportFormat = "json"
	formatJSONL    exportFormat = "jsonl"
)

// exportCmd returns the `gil export <session-id>` command.
func exportCmd() *cobra.Command {
	var (
		socket   string
		format   string
		output   string
		layout   = defaultLayout()
	)
	c := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Dump a session as markdown / json / jsonl for sharing",
		Long: `Export a single session to a self-contained file.

The default markdown format is human-readable: metadata header, frozen spec,
interview transcript, run trace, and final result. The json format is a
typed snapshot. The jsonl format is the raw event stream (one event per
line) prefixed with a single metadata header line.

By default the rendered output goes to stdout; pass --output <file> to write
it to disk (mode 0644). Secrets present in event payloads are already masked
when events.jsonl is written, so exported files are safe to share but you
should still review them before posting publicly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]

			fmt := exportFormat(strings.ToLower(format))
			switch fmt {
			case formatMarkdown, formatJSON, formatJSONL:
			default:
				return cliutil.New(
					"invalid --format value: "+format,
					`use one of: markdown, json, jsonl`)
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			meta, sess, err := loadSessionMeta(ctx, sessionID, socket, layout)
			if err != nil {
				return err
			}

			out, closeFn, err := openExportOutput(output, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			defer closeFn()

			switch fmt {
			case formatJSONL:
				return renderJSONL(out, meta, sess)
			case formatJSON:
				return renderJSON(out, meta, sess)
			default:
				return renderMarkdown(out, meta, sess)
			}
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&format, "format", "markdown", "output format: markdown | json | jsonl")
	c.Flags().StringVarP(&output, "output", "o", "-", `output file path (or "-" for stdout)`)
	return c
}

// sessionMeta is the minimal session header used by every export format.
// It is populated from a mix of (a) the gild session row when reachable
// and (b) the on-disk spec.yaml for the spec block.
type sessionMeta struct {
	ID         string
	Status     string
	WorkingDir string
	GoalHint   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Provider   string
	Model      string
	Tokens     int64
	CostUSD    float64
	Iterations int32
	SessionDir string
	Spec       *gilv1.FrozenSpec // may be nil if no spec.yaml on disk
	SpecYAML   []byte            // raw bytes of spec.yaml (or nil)
}

// sessionData bundles the events read from disk so each renderer can iterate
// without re-doing the I/O.
type sessionData struct {
	Events   []event.Event
	EventsRaw [][]byte // for jsonl mode: untouched lines (preserves field order)
}

// loadSessionMeta resolves session metadata, talking to the daemon for the
// SQLite-backed row when reachable and reading spec.yaml from disk regardless.
// If the daemon is down but the session directory exists we still return a
// best-effort meta value so that archival sessions remain exportable.
func loadSessionMeta(ctx context.Context, sessionID, socket string, layout paths.Layout) (sessionMeta, sessionData, error) {
	sessionDir := filepath.Join(layout.SessionsDir(), sessionID)

	// Spec is always loaded from disk. ErrNotFound is OK — pre-freeze sessions
	// have no spec yet but we still let the user export the events.
	store := specstore.NewStore(sessionDir)
	var fs *gilv1.FrozenSpec
	specYAML, _ := os.ReadFile(filepath.Join(sessionDir, "spec.yaml"))
	if loaded, err := store.Load(); err == nil {
		fs = loaded
	} else if !errors.Is(err, specstore.ErrNotFound) {
		// Tampered / unparseable spec — surface as a warning via stderr but
		// keep going: the events log is still exportable.
		fmt.Fprintf(os.Stderr, "warning: spec.yaml present but failed to load: %v\n", err)
	}

	meta := sessionMeta{
		ID:         sessionID,
		SessionDir: sessionDir,
		Spec:       fs,
		SpecYAML:   specYAML,
	}

	if fs != nil {
		if m := fs.GetModels().GetMain(); m != nil {
			meta.Provider = m.GetProvider()
			meta.Model = m.GetModelId()
		}
	}

	// Try the daemon for the live session row. Failure is non-fatal — the
	// directory itself is enough to produce a usable export.
	rowOK := false
	if err := ensureDaemon(socket, defaultBase()); err == nil {
		if cli, derr := sdk.Dial(socket); derr == nil {
			defer cli.Close()
			if s, gerr := cli.GetSession(ctx, sessionID); gerr == nil && s != nil {
				meta.Status = s.Status
				meta.WorkingDir = s.WorkingDir
				meta.GoalHint = s.GoalHint
				meta.Iterations = s.CurrentIteration
				meta.Tokens = s.CurrentTokens
				rowOK = true
			}
		}
	}

	// If the daemon path failed but the session dir is also missing, we have
	// nothing to export — surface a clear error.
	if !rowOK {
		if _, err := os.Stat(sessionDir); err != nil {
			return sessionMeta{}, sessionData{}, cliutil.New(
				fmt.Sprintf("session %q not found", sessionID),
				`check the session id with "gil status", or pass GIL_HOME to point at a different layout`)
		}
	}

	// Load events from disk; LoadAll handles missing-file → error so we treat
	// a missing events.jsonl as "no events yet" rather than a hard failure.
	eventsPath := filepath.Join(sessionDir, "events", "events.jsonl")
	data := sessionData{}
	if _, err := os.Stat(eventsPath); err == nil {
		evs, err := event.LoadAll(eventsPath)
		if err != nil {
			return sessionMeta{}, sessionData{}, fmt.Errorf("load events: %w", err)
		}
		data.Events = evs
		// Also read raw lines so that --format jsonl preserves bit-exact bytes.
		raw, err := os.ReadFile(eventsPath)
		if err == nil {
			for _, line := range splitLinesKeep(raw) {
				data.EventsRaw = append(data.EventsRaw, line)
			}
		}
	}

	return meta, data, nil
}

// splitLinesKeep splits b on '\n' and discards empty trailing entries.
// The newline is NOT included in the returned slices.
func splitLinesKeep(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// openExportOutput resolves the --output flag to a writer. When path is "" or
// "-" the cobra-supplied stdout is used. Otherwise a 0644 file is opened (or
// truncated). The returned closeFn is always non-nil and safe to defer.
func openExportOutput(path string, stdout io.Writer) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return stdout, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, func() {}, cliutil.Wrap(err,
			"could not open output file "+path,
			`check the directory exists and is writable, or omit --output to print to stdout`)
	}
	return f, func() { _ = f.Close() }, nil
}

// renderJSONL writes a single metadata header line followed by every raw
// event line as captured on disk. This preserves bit-exact bytes (no
// re-marshalling) which makes the format suitable for round-trip via
// `gil import`.
func renderJSONL(out io.Writer, meta sessionMeta, data sessionData) error {
	header := jsonlHeader(meta)
	hb, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if _, err := out.Write(hb); err != nil {
		return err
	}
	if _, err := out.Write([]byte{'\n'}); err != nil {
		return err
	}
	for _, line := range data.EventsRaw {
		if _, err := out.Write(line); err != nil {
			return err
		}
		if _, err := out.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

// jsonlHeader is the single header object that prefixes a jsonl export. The
// "_gil_export" sentinel field lets `gil import` distinguish a header from
// an event line without parsing every field.
type jsonlMetadata struct {
	GilExport  string  `json:"_gil_export"`
	Version    int     `json:"version"`
	SessionID  string  `json:"session_id"`
	Status     string  `json:"status,omitempty"`
	WorkingDir string  `json:"working_dir,omitempty"`
	GoalHint   string  `json:"goal_hint,omitempty"`
	Provider   string  `json:"provider,omitempty"`
	Model      string  `json:"model,omitempty"`
	Tokens     int64   `json:"tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Iterations int32   `json:"iterations,omitempty"`
	SpecYAML   string  `json:"spec_yaml,omitempty"`
}

func jsonlHeader(meta sessionMeta) jsonlMetadata {
	return jsonlMetadata{
		GilExport:  "session",
		Version:    1,
		SessionID:  meta.ID,
		Status:     meta.Status,
		WorkingDir: meta.WorkingDir,
		GoalHint:   meta.GoalHint,
		Provider:   meta.Provider,
		Model:      meta.Model,
		Tokens:     meta.Tokens,
		CostUSD:    meta.CostUSD,
		Iterations: meta.Iterations,
		SpecYAML:   string(meta.SpecYAML),
	}
}

// renderJSON writes a single JSON object containing metadata, the spec
// (already YAML, kept as a string field for verbatim re-render), and every
// event in structured form. We use the disk-loaded event.Event values so
// the consumer gets typed fields rather than embedded JSON strings.
func renderJSON(out io.Writer, meta sessionMeta, data sessionData) error {
	type jsonEvent struct {
		ID        int64           `json:"id"`
		Timestamp string          `json:"timestamp"`
		Source    string          `json:"source"`
		Kind      string          `json:"kind"`
		Type      string          `json:"type"`
		Data      json.RawMessage `json:"data,omitempty"`
		Cause     int64           `json:"cause,omitempty"`
		Tokens    int64           `json:"tokens,omitempty"`
		CostUSD   float64         `json:"cost_usd,omitempty"`
		LatencyMs int64           `json:"latency_ms,omitempty"`
	}
	events := make([]jsonEvent, 0, len(data.Events))
	for _, e := range data.Events {
		var raw json.RawMessage
		if len(e.Data) > 0 {
			// Preserve as JSON if parseable; otherwise wrap as string so the
			// payload is never lost.
			if json.Valid(e.Data) {
				raw = json.RawMessage(e.Data)
			} else {
				wrapped, _ := json.Marshal(string(e.Data))
				raw = wrapped
			}
		}
		events = append(events, jsonEvent{
			ID:        e.ID,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			Source:    sourceLabel(e.Source),
			Kind:      kindLabel(e.Kind),
			Type:      e.Type,
			Data:      raw,
			Cause:     e.Cause,
			Tokens:    e.Metrics.Tokens,
			CostUSD:   e.Metrics.CostUSD,
			LatencyMs: e.Metrics.LatencyMs,
		})
	}
	doc := struct {
		GilExport  string         `json:"_gil_export"`
		Version    int            `json:"version"`
		Metadata   jsonlMetadata  `json:"metadata"`
		Events     []jsonEvent    `json:"events"`
	}{
		GilExport: "session",
		Version:   1,
		Metadata:  jsonlHeader(meta),
		Events:    events,
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(&doc)
}

// renderMarkdown is the human-facing format. Sections in order:
//
//  1. Header (id, working dir, status, model, totals)
//  2. Spec (collapsed YAML)
//  3. Interview transcript (best-effort: stitched from interview events
//     when present, otherwise a stub)
//  4. Run trace (iteration-grouped tool calls, results, agent text)
//  5. Final result (from run_done / verify_result events)
//
// Tool inputs/outputs over truncateBytes are clipped with a marker so that
// a runaway grep result doesn't bury the rest of the trace.
func renderMarkdown(out io.Writer, meta sessionMeta, data sessionData) error {
	var b strings.Builder

	// --- Header ----------------------------------------------------------
	fmt.Fprintf(&b, "# gil session %s\n\n", meta.ID)
	if meta.WorkingDir != "" {
		fmt.Fprintf(&b, "**Working dir**: %s\n", meta.WorkingDir)
	}
	if meta.Status != "" {
		fmt.Fprintf(&b, "**Status**: %s\n", meta.Status)
	}
	if meta.Provider != "" {
		fmt.Fprintf(&b, "**Provider**: %s\n", meta.Provider)
	}
	if meta.Model != "" {
		fmt.Fprintf(&b, "**Model**: %s\n", meta.Model)
	}
	if meta.Iterations != 0 {
		fmt.Fprintf(&b, "**Iterations**: %d\n", meta.Iterations)
	}
	if meta.Tokens != 0 {
		fmt.Fprintf(&b, "**Tokens**: %d\n", meta.Tokens)
	}
	if meta.CostUSD != 0 {
		fmt.Fprintf(&b, "**Cost**: $%.4f (estimated)\n", meta.CostUSD)
	}
	if meta.GoalHint != "" {
		fmt.Fprintf(&b, "**Goal hint**: %s\n", meta.GoalHint)
	}
	b.WriteString("\n")

	// --- Spec ------------------------------------------------------------
	b.WriteString("## Spec\n\n")
	if len(meta.SpecYAML) > 0 {
		b.WriteString("```yaml\n")
		// Mask defensively even though spec normally has no secrets.
		b.WriteString(event.MaskSecrets(string(meta.SpecYAML)))
		if !strings.HasSuffix(string(meta.SpecYAML), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	} else {
		b.WriteString("_No spec.yaml present (interview not yet completed)._\n\n")
	}

	// --- Interview transcript -------------------------------------------
	b.WriteString("## Interview transcript\n\n")
	wrote := renderInterview(&b, data.Events)
	if !wrote {
		b.WriteString("_No interview events recorded for this session._\n\n")
	}

	// --- Run trace -------------------------------------------------------
	b.WriteString("## Run trace\n\n")
	wrote = renderRunTrace(&b, data.Events)
	if !wrote {
		b.WriteString("_No run events recorded yet._\n\n")
	}

	// --- Final result ----------------------------------------------------
	b.WriteString("## Final result\n\n")
	renderFinalResult(&b, data.Events, meta)

	_, err := io.WriteString(out, b.String())
	return err
}

// renderInterview pulls interview Q&A from the event stream. Today the gild
// interview service does NOT persist Q&A to events.jsonl (it streams over
// gRPC), so we emit a stub when nothing matches. This keeps the section
// header stable so future enhancements can fill it in without changing the
// markdown shape.
func renderInterview(b *strings.Builder, events []event.Event) bool {
	wrote := false
	qN := 0
	for _, e := range events {
		switch e.Type {
		case "interview_question":
			qN++
			fmt.Fprintf(b, "**Q%d**: %s\n\n", qN, truncate(decodeString(e.Data, "content"), truncateBytes))
			wrote = true
		case "interview_answer":
			fmt.Fprintf(b, "**A%d**: %s\n\n", qN, truncate(decodeString(e.Data, "content"), truncateBytes))
			wrote = true
		}
	}
	return wrote
}

// renderRunTrace groups events by iteration_start. Each iteration shows the
// agent's tool calls, results, and any verifier output. We intentionally
// elide low-value events (heartbeats, ssh_sync_*) — the goal is a readable
// transcript, not a complete event log (use --format jsonl for that).
func renderRunTrace(b *strings.Builder, events []event.Event) bool {
	wrote := false
	iter := 0
	headerOpen := false
	openHeader := func(i int) {
		fmt.Fprintf(b, "### Iteration %d\n\n", i)
		headerOpen = true
	}
	for _, e := range events {
		switch e.Type {
		case "iteration_start":
			iter++
			openHeader(iter)
			wrote = true
		case "provider_response":
			if !headerOpen {
				openHeader(iter + 1)
				iter++
			}
			text := decodeString(e.Data, "text")
			if text != "" {
				fmt.Fprintf(b, "**LLM**: %s\n\n", truncate(text, truncateBytes))
				wrote = true
			}
		case "tool_call":
			if !headerOpen {
				openHeader(iter + 1)
				iter++
			}
			name := decodeString(e.Data, "name")
			input := decodeRaw(e.Data, "input")
			fmt.Fprintf(b, "**tool_call** `%s`: `%s`\n\n", name, truncate(input, truncateBytes))
			wrote = true
		case "tool_result":
			if !headerOpen {
				openHeader(iter + 1)
				iter++
			}
			result := decodeString(e.Data, "output")
			if result == "" {
				result = decodeString(e.Data, "result")
			}
			if result == "" {
				result = string(e.Data)
			}
			fmt.Fprintf(b, "**tool_result**:\n```\n%s\n```\n\n", truncate(result, truncateBytes))
			wrote = true
		case "verify_run":
			fmt.Fprintf(b, "**verify**: running checks…\n\n")
			wrote = true
		case "verify_result":
			passed := decodeBool(e.Data, "passed")
			fmt.Fprintf(b, "**verify_result**: passed=%v %s\n\n", passed, truncate(string(e.Data), truncateBytes))
			wrote = true
		}
	}
	return wrote
}

// renderFinalResult finds the terminal event (run_done / run_max_iterations
// / run_error) and writes a one-paragraph summary. Falls back to status
// metadata when no terminal event is present (run still in flight or never
// started).
func renderFinalResult(b *strings.Builder, events []event.Event, meta sessionMeta) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		switch e.Type {
		case "run_done":
			fmt.Fprintf(b, "**Status**: done\n\n")
			fmt.Fprintf(b, "Iterations: %d, tokens: %d\n\n",
				decodeInt(e.Data, "iterations"), decodeInt(e.Data, "tokens"))
			return
		case "run_max_iterations":
			fmt.Fprintf(b, "**Status**: stopped (max iterations)\n\n")
			fmt.Fprintf(b, "%s\n\n", truncate(string(e.Data), truncateBytes))
			return
		case "run_error":
			fmt.Fprintf(b, "**Status**: error\n\n")
			fmt.Fprintf(b, "%s\n\n", truncate(decodeString(e.Data, "err"), truncateBytes))
			return
		case "stuck_unrecovered":
			fmt.Fprintf(b, "**Status**: stuck (unrecovered)\n\n")
			fmt.Fprintf(b, "%s\n\n", truncate(string(e.Data), truncateBytes))
			return
		}
	}
	if meta.Status != "" {
		fmt.Fprintf(b, "**Status**: %s (no terminal event recorded)\n\n", meta.Status)
	} else {
		b.WriteString("_No terminal event recorded._\n\n")
	}
}

// truncate clips s to max bytes and appends a marker reporting how many
// bytes were dropped. We measure in bytes rather than runes so the marker
// reflects what an operator would see in `wc -c`.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	dropped := len(s) - max
	return s[:max] + fmt.Sprintf("... [%d bytes truncated]", dropped)
}

// decodeString pulls a string field out of a JSON-encoded event Data blob.
// Returns "" when the data is not JSON, the field is absent, or the field
// is not a string.
func decodeString(data []byte, field string) string {
	if len(data) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}

// decodeRaw is like decodeString but JSON-encodes the field's value (so
// nested objects render as literal JSON in the markdown trace).
func decodeRaw(data []byte, field string) string {
	if len(data) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if v, ok := m[field]; ok {
		return string(v)
	}
	return ""
}

// decodeBool pulls a bool field out of a JSON-encoded event Data blob.
func decodeBool(data []byte, field string) bool {
	if len(data) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	if v, ok := m[field].(bool); ok {
		return v
	}
	return false
}

// decodeInt pulls an int64 field out of a JSON-encoded event Data blob.
// Handles both float (JSON numbers default to float64) and int.
func decodeInt(data []byte, field string) int64 {
	if len(data) == 0 {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return 0
	}
	switch v := m[field].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

// sourceLabel converts an event.Source enum into a stable, human-readable
// string. We mirror the proto enum spelling so tooling can compare across
// the gRPC stream output and the exported JSON.
func sourceLabel(s event.Source) string {
	switch s {
	case event.SourceAgent:
		return "AGENT"
	case event.SourceUser:
		return "USER"
	case event.SourceEnvironment:
		return "ENVIRONMENT"
	case event.SourceSystem:
		return "SYSTEM"
	default:
		return "UNSPECIFIED"
	}
}

// kindLabel converts an event.Kind enum to its human-readable form.
func kindLabel(k event.Kind) string {
	switch k {
	case event.KindAction:
		return "ACTION"
	case event.KindObservation:
		return "OBSERVATION"
	case event.KindNote:
		return "NOTE"
	default:
		return "UNSPECIFIED"
	}
}
