package app

import (
	"encoding/json"
	"fmt"
	"strings"

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// ActivityFilter controls which events the activity pane surfaces.
type ActivityFilter int

const (
	// FilterMilestones is the default — shows iteration boundaries,
	// verify outcomes, checkpoints, stuck, compact, run-end. Per spec
	// §12.
	FilterMilestones ActivityFilter = iota
	// FilterAll shows every emitted event (firehose).
	FilterAll
)

// String returns "milestones" / "all" for footer/help text.
func (f ActivityFilter) String() string {
	if f == FilterAll {
		return "all"
	}
	return "milestones"
}

// milestoneTypes is the canonical milestone set from spec §12. Keep in
// sync with that table — both directions: adding a milestone here must
// also be reflected in the spec.
var milestoneTypes = map[string]bool{
	"iteration_start":     true,
	"verify_run":          true,
	"verify_result":       true,
	"checkpoint_committed": true,
	"checkpoint_init":     true,
	"stuck_detected":      true,
	"stuck_recovered":     true,
	"stuck_unrecovered":   true,
	"compact_start":       true,
	"compact_done":        true,
	"run_done":            true,
	"run_max_iterations":  true,
	"run_error":           true,
	"permission_ask":      true,
	"permission_denied":   true,
}

// ActivityRow is one rendered line in the activity pane. The view
// layer formats it as "▏ HH:MM iter N <verb> <summary>".
type ActivityRow struct {
	Timestamp string // "18:34"
	Iter      int32  // most recent iteration_start seen at or before this row
	Verb      string // "bash", "edit", "verify ✓", "checkpoint", ...
	Summary   string // truncated one-line detail
	IsLatest  bool   // true for the bottom-most row (gets spinner if mid-tool)
	Spinning  bool   // when true, replace verb with spinner+"thinking…"
}

// activityFromEvents transforms the buffered raw events into
// ActivityRow slices respecting the filter. The most recent N rows are
// returned (oldest first), where N is min(maxRows, total).
func activityFromEvents(events []*gilv1.Event, filter ActivityFilter, maxRows int) []ActivityRow {
	if maxRows <= 0 || len(events) == 0 {
		return nil
	}
	var iter int32
	rows := make([]ActivityRow, 0, len(events))
	pendingTool := false // tool_call without matching tool_result
	pendingProvider := false

	for _, ev := range events {
		typ := ev.GetType()
		if typ == "iteration_start" {
			iter = parseIterFromEvent(ev.GetDataJson())
		}
		// Track in-flight provider/tool for spinner heuristic (spec §12).
		switch typ {
		case "provider_request":
			pendingProvider = true
		case "provider_response":
			pendingProvider = false
		case "tool_call":
			pendingTool = true
		case "tool_result":
			pendingTool = false
		}
		if filter == FilterMilestones && !milestoneTypes[typ] {
			continue
		}
		ts := "--:--"
		if t := ev.GetTimestamp(); t != nil {
			ts = t.AsTime().Format("15:04")
		}
		verb, summary := summarizeEvent(ev)
		rows = append(rows, ActivityRow{
			Timestamp: ts,
			Iter:      iter,
			Verb:      verb,
			Summary:   summary,
		})
	}
	// Trim to last maxRows.
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	if len(rows) == 0 {
		return nil
	}
	rows[len(rows)-1].IsLatest = true
	rows[len(rows)-1].Spinning = pendingTool || pendingProvider
	return rows
}

// summarizeEvent maps an event type + data JSON to a (verb, summary)
// pair per spec §12.
//
// Verbs are intentionally short. tool_call → "bash" or "edit" or the
// raw tool name; verify_result → "verify ✓" / "verify ✗"; checkpoint
// → "checkpoint"; stuck_detected → "stuck"; etc.
func summarizeEvent(ev *gilv1.Event) (verb, summary string) {
	typ := ev.GetType()
	data := ev.GetDataJson()
	switch typ {
	case "iteration_start":
		return "iter", fmt.Sprintf("started")
	case "verify_run":
		return "verify", "run"
	case "verify_result":
		var d struct {
			Pass    bool   `json:"pass"`
			Passed  int    `json:"passed"`
			Total   int    `json:"total"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(data, &d)
		mark := Glyphs().Done
		v := "verify " + mark
		if !d.Pass {
			v = "verify " + Glyphs().Failed
		}
		s := d.Summary
		if s == "" && d.Total > 0 {
			s = fmt.Sprintf("%d / %d", d.Passed, d.Total)
		}
		return v, s
	case "checkpoint_committed":
		var d struct {
			Step int    `json:"step"`
			SHA  string `json:"sha"`
			Note string `json:"note"`
		}
		_ = json.Unmarshal(data, &d)
		s := d.Note
		if s == "" && d.SHA != "" {
			s = shortSHA(d.SHA)
		}
		return "checkpoint", s
	case "stuck_detected":
		var d struct {
			Pattern string `json:"pattern"`
			Detail  string `json:"detail"`
		}
		_ = json.Unmarshal(data, &d)
		s := d.Pattern
		if d.Detail != "" {
			s += " " + d.Detail
		}
		return "stuck", s
	case "stuck_recovered":
		var d struct {
			Strategy    string `json:"strategy"`
			Explanation string `json:"explanation"`
		}
		_ = json.Unmarshal(data, &d)
		s := d.Strategy
		if d.Explanation != "" {
			s = d.Strategy + " — " + d.Explanation
		}
		return "recover", s
	case "stuck_unrecovered":
		return "stuck ✗", "all strategies exhausted"
	case "compact_start":
		return "compact", "start"
	case "compact_done":
		return "compact", "done"
	case "run_done":
		return "done", ""
	case "run_max_iterations":
		return "limit", "max iterations"
	case "run_error":
		var d struct {
			Err string `json:"err"`
		}
		_ = json.Unmarshal(data, &d)
		return "error", d.Err
	case "permission_ask":
		var d struct {
			Tool string `json:"tool"`
			Key  string `json:"key"`
		}
		_ = json.Unmarshal(data, &d)
		return "ask", d.Tool + " " + d.Key
	case "permission_denied":
		return "deny", ""
	case "tool_call":
		var d struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(data, &d)
		v := d.Name
		if v == "" {
			v = "tool"
		}
		return v, summarizeToolInput(d.Name, d.Input)
	case "tool_result":
		var d struct {
			Name string `json:"name"`
			Err  string `json:"err"`
		}
		_ = json.Unmarshal(data, &d)
		if d.Err != "" {
			return d.Name + " ✗", d.Err
		}
		return d.Name, "ok"
	case "provider_request":
		return "llm", "request"
	case "provider_response":
		var d struct {
			Tokens int `json:"tokens"`
		}
		_ = json.Unmarshal(data, &d)
		if d.Tokens > 0 {
			return "llm", fmt.Sprintf("response (%d tok)", d.Tokens)
		}
		return "llm", "response"
	}
	return typ, ""
}

// summarizeToolInput pulls one short field out of a tool's JSON input.
// For bash → command; for edit → file_path; otherwise returns first
// 40 chars of the marshalled JSON.
func summarizeToolInput(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ""
	}
	switch name {
	case "bash":
		if cmd, ok := generic["command"].(string); ok {
			return fmt.Sprintf("%q", oneLine(cmd, 40))
		}
	case "edit", "write", "read":
		if p, ok := generic["file_path"].(string); ok {
			return p
		}
		if p, ok := generic["path"].(string); ok {
			return p
		}
	}
	// Fallback: first key=value.
	for k, v := range generic {
		return fmt.Sprintf("%s=%v", k, oneLine(fmt.Sprint(v), 30))
	}
	return ""
}

// oneLine collapses any whitespace into single spaces and truncates to n.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n-1] + Glyphs().Ellipsis
	}
	return s
}

// shortSHA returns the first 7 chars of a hex SHA, or the input if shorter.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// parseIterFromEvent pulls "iter" from an iteration_start event payload.
func parseIterFromEvent(raw []byte) int32 {
	var d struct {
		Iter int32 `json:"iter"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Iter
}

// renderActivityPane renders the activity content (without border) at
// the given content width. height is the available row count; rows
// beyond it are dropped (oldest first). spinFrame is the current
// spinner frame index used for the latest in-flight row.
func renderActivityPane(width, height int, rows []ActivityRow, spinFrame int) string {
	g := Glyphs()
	if len(rows) == 0 {
		return styleDim("(no activity yet)")
	}
	if height > 0 && len(rows) > height {
		rows = rows[len(rows)-height:]
	}
	var sb strings.Builder
	for i, r := range rows {
		left := styleDim(g.QuoteBar)
		ts := styleMeta(r.Timestamp)
		iterLbl := styleDim(fmt.Sprintf("iter %-3d", r.Iter))
		var verbStr string
		if r.IsLatest && r.Spinning {
			frame := g.Spinner[spinFrame%len(g.Spinner)]
			verbStr = styleEmphasis(frame) + " " + styleSurface("thinking"+g.Ellipsis)
		} else {
			verbStr = styleSurface(r.Verb)
		}
		summary := truncate(r.Summary, max(width-30, 10))
		line := fmt.Sprintf("%s %s  %s  %-12s %s",
			left, ts, iterLbl, verbStr, styleSurface(summary))
		sb.WriteString(line)
		if i < len(rows)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
