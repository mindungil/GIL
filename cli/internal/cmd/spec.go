package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jedutools/gil/sdk"
)

// specCmd returns the `gil spec <session-id>` command.
// It also has a subcommand: `gil spec freeze <session-id>`.
func specCmd() *cobra.Command {
	var socket string
	c := &cobra.Command{
		Use:   "spec <session-id>",
		Short: "Show the current spec for a session (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
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

			fs, err := cli.GetSpec(ctx, sessionID)
			if err != nil {
				return fmt.Errorf("get spec: %w", err)
			}
			data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(fs)
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.AddCommand(specFreezeCmd())
	return c
}

// specFreezeCmd returns the `gil spec freeze <session-id>` command.
func specFreezeCmd() *cobra.Command {
	var socket string
	c := &cobra.Command{
		Use:   "freeze <session-id>",
		Short: "Freeze the spec (write spec.lock; immutable thereafter)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
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

			specID, hex, err := cli.ConfirmInterview(ctx, sessionID)
			if err != nil {
				return fmt.Errorf("confirm: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Frozen: spec_id=%s sha256=%s\n", specID, hex)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	return c
}
