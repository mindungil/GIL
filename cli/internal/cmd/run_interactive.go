package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jedutools/gil/core/paths"
	"github.com/jedutools/gil/core/slash"
	"github.com/jedutools/gil/sdk"
)

// runInteractive blocks on stdin reading line-by-line. Lines starting
// with "/" are dispatched through the shared slash registry; everything
// else is intentionally ignored (mid-run free-form prompts would
// violate the "agent decides, system safety net" rule — slash commands
// are observation surfaces, never an interrupt mechanism).
//
// The function returns when stdin is exhausted, /quit is dispatched, or
// the context is cancelled. The caller is responsible for the run
// completing on the server — this loop is purely a side-channel.
func runInteractive(ctx context.Context, cli *sdk.Client, sessionID string, in io.Reader, out io.Writer) error {
	reg, env := buildInteractiveSlash(cli, sessionID)
	fmt.Fprintf(out, "interactive mode — type /help for commands, /quit to leave\n")

	scanner := bufio.NewScanner(in)
	// Stdin lines can carry pasted JSON or long absolute paths; raise the
	// default 64 KB token cap so the loop doesn't refuse a long /agents
	// path on Windows.
	scanner.Buffer(make([]byte, 0, 1<<14), 1<<20)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		cmd, ok := slash.ParseLine(line)
		if !ok {
			fmt.Fprintln(out, "(only slash commands are dispatched mid-run; type /help for the list)")
			continue
		}
		spec, ok := reg.Lookup(cmd.Name)
		if !ok {
			fmt.Fprintf(out, "unknown command: /%s  (try /help)\n", cmd.Name)
			continue
		}
		if !spec.NoSession && env.SessionID == "" {
			fmt.Fprintln(out, "no session attached")
			continue
		}
		// Each handler gets its own short-lived context so a hung gRPC
		// call doesn't lock the whole interactive loop.
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		output, err := spec.Handler(hctx, cmd)
		cancel()
		if err != nil {
			if errors.Is(err, slash.ErrQuit) {
				if output != "" {
					fmt.Fprintln(out, output)
				}
				return nil
			}
			fmt.Fprintf(out, "error: %s\n", err)
			continue
		}
		if output != "" {
			fmt.Fprintln(out, output)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("interactive: read stdin: %w", err)
	}
	return nil
}

// buildInteractiveSlash mirrors initSlashState in tui/internal/app/slash.go
// but without the Bubbletea-specific local state (no event ring buffer
// to clear in the headless CLI).
func buildInteractiveSlash(cli *sdk.Client, sessionID string) (*slash.Registry, *slash.HandlerEnv) {
	layout, _ := paths.FromEnv()
	env := &slash.HandlerEnv{
		SessionID: sessionID,
		Layout:    layout,
		Fetcher: func(ctx context.Context, id string) (*slash.SessionInfo, error) {
			if cli == nil {
				return nil, errors.New("no gRPC client")
			}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			s, err := cli.GetSession(cctx, id)
			if err != nil {
				return nil, err
			}
			return &slash.SessionInfo{
				ID:               s.ID,
				Status:           s.Status,
				WorkingDir:       s.WorkingDir,
				GoalHint:         s.GoalHint,
				CurrentIteration: s.CurrentIteration,
				CurrentTokens:    s.CurrentTokens,
			}, nil
		},
		// /clear is a no-op in the CLI — there's no surface buffer to
		// wipe, and the help text already calls this out.
		Local:  slash.LocalState{ClearEvents: func() {}},
		Stdout: nil, // /agents uses stdout printing only when this is an *os.File terminal
	}
	reg := slash.NewRegistry()
	slash.RegisterDefaults(reg, env)
	return reg, env
}
