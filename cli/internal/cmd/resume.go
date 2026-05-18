// Package cmd — `gil resume <id-or-prefix>` is the explicit-resume
// counterpart of `gil run`. Resolves a 10-char ULID prefix (matching
// what `gil status` displays) to a full session id and starts a
// fresh run for it. Useful for resuming a session you saw orphaned
// in `gil session orphans` or in the status summary without having
// to copy-paste the full 26-char ULID.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/sdk"
)

func resumeCmd() *cobra.Command {
	var socket, providerName, model string
	var attach bool
	c := &cobra.Command{
		Use:   "resume <session-id-or-prefix>",
		Short: "Restart a frozen session (accepts 10-char ULID prefix)",
		Long: `Start a fresh run for an existing frozen session, identified
either by full ULID or by the 10-char prefix that ` + "`gil status`" + ` shows.

This is the explicit-resume counterpart of ` + "`gil run`" + `. Both kick
RunService.Start, but resume:
  - accepts a ULID prefix and resolves it via session list (errors on
    ambiguous prefixes — pass the full id to disambiguate);
  - defaults to detached mode (the use case is "pick up a long task";
    you almost never want to block on stdout); pass --attach to
    foreground.

Useful after seeing a session in ` + "`gil session orphans`" + ` or after
a daemon bounce when you want to re-trigger a verify-gated task
manually.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			needle := strings.ToUpper(strings.TrimSpace(args[0]))
			if needle == "" {
				return fmt.Errorf("session id-or-prefix required")
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

			fullID, err := resolveSessionPrefix(ctx, cli, needle)
			if err != nil {
				return err
			}

			detach := !attach
			resp, err := cli.StartRun(ctx, fullID, providerName, model, detach)
			if err != nil {
				return wrapRPCError(err)
			}

			out := cmd.OutOrStdout()
			if detach && resp.Status == "started" {
				fmt.Fprintf(out, "Resumed %s (background).\n", fullID)
				fmt.Fprintf(out, "Watch progress:  gil events %s --tail\n", fullID)
				fmt.Fprintf(out, "Check status:    gil status\n")
				return nil
			}
			fmt.Fprintf(out, "Status:     %s\n", resp.Status)
			fmt.Fprintf(out, "Iterations: %d\n", resp.Iterations)
			fmt.Fprintf(out, "Tokens:     %d\n", resp.Tokens)
			if resp.ErrorMessage != "" {
				fmt.Fprintf(out, "Error:      %s\n", resp.ErrorMessage)
			}
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&providerName, "provider", "", "LLM provider (anthropic|openai|openrouter|vllm|mock); empty → spec/workspace config")
	c.Flags().StringVar(&model, "model", "", "LLM model id; empty → spec/workspace config")
	c.Flags().BoolVar(&attach, "attach", false, "block until the run finishes (default: detach)")
	return c
}

// resolveSessionPrefix accepts a full 26-char ULID OR a 10-char (or
// longer) prefix and returns the full session id. Ambiguous prefixes
// (more than one session starts with the same chars) are an error;
// the caller is expected to disambiguate.
//
// Lists up to 200 sessions, newest first — the use case is "I just
// saw this in status, resume it" so the recently-seen window is what
// matters. If a user needs to resume a very old session they need
// the full id anyway.
func resolveSessionPrefix(ctx context.Context, cli *sdk.Client, needle string) (string, error) {
	const ulidLen = 26
	needle = strings.ToUpper(needle)
	if len(needle) == ulidLen {
		return needle, nil // assume full ULID; SDK rejects unknown ids
	}
	if len(needle) < 4 {
		return "", fmt.Errorf("prefix %q too short — pass at least 4 chars (or the full 26-char ULID)", needle)
	}
	sessions, err := cli.ListSessions(ctx, 200)
	if err != nil {
		return "", wrapRPCError(err)
	}
	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(strings.ToUpper(s.ID), needle) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session in the recent 200 starts with %q — pass the full 26-char ULID", needle)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("prefix %q matches %d sessions: %s — disambiguate with more chars",
			needle, len(matches), strings.Join(matches, ", "))
	}
}
