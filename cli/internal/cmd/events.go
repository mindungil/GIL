package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/core/cliutil"
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
				return cliutil.New(
					"replay-from-disk is not implemented yet",
					`pass --tail to stream events from a live run`)
			}
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			stream, err := cli.TailRun(ctx, sessionID)
			if err != nil {
				return wrapRPCError(err)
			}

			if outputJSON() {
				return tailEventsJSON(ctx, stream, cmd.OutOrStdout())
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

// tailEventsJSON is the --output json sibling of tailEvents. It emits one
// JSON object per event (line-delimited NDJSON) so consumers can pipe the
// stream through jq or split on newlines without buffering the whole run.
//
// The shape mirrors the on-disk events.jsonl format produced by
// core/event/persist.go — same field names, same value types — so a
// downstream tool that already parses the persisted file can re-use its
// schema for the live stream.
func tailEventsJSON(ctx context.Context, stream gilv1.RunService_TailClient, out io.Writer) error {
	enc := json.NewEncoder(out)
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		// Pre-parse the inner data_json so the resulting envelope is one
		// well-formed JSON document instead of a string-quoted blob.
		// Empty/invalid bodies fall back to {} to keep the schema stable.
		var data json.RawMessage
		raw := evt.GetDataJson()
		if len(raw) > 0 && json.Valid(raw) {
			data = raw
		} else {
			data = json.RawMessage("{}")
		}

		var ts time.Time
		if t := evt.GetTimestamp(); t != nil {
			ts = t.AsTime().UTC()
		}

		envelope := struct {
			Timestamp time.Time       `json:"timestamp"`
			Source    string          `json:"source"`
			Kind      string          `json:"kind"`
			Type      string          `json:"type"`
			Data      json.RawMessage `json:"data"`
		}{
			Timestamp: ts,
			Source:    evt.GetSource().String(),
			Kind:      evt.GetKind().String(),
			Type:      evt.GetType(),
			Data:      data,
		}
		if err := enc.Encode(&envelope); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	}
}
