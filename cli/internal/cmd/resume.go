package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jedutools/gil/sdk"
)

// resumeCmd returns the `gil resume <session-id>` command.
// It re-emits the last agent turn from an in-progress interview so the user
// knows where they left off. (Cross-restart persistent resume = Phase 4.)
func resumeCmd() *cobra.Command {
	var socket, providerName, model string
	c := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume an in-progress interview (shows the last agent turn)",
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

			out := cmd.OutOrStdout()
			stream, err := cli.StartInterview(ctx, sessionID, "", providerName, model, sdk.InterviewModels{})
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
			for {
				evt, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return fmt.Errorf("recv: %w", err)
				}
				if st := evt.GetStage(); st != nil {
					fmt.Fprintf(out, "[stage %s → %s: %s]\n", st.From, st.To, st.Reason)
				}
				if t := evt.GetAgentTurn(); t != nil {
					fmt.Fprintf(out, "Agent: %s\n", t.Content)
				}
			}
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "anthropic", "provider name")
	c.Flags().StringVar(&model, "model", "", "model id (empty → provider default)")
	return c
}
