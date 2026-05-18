// Package cmd — `gil session orphans` lists sessions that were reaped
// by P36 (daemon_restart) or P38 (stale_heartbeat). Uses the audit
// row in events.jsonl as the source of truth, so a session that
// was orphaned and later manually re-run is still listed (the audit
// row is forensic).
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/core/paths"
	"github.com/mindungil/gil/sdk"
)

// orphanRow is one rendered line in the output table.
type orphanRow struct {
	ID         string    `json:"id"`
	GoalHint   string    `json:"goal_hint"`
	Reason     string    `json:"reason"`
	AutoResume bool      `json:"auto_resume"`
	OrphanedAt time.Time `json:"orphaned_at"`
	Status     string    `json:"status"` // current status (may have moved past stopped)
}

// orphanData mirrors the JSON payload P36 (daemon_restart) and P38
// (stale_heartbeat) write to events.jsonl for run_orphaned events.
type orphanData struct {
	Reason       string `json:"reason"`
	PriorStatus  string `json:"prior_status"`
	AutoResume   bool   `json:"auto_resume"`
}

func sessionOrphansCmd() *cobra.Command {
	var socket string
	var limit int
	var jsonOut bool
	c := &cobra.Command{
		Use:   "orphans",
		Short: "List sessions that were reaped (P36 daemon restart / P38 stale heartbeat)",
		Long: `List sessions whose events.jsonl carries a run_orphaned audit
row. Useful after a daemon bounce ("which of my runs got killed?")
or for forensic review of mid-session sweeper activity.

The list reads the per-session events.jsonl directly — gild does NOT
need to be running. A session that was orphaned and later manually
re-run via ` + "`gil run`" + ` is still listed; the audit row is
forensic, not a live state. To re-trigger a stopped orphan, run
` + "`gil run <id> --detach`" + ` (or wait for daemon auto-resume if the
spec opted in via Risk.ResumeOnRestart).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			layout, lerr := paths.FromEnv()
			if lerr != nil {
				return fmt.Errorf("resolve gil paths: %w", lerr)
			}
			// Use the SDK to enumerate sessions so the rendering matches
			// other surfaces (status/list). We need the current status
			// to render alongside the orphan event, so going through
			// the daemon is the cleanest path.
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			sessions, err := cli.ListSessions(ctx, limit)
			if err != nil {
				return wrapRPCError(err)
			}
			rows := make([]orphanRow, 0, len(sessions))
			for _, s := range sessions {
				row, found := loadOrphanRow(layout.SessionsDir(), s.ID, s.GoalHint, s.Status)
				if !found {
					continue
				}
				rows = append(rows, row)
			}
			out := cmd.OutOrStdout()
			if jsonOut || outputFormat == "json" {
				b, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(out, string(b))
				return nil
			}
			if len(rows) == 0 {
				fmt.Fprintln(out, "No orphan rows found in the most recent sessions.")
				return nil
			}
			fmt.Fprintln(out, "Sessions reaped (P36 daemon_restart / P38 stale_heartbeat):")
			fmt.Fprintln(out, "")
			fmt.Fprintf(out, "  %-26s  %-16s  %-5s  %-7s  %s\n",
				"ID", "REASON", "AUTO", "STATUS", "GOAL")
			for _, r := range rows {
				autoStr := "no"
				if r.AutoResume {
					autoStr = "yes"
				}
				fmt.Fprintf(out, "  %-26s  %-16s  %-5s  %-7s  %s\n",
					r.ID, r.Reason, autoStr, r.Status, truncateGoal(r.GoalHint, 60))
			}
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().IntVar(&limit, "limit", 100, "max sessions to scan")
	c.Flags().BoolVar(&jsonOut, "json", false, "output as JSON array")
	return c
}

// loadOrphanRow inspects the session's events.jsonl for the most
// recent run_orphaned row. Returns found=false when none present.
// Best-effort: read errors, JSON parse errors, missing file all
// just mean "no orphan row" rather than failing the whole listing.
func loadOrphanRow(sessionsDir, sessionID, goal, status string) (orphanRow, bool) {
	eventsPath := filepath.Join(sessionsDir, sessionID, "events", "events.jsonl")
	events, err := event.LoadAll(eventsPath)
	if err != nil {
		return orphanRow{}, false
	}
	// Walk backwards to find the LATEST run_orphaned row (a session
	// could in theory be reaped + auto-resumed + reaped again; latest
	// is what matters).
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "run_orphaned" {
			continue
		}
		var d orphanData
		_ = json.Unmarshal(events[i].Data, &d)
		return orphanRow{
			ID:         sessionID,
			GoalHint:   goal,
			Reason:     d.Reason,
			AutoResume: d.AutoResume,
			OrphanedAt: events[i].Timestamp,
			Status:     strings.ToLower(status),
		}, true
	}
	return orphanRow{}, false
}

func truncateGoal(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// orphansOutputFormatIsJSON exposes outputFormat for unit tests
// that need to inject the json mode without going through the
// root persistent flag plumbing.
var _ = func() bool { return outputFormat == "json" }
