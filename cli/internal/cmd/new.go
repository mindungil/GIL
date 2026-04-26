package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

// newCmd returns the "new" subcommand for creating a new session.
// It creates a new session via the gild gRPC server and prints the session ID.
func newCmd() *cobra.Command {
	var socket, workingDir, goalHint string
	c := &cobra.Command{
		Use:   "new",
		Short: "Create a new session",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()
			s, err := cli.CreateSession(ctx, sdk.CreateOptions{
				WorkingDir: workingDir,
				GoalHint:   goalHint,
			})
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session created: %s\n", s.ID)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&workingDir, "working-dir", "", "project working directory")
	c.Flags().StringVar(&goalHint, "goal", "", "optional goal hint")
	return c
}
