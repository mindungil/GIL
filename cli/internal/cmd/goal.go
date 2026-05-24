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

func goalCmd() *cobra.Command {
	var socket string
	c := &cobra.Command{
		Use:   "goal <session-id-or-prefix>",
		Short: "Show the frozen goal, tasks, and latest activity",
		Long: `Show the frozen goal in a session: one-liner, detailed goal,
tasks, success criteria, non-goals, and the latest recorded activity.

Accepts a full ULID or the short prefix shown in ` + "`gil status`" + `.
This is the closest CLI analogue to Codex's /goal reminder: it keeps
the user anchored to what the session is actually supposed to do.`,
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
			summary, err := loadFrozenSpecForSession(filepath.Join(layout.SessionsDir(), fullID))
			if err != nil {
				return fmt.Errorf("load frozen spec: %w", err)
			}
			meta := sessionMetaFor(sess, layout.SessionsDir())

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Goal for %s\n", fullID)
			fmt.Fprintf(out, "Status: %s\n", strings.ToLower(sess.Status))
			fmt.Fprintf(out, "One-liner: %s\n", summary.Goal.OneLiner)
			if summary.Goal.Detailed != "" {
				fmt.Fprintf(out, "Detailed: %s\n", summary.Goal.Detailed)
			}
			if len(summary.Goal.Tasks) > 0 {
				fmt.Fprintln(out, "Tasks:")
				for i, task := range summary.Goal.Tasks {
					fmt.Fprintf(out, "  %d. %s\n", i+1, task)
				}
			}
			if len(summary.Goal.SuccessCriteriaNatural) > 0 {
				fmt.Fprintln(out, "Success criteria:")
				for i, criterion := range summary.Goal.SuccessCriteriaNatural {
					fmt.Fprintf(out, "  %d. %s\n", i+1, criterion)
				}
			}
			if len(summary.Goal.NonGoals) > 0 {
				fmt.Fprintln(out, "Non-goals:")
				for i, item := range summary.Goal.NonGoals {
					fmt.Fprintf(out, "  %d. %s\n", i+1, item)
				}
			}
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
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	return c
}

func renderGoalDiffSummary(diff *sdk.DiffResult) string {
	if diff == nil {
		return ""
	}
	if diff.Note != "" {
		return diff.Note
	}
	if strings.TrimSpace(diff.UnifiedDiff) == "" {
		short := diff.CheckpointSHA
		if len(short) > 8 {
			short = short[:8]
		}
		return fmt.Sprintf("workspace matches checkpoint %s (no changes)", short)
	}
	short := diff.CheckpointSHA
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%d files, +%d/-%d%s", diff.FilesChanged, diff.LinesAdded, diff.LinesRemoved, func() string {
		if diff.Truncated {
			return fmt.Sprintf(" (truncated %d bytes)", diff.TruncatedBytes)
		}
		return ""
	}())
}
