// Package cmd — `gil memory` surfaces the P55 cross-session memory
// bank to the operator. The agent writes memories via the
// `remember` chat tool; this command lets the human inspect / prune
// what accumulated.
package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	_ "modernc.org/sqlite"

	"github.com/mindungil/gil/core/paths"
)

func memoryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "memory",
		Short: "Inspect / prune the cross-session memory bank (P55)",
		Long: `The cross-session memory bank is what the chat agent persists via
the ` + "`remember`" + ` tool. Notes survive daemon restart and auto-
surface in the next chat session's system prompt.

Subcommands:
  list   — print all memories newest-first
  rm     — delete one memory by id
  clear  — delete all memories (requires --force)

These commands read/write the SQLite DB directly. gild does not need
to be running.`,
	}
	c.AddCommand(memoryListCmd())
	c.AddCommand(memoryRmCmd())
	c.AddCommand(memoryClearCmd())
	return c
}

func memoryListCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List memories newest-first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openMemoryDB()
			if err != nil {
				return err
			}
			defer db.Close()
			rows, err := db.QueryContext(cmd.Context(), `SELECT id, session_id, content, created_at
				FROM session_memories ORDER BY created_at DESC LIMIT ?`, limit)
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			defer rows.Close()
			out := cmd.OutOrStdout()
			any := false
			for rows.Next() {
				var (
					id        int64
					sessionID string
					content   string
					createdAt time.Time
				)
				if err := rows.Scan(&id, &sessionID, &content, &createdAt); err != nil {
					return err
				}
				any = true
				content = strings.ReplaceAll(content, "\n", " ")
				if len(content) > 100 {
					content = content[:100] + "…"
				}
				fmt.Fprintf(out, "  %-6d  %s  %s\n",
					id, createdAt.UTC().Format("2006-01-02 15:04"), content)
			}
			if !any {
				fmt.Fprintln(out, "No memories yet. The chat agent writes them via the `remember` tool.")
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 200, "max rows to print")
	return c
}

func memoryRmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete one memory by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid id %q: %w", args[0], err)
			}
			db, err := openMemoryDB()
			if err != nil {
				return err
			}
			defer db.Close()
			res, err := db.ExecContext(cmd.Context(), `DELETE FROM session_memories WHERE id = ?`, id)
			if err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			n, _ := res.RowsAffected()
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d row(s).\n", n)
			return nil
		},
	}
	return c
}

func memoryClearCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "clear",
		Short: "Delete ALL memories (requires --force)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				return fmt.Errorf("memory clear is destructive — pass --force to confirm")
			}
			db, err := openMemoryDB()
			if err != nil {
				return err
			}
			defer db.Close()
			res, err := db.ExecContext(cmd.Context(), `DELETE FROM session_memories`)
			if err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			n, _ := res.RowsAffected()
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared %d memories.\n", n)
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "actually perform the delete (required)")
	return c
}

func openMemoryDB() (*sql.DB, error) {
	layout, err := paths.FromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve gil paths: %w", err)
	}
	dbPath := layout.SessionsDB()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	// Don't run migrations — the daemon owns those. We just read/write
	// existing rows.
	return db, nil
}

// suppress unused-import warning if context migrates later
var _ = context.Background
