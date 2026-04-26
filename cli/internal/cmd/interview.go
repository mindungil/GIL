package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/sdk"
)

// eventStream is satisfied by both Start and Reply client streams.
type eventStream interface {
	Recv() (*gilv1.InterviewEvent, error)
}

// drainEvents reads events from the stream and prints agent turns and stage transitions.
// Returns when stream EOFs or encounters an error.
func drainEvents(out io.Writer, s eventStream) error {
	for {
		evt, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv event: %w", err)
		}
		if t := evt.GetAgentTurn(); t != nil {
			fmt.Fprintf(out, "Agent: %s\n", t.Content)
			continue
		}
		if st := evt.GetStage(); st != nil {
			fmt.Fprintf(out, "[stage %s → %s: %s]\n", st.From, st.To, st.Reason)
			if st.To == "confirm" {
				fmt.Fprintln(out, "Saturation reached. Run 'gil spec freeze <session-id>' to lock.")
				return nil
			}
			continue
		}
		if e := evt.GetError(); e != nil {
			return fmt.Errorf("interview error %s: %s", e.Code, e.Message)
		}
	}
}

// interviewCmd returns the `gil interview <session-id>` command.
// It runs an interactive interview: shows the agent's question, reads the
// user's reply from stdin, and loops until the user types `/done` or the
// stream ends.
func interviewCmd() *cobra.Command {
	var socket, providerName, model string
	c := &cobra.Command{
		Use:   "interview <session-id>",
		Short: "Run the interview for a session interactively",
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
			in := bufio.NewReader(cmd.InOrStdin())

			fmt.Fprint(out, "First message: ")
			firstLine, err := in.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read first input: %w", err)
			}
			firstLine = strings.TrimSpace(firstLine)

			startStream, err := cli.StartInterview(ctx, sessionID, firstLine, providerName, model, sdk.InterviewModels{})
			if err != nil {
				return fmt.Errorf("start: %w", err)
			}
			if err := drainEvents(out, startStream); err != nil {
				return err
			}

			// Reply loop
			for {
				fmt.Fprint(out, "\nYou (or /done to stop): ")
				line, err := in.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						return nil
					}
					return fmt.Errorf("read input: %w", err)
				}
				line = strings.TrimSpace(line)
				if line == "/done" || line == "" {
					return nil
				}
				replyStream, err := cli.ReplyInterview(ctx, sessionID, line)
				if err != nil {
					return fmt.Errorf("reply: %w", err)
				}
				if err := drainEvents(out, replyStream); err != nil {
					return err
				}
			}
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "anthropic", "LLM provider (anthropic|mock)")
	c.Flags().StringVar(&model, "model", "", "LLM model id (empty → provider default)")
	return c
}
