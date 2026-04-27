package cmd

import (
	"strings"

	"github.com/mindungil/gil/core/cliutil"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

// eventFilter decides whether a given event should pass through the
// `gil events --filter ...` pipeline. The standard sets are documented
// in the user-facing help string and in the Phase 14 plan:
//
//   all         — pass everything (default)
//   milestones  — iteration boundaries, verify, checkpoint, stuck, run completion
//   errors      — anything matching *_error / *_failed / stuck_detected
//   tools       — tool_call / tool_result / tool_step / tool_*
//   agent       — provider_request / provider_response / compact_*
//
// A user may pass --filter milestones,errors and the union is taken.
// We resolve the comma-split list once at command setup time so the
// per-event hot path is just a switch over a precomputed bitmask.
type eventFilter struct {
	all        bool
	milestones bool
	errors     bool
	tools      bool
	agent      bool
}

func newEventFilter(specs []string) (eventFilter, error) {
	f := eventFilter{}
	if len(specs) == 0 {
		f.all = true
		return f, nil
	}
	for _, raw := range specs {
		// "milestones,errors" → ["milestones", "errors"]
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(strings.ToLower(part))
			switch part {
			case "":
				continue
			case "all":
				f.all = true
			case "milestones", "milestone":
				f.milestones = true
			case "errors", "error":
				f.errors = true
			case "tools", "tool":
				f.tools = true
			case "agent":
				f.agent = true
			default:
				return eventFilter{}, cliutil.New(
					"unknown --filter set: "+part,
					`valid sets: all, milestones, errors, tools, agent`)
			}
		}
	}
	if !f.all && !f.milestones && !f.errors && !f.tools && !f.agent {
		f.all = true
	}
	return f, nil
}

// matches reports whether evt should be emitted under the active
// filter. The matching is deliberately string-based because the
// proto's "type" field is the canonical event vocabulary — adding a
// new event type does not require a protobuf change, so we cannot key
// on an enum here.
func (f eventFilter) matches(evt *gilv1.Event) bool {
	if f.all {
		return true
	}
	t := evt.GetType()
	if f.milestones && isMilestoneEvent(t) {
		return true
	}
	if f.errors && isErrorEvent(t) {
		return true
	}
	if f.tools && isToolEvent(t) {
		return true
	}
	if f.agent && isAgentEvent(t) {
		return true
	}
	return false
}

// isMilestoneEvent — the events the no-arg / watch / TUI surfaces
// promote to first-class progress signals. Names mirror the strings
// emitted by core/runner/runner.go (iteration_start, run_done, etc).
func isMilestoneEvent(t string) bool {
	switch t {
	case "iteration_start", "iteration_end",
		"verify_run", "verify_result",
		"checkpoint_init", "checkpoint_done", "checkpoint_recorded",
		"stuck_detected", "stuck_recovery_done", "stuck_recovered", "stuck_unrecovered",
		"run_done", "run_completed":
		return true
	}
	return false
}

// isErrorEvent matches anything that looks like a failure — *_error,
// *_failed, plus the explicit stuck_detected name (which is a warning
// rather than a syntactic error but is what the user wants to see).
func isErrorEvent(t string) bool {
	if strings.HasSuffix(t, "_error") || strings.HasSuffix(t, "_failed") {
		return true
	}
	switch t {
	case "stuck_detected", "stuck_unrecovered", "run_error":
		return true
	}
	return false
}

// isToolEvent — anything in the tool_* family. We do not match on a
// single suffix because the proto has both tool_call (action) and
// tool_result (observation), and we want both.
func isToolEvent(t string) bool {
	return strings.HasPrefix(t, "tool_")
}

// isAgentEvent — the LLM round-trip plus compaction events.
func isAgentEvent(t string) bool {
	switch {
	case t == "provider_request", t == "provider_response", t == "anthropic_request":
		return true
	case strings.HasPrefix(t, "compact_"):
		return true
	}
	return false
}
