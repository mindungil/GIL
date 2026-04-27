package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/sdk"
)

// runCmd returns the `gil run <session-id>` command.
// Runs the agent loop synchronously (server-side) and prints the result.
// With --detach, returns immediately and the server runs the loop in background.
// With --interactive, the run is started in detached mode and the CLI then
// reads slash commands from stdin until /quit or EOF — see runInteractive.
func runCmd() *cobra.Command {
	var socket, providerName, model string
	var detach, interactive bool
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

			// --interactive forces detach so the foreground CLI goroutine
			// is free to read stdin while the agent loop runs server-side.
			// The slash-command surface is observation-only (see Track C
			// ground rules) so this can never strand the run mid-tool-call.
			runDetached := detach
			if interactive {
				runDetached = true
			}
			resp, err := cli.StartRun(ctx, sessionID, providerName, model, runDetached)
			if err != nil {
				return wrapRPCError(err)
			}

			out := cmd.OutOrStdout()
			if interactive {
				if resp.Status != "started" && resp.Status != "" {
					fmt.Fprintf(out, "(run completed before interactive loop started: %s)\n", resp.Status)
				}
				return runInteractive(ctx, cli, sessionID, cmd.InOrStdin(), out)
			}
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
	c.Flags().BoolVar(&interactive, "interactive", false, "start run in background and accept slash commands from stdin")
	return c
}
