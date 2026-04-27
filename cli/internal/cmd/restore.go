package cmd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/sdk"
)

// restoreCmd returns the `gil restore <session-id> <step>` command.
// Rolls the workspace back to the Nth checkpoint snapshot taken during a run.
func restoreCmd() *cobra.Command {
	var socket string
	c := &cobra.Command{
		Use:   "restore <session-id> <step>",
		Short: "Roll back a session's workspace to a previous checkpoint",
		Long: `Restore the workspace to a prior checkpoint snapshot taken during a run.

step is 1-indexed where 1 is the oldest checkpoint. Use negative numbers to
count from the most recent: -1 is the latest, -2 the second latest, etc.

The session must not be currently running.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			step, err := strconv.Atoi(args[1])
			if err != nil {
				return cliutil.New(
					fmt.Sprintf("step must be an integer, got %q", args[1]),
					`use 1 (oldest), -1 (newest), or any non-zero integer`)
			}
			if step == 0 {
				return cliutil.New(
					"step must be non-zero",
					`use 1 (oldest), -1 (newest), or any non-zero integer`)
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
			resp, err := cli.RestoreRun(ctx, sessionID, int32(step))
			if err != nil {
				return wrapRPCError(err)
			}
			sha := resp.CommitSha
			if len(sha) > 12 {
				sha = sha[:12]
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Restored session %s to step %d / %d (commit %s: %s)\n",
				sessionID, step, resp.TotalCheckpoints, sha, resp.CommitMessage)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	return c
}
