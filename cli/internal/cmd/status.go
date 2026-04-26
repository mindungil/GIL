package cmd

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

// statusCmd returns the "status" subcommand for listing sessions.
// It lists all sessions from the gild gRPC server in a tab-separated table format.
func statusCmd() *cobra.Command {
	var socket string
	var limit int
	c := &cobra.Command{
		Use:   "status",
		Short: "List sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 {
				return fmt.Errorf("--limit must be positive, got %d", limit)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return fmt.Errorf("ensure daemon: %w", err)
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			list, err := cli.ListSessions(ctx, limit)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tWORKING_DIR\tGOAL")
			for _, s := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Status, s.WorkingDir, s.GoalHint)
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().IntVar(&limit, "limit", 100, "max sessions to list")
	return c
}
