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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/paths"
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
			sess, err := cli.GetSession(ctx, fullID)
			if err != nil {
				return wrapRPCError(err)
			}

			layout, err := paths.FromEnv()
			if err != nil {
				return fmt.Errorf("resolve gil paths: %w", err)
			}
			spec, err := loadFrozenSpecForSession(filepath.Join(layout.SessionsDir(), fullID))
			if err != nil {
				return fmt.Errorf("load frozen spec: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Goal: %s\n", spec.Goal.OneLiner)
			if spec.Goal.Detailed != "" {
				fmt.Fprintf(out, "Detail: %s\n", spec.Goal.Detailed)
			}
			if len(spec.Goal.Tasks) > 0 {
				fmt.Fprintln(out, "Tasks:")
				for i, task := range spec.Goal.Tasks {
					fmt.Fprintf(out, "  %d. %s\n", i+1, task)
				}
			}
			meta := sessionMetaFor(sess, layout.SessionsDir())
			if meta.gitSummary != "" {
				fmt.Fprintf(out, "Git: %s\n", meta.gitSummary)
			}
			if meta.latestType != "" {
				fmt.Fprintf(out, "Latest activity: %s (%s)\n", meta.latestType, meta.latestAt.Local().Format("2006-01-02 15:04:05"))
			}
			if diff, derr := cli.Diff(ctx, fullID); derr == nil && diff != nil {
				if summary := renderGoalDiffSummary(diff); summary != "" {
					fmt.Fprintf(out, "Workspace diff: %s\n", summary)
				}
			}

			detach := !attach
			resp, err := cli.StartRun(ctx, fullID, providerName, model, detach)
			if err != nil {
				return wrapRPCError(err)
			}

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
