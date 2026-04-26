package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

// runCmd returns the `gil run <session-id>` command.
// Runs the agent loop synchronously (server-side) and prints the result.
// With --detach, returns immediately and the server runs the loop in background.
func runCmd() *cobra.Command {
	var socket, providerName, model string
	var detach bool
	c := &cobra.Command{
		Use:   "run <session-id>",
		Short: "Run the agent loop for a frozen session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
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

			resp, err := cli.StartRun(ctx, sessionID, providerName, model, detach)
			if err != nil {
				return wrapRPCError(err)
			}

			out := cmd.OutOrStdout()
			if detach && resp.Status == "started" {
				fmt.Fprintf(out, "Started run for %s (background).\n", sessionID)
				fmt.Fprintf(out, "Watch progress:  gil events %s --tail\n", sessionID)
				fmt.Fprintf(out, "Check status:    gil status\n")
				return nil
			}

			fmt.Fprintf(out, "Status:     %s\n", resp.Status)
			fmt.Fprintf(out, "Iterations: %d\n", resp.Iterations)
			fmt.Fprintf(out, "Tokens:     %d\n", resp.Tokens)
			if resp.ErrorMessage != "" {
				fmt.Fprintf(out, "Error:      %s\n", resp.ErrorMessage)
			}
			fmt.Fprintln(out, "Verify results:")
			for _, vr := range resp.VerifyResults {
				mark := "✗"
				if vr.Passed {
					mark = "✓"
				}
				fmt.Fprintf(out, "  %s %s (exit=%d)\n", mark, vr.Name, vr.ExitCode)
			}
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "anthropic", "LLM provider (anthropic|mock)")
	c.Flags().StringVar(&model, "model", "", "LLM model id (empty → provider default)")
	c.Flags().BoolVar(&detach, "detach", false, "start run in background and return immediately")
	return c
}
