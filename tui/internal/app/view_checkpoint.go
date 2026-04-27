package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mindungil/gil/sdk"
)

// CheckpointEntry is one row in the checkpoint timeline modal. The TUI
// builds the slice from observed checkpoint_committed events in the
// session's tail buffer; the server has no ListCheckpoints RPC yet.
type CheckpointEntry struct {
	Step    int    // 1-indexed step number
	When    string // "18:01:23"
	Iter    int32  // iteration the checkpoint was taken at
	Summary string // short description (note from event payload)
}

// CheckpointModalState holds the open/closed state of the checkpoint
// modal plus its selected row.
type CheckpointModalState struct {
	Open       bool
	Entries    []CheckpointEntry
	Selected   int    // 0-based index into Entries
	Error      string // non-empty when last Restore attempt failed
	Notice     string // transient success notice, dim
}

// renderCheckpointModal renders the checkpoint-list modal per spec §11
// + the Phase 14 plan layout. width caps the modal width; we let
// lipgloss wrap any oversized rows by truncating summaries instead.
//
// The modal uses the rounded light frame plus an internal table:
//
//	step  when      iter  summary
//	----  --------  ----  ---------------------------------
//	  1   18:01:23     1  baseline
//	  2   18:04:11     3  wired theme provider
//	→ 3   18:09:55     7  ✓ first 2 checks
//	  ...
//
// Selected row is marked by `›` (accent-info) flush left, no bg fill —
// per spec §11 ("Background fills inside modals are forbidden").
func renderCheckpointModal(width int, st CheckpointModalState) string {
	g := Glyphs()
	header := styleHeader("Checkpoints")
	var body strings.Builder
	body.WriteString(header)
	body.WriteString("\n\n")
	if len(st.Entries) == 0 {
		body.WriteString(styleDim("(no checkpoints yet — they appear after the first iteration)"))
	} else {
		// Column widths: step=4, when=8, iter=4, summary=rest.
		body.WriteString(styleDim(fmt.Sprintf(" %-4s  %-8s  %-4s  %s", "step", "when", "iter", "summary")))
		body.WriteString("\n")
		body.WriteString(styleDim(fmt.Sprintf(" %s  %s  %s  %s",
			strings.Repeat(g.HSep, 4),
			strings.Repeat(g.HSep, 8),
			strings.Repeat(g.HSep, 4),
			strings.Repeat(g.HSep, max(width-30, 10)))))
		body.WriteString("\n")
		summaryW := max(width-32, 10)
		for i, e := range st.Entries {
			marker := " "
			rowText := fmt.Sprintf(" %4d  %-8s  %4d  %s",
				e.Step, e.When, e.Iter, truncate(e.Summary, summaryW))
			if i == st.Selected {
				marker = styleEmphasis(g.Arrow)
				rowText = styleSurface(rowText)
			} else {
				rowText = styleDim(rowText)
			}
			body.WriteString(marker)
			body.WriteString(rowText)
			body.WriteString("\n")
		}
	}
	if st.Error != "" {
		body.WriteString("\n")
		body.WriteString(styleAlert(g.Failed + " " + st.Error))
		body.WriteString("\n")
	} else if st.Notice != "" {
		body.WriteString("\n")
		body.WriteString(styleSuccess(g.Done + " " + st.Notice))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(styleDim("↑/↓ navigate " + g.Dot + " enter restore " + g.Dot + " esc close"))

	frame := paneFrame("").Padding(1, 2)
	return frame.Render(body.String())
}

// extractCheckpointEntries walks raw events and builds the timeline
// from checkpoint_committed payloads. Order is oldest-first (the order
// they arrived in the tail buffer).
func extractCheckpointEntries(events []*tailEventLite) []CheckpointEntry {
	out := make([]CheckpointEntry, 0)
	step := 0
	var iter int32
	for _, ev := range events {
		if ev.Type == "iteration_start" {
			iter = ev.Iter
		}
		if ev.Type != "checkpoint_committed" {
			continue
		}
		step++
		out = append(out, CheckpointEntry{
			Step:    step,
			When:    ev.When,
			Iter:    iter,
			Summary: ev.Note,
		})
	}
	return out
}

// tailEventLite is a tiny projection of *gilv1.Event used by the
// checkpoint extractor; defined separately so tests can construct
// fixtures without touching protobuf.
type tailEventLite struct {
	Type string
	When string
	Iter int32
	Note string // for checkpoint_committed: "note" or short SHA fallback
}

// restoreCheckpointCmd issues a RunService.Restore RPC in a goroutine
// and surfaces the outcome as a checkpointRestoreMsg. step is the
// 1-indexed step number from the modal row.
func restoreCheckpointCmd(client *sdk.Client, sessionID string, step int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.RestoreRun(ctx, sessionID, int32(step))
		if err != nil {
			return checkpointRestoreMsg{err: err.Error()}
		}
		return checkpointRestoreMsg{success: true, step: step}
	}
}

// checkpointRestoreMsg is the response from the Restore RPC.
type checkpointRestoreMsg struct {
	success bool
	step    int
	err     string
}
