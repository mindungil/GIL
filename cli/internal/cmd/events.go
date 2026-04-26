package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jedutools/gil/sdk"
)

// eventsCmd returns the `gil events <session-id>` command.
// With --tail it subscribes to the live event stream (Phase 5+).
// In Phase 4 the server returns Unimplemented; this command surfaces that
// gracefully so users know the feature is in progress.
func eventsCmd() *cobra.Command {
	var socket string
	var tail bool
	c := &cobra.Command{
		Use:   "events <session-id>",
		Short: "Stream events from a session (currently --tail only; Phase 5+)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if !tail {
				return fmt.Errorf("only --tail is supported in Phase 4")
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return fmt.Errorf("ensure daemon: %w", err)
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			stream, err := cli.TailRun(ctx, sessionID)
			if err != nil {
				if isUnimplemented(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "Event tailing is not yet implemented (Phase 5).")
					return nil
				}
				return fmt.Errorf("tail: %w", err)
			}

			out := cmd.OutOrStdout()
			for {
				evt, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					if isUnimplemented(err) {
						fmt.Fprintln(out, "Event tailing is not yet implemented (Phase 5).")
						return nil
					}
					return fmt.Errorf("recv: %w", err)
				}
				fmt.Fprintf(out, "[#%d %s] %s\n", evt.GetId(), evt.GetType(), string(evt.GetDataJson()))
			}
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().BoolVar(&tail, "tail", false, "follow live events")
	return c
}

// isUnimplemented returns true if err is a gRPC Unimplemented status (server
// stub during Phase 4).
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
		return true
	}
	return errors.Is(err, errors.New("unimplemented")) // fallback for non-status errors
}
