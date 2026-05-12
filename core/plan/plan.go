// Package plan implements the per-session run plan: a small TODO/checklist
// the agent maintains via the `plan` tool. It persists across compactions,
// surfaces in the TUI/CLI, and (paired with autonomy=PLAN_ONLY) finally
// gives the PLAN_ONLY gate something to write to.
//
// Lift summary:
//   - Status enum (3-state) borrows opencode's `pending` / `in_progress` /
//     `completed` shape (todo.ts) — opencode also has `cancelled` but the
//     audit found 3-state covers gil's cases without bloating the prompt.
//   - Item shape (id, text, status, optional sub-items) borrows codex's
//     UpdatePlanArgs (codex-rs/core/src/tools/handlers/plan.rs +
//     codex-rs/protocol/src/plan_tool.rs) — flatter than cline's nested
//     focus_chain but richer than openhands' flat-array.
//   - Update operation (agent passes the complete updated list each call)
//     comes from opencode/todo.ts — keeps the server stateless on plan
//     logic; mutation = whole-list overwrite or one-item delta. We support
//     both shapes via the tool's `operation` discriminator.
//
// Storage shape on disk: <SessionsDir>/<sessionID>/plan.json (mode 0644;
// plan content is not secret). Atomic write via tmpfile + rename.
package plan

import "time"

// Status is one of three states an item can be in. The values are the
// JSON-wire forms used by the agent-callable tool, so they're stable.
type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
)

// IsValid reports whether s is one of Pending/InProgress/Completed.
func (s Status) IsValid() bool {
	switch s {
	case Pending, InProgress, Completed:
		return true
	}
	return false
}

// Item is one entry in the plan. Sub-items are at most one level deep
// (the store flattens deeper input — see Plan.Normalize).
type Item struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status Status `json:"status"`
	Sub    []Item `json:"sub,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Plan is the persisted shape. Version bumps on every Save so observers
// (TUI, CLI, RPC consumers) can detect "this is a newer plan than what
// I have cached" without diffing item-by-item.
type Plan struct {
	SessionID string    `json:"session_id"`
	Items     []Item    `json:"items"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

// Counts returns (pending, in_progress, completed) tallies across the
// plan, including sub-items. Used by the TUI/CLI summary line and the
// system-prompt prepend.
func (p *Plan) Counts() (pending, inProgress, completed int) {
	if p == nil {
		return 0, 0, 0
	}
	for _, it := range p.Items {
		switch it.Status {
		case Pending:
			pending++
		case InProgress:
			inProgress++
		case Completed:
			completed++
		}
		for _, sub := range it.Sub {
			switch sub.Status {
			case Pending:
				pending++
			case InProgress:
				inProgress++
			case Completed:
				completed++
			}
		}
	}
	return
}

// IsEmpty reports whether the plan has no items at all. Used by the
// runner to decide whether to skip the system-prompt prepend.
func (p *Plan) IsEmpty() bool {
	return p == nil || len(p.Items) == 0
}
