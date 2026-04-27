package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/sdk"
)

// statusSessionJSON is the JSON shape emitted by `gil status --output json`.
// All fields are present unconditionally (zero values are real data here, not
// "missing") so consumers can read each key without a presence check.
type statusSessionJSON struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	WorkingDir       string `json:"working_dir"`
	GoalHint         string `json:"goal_hint"`
	CurrentIteration int32  `json:"current_iteration"`
	CurrentTokens    int64  `json:"current_tokens"`
}

type statusJSONReport struct {
	Sessions []statusSessionJSON `json:"sessions"`
}

// statusCmd returns the "status" subcommand for listing sessions.
//
// As of Phase 14 the default rendering is the visual "mission-control"
// card layout (one card per session, with a sub-cell progress bar and
// a meta row underneath). Two flags drop back to the legacy formats:
//
//   --plain       the original tab-separated table — script friendly,
//                 stable column order, never emits ANSI
//   --output json the structured JSON envelope — same shape as before
//
// We deliberately keep both fallbacks: any external script that has
// been parsing the table since Phase 1-12 keeps working as long as it
// is updated to pass --plain.
func statusCmd() *cobra.Command {
	var socket string
	var limit int
	var plain bool
	c := &cobra.Command{
		Use:   "status",
		Short: "List sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 {
				return cliutil.New(
					fmt.Sprintf("--limit must be positive, got %d", limit),
					`try --limit 100 (or any positive integer)`)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			list, err := cli.ListSessions(ctx, limit)
			if err != nil {
				return wrapRPCError(err)
			}
			if outputJSON() {
				return writeStatusJSON(cmd.OutOrStdout(), list)
			}
			if plain {
				return writeStatusText(cmd.OutOrStdout(), list)
			}
			return writeStatusVisual(cmd.OutOrStdout(), list, asciiMode)
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().IntVar(&limit, "limit", 100, "max sessions to list")
	c.Flags().BoolVar(&plain, "plain", false, "use the legacy tab-separated table (script friendly)")
	return c
}

// writeStatusText is the legacy human-readable rendering — preserved 1:1
// from the pre-Track-G code path so existing scripts that grep the table
// continue to work.
func writeStatusText(w io.Writer, list []*sdk.Session) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tITER\tTOKENS\tWORKING_DIR\tGOAL")
	for _, s := range list {
		iter := "-"
		tokens := "-"
		if s.CurrentIteration != 0 {
			iter = fmt.Sprintf("%d", s.CurrentIteration)
		}
		if s.CurrentTokens != 0 {
			tokens = fmt.Sprintf("%d", s.CurrentTokens)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Status, iter, tokens, s.WorkingDir, s.GoalHint)
	}
	return tw.Flush()
}

// writeStatusJSON emits the structured shape under the persistent
// --output json flag. We always populate the "sessions" key (with an
// empty array when nothing is configured) so downstream jq filters can
// read .sessions[] without first checking presence.
func writeStatusJSON(w io.Writer, list []*sdk.Session) error {
	rows := make([]statusSessionJSON, 0, len(list))
	for _, s := range list {
		if s == nil {
			continue
		}
		rows = append(rows, statusSessionJSON{
			ID:               s.ID,
			Status:           s.Status,
			WorkingDir:       s.WorkingDir,
			GoalHint:         s.GoalHint,
			CurrentIteration: s.CurrentIteration,
			CurrentTokens:    s.CurrentTokens,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(statusJSONReport{Sessions: rows})
}
