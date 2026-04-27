package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mindungil/gil/core/paths"
)

// PlanItemView is the UI-only view of one plan item. Mirrors
// core/plan.Item but lives here so the TUI doesn't drag the runtime
// plan package into its dependency graph (it already depends on
// core/paths, which is enough). When the TUI surface and the on-disk
// shape diverge the TUI is the lossy side: extra fields like Sub or
// Note are folded into the visible Text.
type PlanItemView struct {
	ID     string         `json:"id"`
	Text   string         `json:"text"`
	Status string         `json:"status"`
	Sub    []PlanItemView `json:"sub,omitempty"`
	Note   string         `json:"note,omitempty"`
}

// PlanSnapshot is the read-side state the renderer consumes. We never
// mutate it from the TUI — the agent-side plan tool owns writes; we
// just re-read on every event/refresh.
type PlanSnapshot struct {
	Items     []PlanItemView // top-level items (with one-level Sub)
	UpdatedAt time.Time
	Version   int
	NotFound  bool
}

// loadPlanSnapshot reads <SessionsDir>/<sessionID>/plan.json. NotFound
// is true (no error) when the file simply doesn't exist — the same
// "soft missing" pattern we use for memory excerpts. A malformed JSON
// surfaces as NotFound so a partially-written tmp file (theoretical
// race we already guard against with atomic-rename) can never crash
// the TUI.
func loadPlanSnapshot(sessionID string) PlanSnapshot {
	if sessionID == "" {
		return PlanSnapshot{NotFound: true}
	}
	layout, err := paths.FromEnv()
	if err != nil {
		return PlanSnapshot{NotFound: true}
	}
	p := filepath.Join(layout.SessionsDir(), sessionID, "plan.json")
	st, err := os.Stat(p)
	if err != nil {
		return PlanSnapshot{NotFound: true}
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return PlanSnapshot{NotFound: true}
	}
	var d struct {
		Items     []PlanItemView `json:"items"`
		UpdatedAt time.Time      `json:"updated_at"`
		Version   int            `json:"version"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return PlanSnapshot{NotFound: true}
	}
	if len(d.Items) == 0 {
		// Empty plans are treated as NotFound for pane-routing purposes —
		// "the agent has not actually populated a plan" reads the same
		// to the user as "no plan".
		return PlanSnapshot{NotFound: true, UpdatedAt: st.ModTime(), Version: d.Version}
	}
	return PlanSnapshot{
		Items:     d.Items,
		UpdatedAt: d.UpdatedAt,
		Version:   d.Version,
	}
}

// planCounts returns (pending, in_progress, completed) tallies across
// items + sub-items so the pane title can show "(3 items: 1 done…)".
func planCounts(items []PlanItemView) (pending, inProgress, completed int) {
	for _, it := range items {
		switch it.Status {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		default:
			pending++
		}
		for _, sub := range it.Sub {
			switch sub.Status {
			case "completed":
				completed++
			case "in_progress":
				inProgress++
			default:
				pending++
			}
		}
	}
	return
}

// planPaneTitle is the dynamic title for the Plan pane. Format:
// "Plan (3 items, 1m ago)" — the staleness suffix mirrors the Memory
// pane.
func planPaneTitle(p PlanSnapshot) string {
	if p.NotFound {
		return "Plan (none)"
	}
	total := len(p.Items)
	for _, it := range p.Items {
		total += len(it.Sub)
	}
	noun := "item"
	if total != 1 {
		noun = "items"
	}
	if p.UpdatedAt.IsZero() {
		return fmt.Sprintf("Plan (%d %s)", total, noun)
	}
	return fmt.Sprintf("Plan (%d %s, %s)", total, noun, relTimeShort(time.Since(p.UpdatedAt)))
}

// renderPlanPane renders the plan content (without border). Title is
// composed by the caller via paneFrame. Empty plan → dim hint matching
// the Memory pane's "(memory bank not yet populated)" pattern.
//
// Layout per terminal-aesthetic.md §3 (Iconography): completed → ✓
// (success), in_progress → ● (info accent), pending → ○ (dim). Notes
// are appended in dim italic to keep the eye on status first.
//
// width is the content width; very narrow widths drop the note suffix
// rather than wrapping (the Memory pane has the same convention).
func renderPlanPane(width int, p PlanSnapshot) string {
	g := Glyphs()
	if p.NotFound || len(p.Items) == 0 {
		return styleDim("(no plan yet — agent has not called the plan tool)")
	}
	var sb strings.Builder
	for i, it := range p.Items {
		sb.WriteString(planItemLine(g, width, it, false))
		for j, sub := range it.Sub {
			sb.WriteString("\n")
			sb.WriteString(planItemLine(g, width, sub, true))
			_ = j
		}
		if i < len(p.Items)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// planItemLine renders one item with the right glyph + style. When
// indented (sub-item) the line gets a 2-space prefix and a slightly
// dimmer body to keep the visual hierarchy.
func planItemLine(g Glyph, width int, it PlanItemView, indent bool) string {
	prefix := ""
	if indent {
		prefix = "  "
	}
	var glyph, body string
	switch it.Status {
	case "completed":
		// Strikethrough is too aggressive in narrow panes; we settle
		// for dim+✓ which still reads as "done" against ● and ○.
		glyph = styleSuccess(g.Done)
		body = styleDim(it.Text)
	case "in_progress":
		glyph = styleInfo(g.Running)
		body = styleSurface(it.Text)
	default:
		glyph = styleDim(g.Idle)
		body = styleSurface(it.Text)
	}
	line := fmt.Sprintf("%s%s %s", prefix, glyph, body)
	// Append note if there's room. Width budget: prefix + 1 glyph + 1
	// space + body + " — " + note. We don't measure ANSI in width;
	// styleSurface adds a fixed-cost overhead so a soft 4-char buffer
	// is enough to keep most rows on a single line in a 30-col pane.
	if it.Note != "" {
		need := len(prefix) + 2 + len(it.Text) + 4 + len(it.Note)
		if need < width {
			line += "  " + styleDim("— "+it.Note)
		}
	}
	return truncate(line, max(width, 8))
}
