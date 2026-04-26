package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/sdk"
)

// eventsCmd returns the `gil events <session-id>` command.
// With --tail it subscribes to the live event stream from RunService.Tail.
// Replay-from-disk (no --tail) is deferred to Phase 6.
func eventsCmd() *cobra.Command {
	var socket string
	var tail bool
	c := &cobra.Command{
		Use:   "events <session-id>",
		Short: "Stream events from a live run session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if !tail {
				return fmt.Errorf("replay-from-disk not yet implemented (Phase 6); use --tail to stream live events")
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
				return fmt.Errorf("tail: %w", err)
			}

			return tailEvents(ctx, stream, cmd.OutOrStdout())
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().BoolVar(&tail, "tail", false, "follow live events")
	return c
}

// tailEvents reads events from stream and prints them to out until EOF or error.
func tailEvents(ctx context.Context, stream gilv1.RunService_TailClient, out io.Writer) error {
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		ts := "-"
		if t := evt.GetTimestamp(); t != nil {
			ts = t.AsTime().UTC().Format(time.RFC3339)
		}

		source := evt.GetSource().String()
		kind := evt.GetKind().String()

		data := string(evt.GetDataJson())
		if data == "" {
			data = "{}"
		}

		fmt.Fprintf(out, "%s %s %s %s %s\n", ts, source, kind, evt.GetType(), data)
	}
}
