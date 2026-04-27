// Package cmd — `gil clarify` is the surface for answering the
// clarify-tool's pause from the CLI. Useful for ssh users without TUI
// access, scripted reviewers in CI, and the e2e suite. The same RPC
// (RunService.AnswerClarification) is used by the TUI modal — this
// command is just a thinner shell over the SDK.
package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/core/cliutil"
	"github.com/mindungil/gil/core/event"
	"github.com/mindungil/gil/sdk"
)

// clarifyCmd returns the `gil clarify <session-id> [answer]` command.
//
// Modes (mutually exclusive flags resolved in this order):
//
//	--list         → print pending askIDs + question lines, no RPC sent
//	--pick N       → answer the latest pending ask with suggestion #N
//	<answer>       → answer the latest pending ask with the literal string
//	(no args)      → interactive prompt: print the question + suggestions,
//	                 read one line from stdin, send as the answer
//
// We resolve the latest pending ask by walking the on-disk events.jsonl
// in reverse (most recent first) and picking the first clarify_requested
// whose ask_id has not been followed by a clarify_answered marker.
// AnswerClarification's delivered=false on a stale ID prevents the
// double-answer race when the user retries quickly.
func clarifyCmd() *cobra.Command {
	var (
		socket string
		list   bool
		pick   int
		askID  string
	)
	c := &cobra.Command{
		Use:   "clarify <session-id> [answer]",
		Short: "Answer a pending clarification request from the agent",
		Long: `Answer a pending clarify_requested event raised by the agent's
'clarify' tool. The agent loop is paused mid-iteration and waiting on
the user; sending an answer here unblocks it (the answer is fed back
to the model as the tool_result content).

Examples:
  gil clarify abc123                    # interactive (read answer from stdin)
  gil clarify abc123 "deploy now"       # send the literal string
  gil clarify abc123 --pick 2           # pick suggestion #2 from the latest ask
  gil clarify abc123 --list             # show pending asks; do not answer
  gil clarify abc123 --ask-id <id> "x"  # answer a specific ask (when multiple pending)`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			pendings, err := loadPendingClarifications(sessionID)
			if err != nil {
				return cliutil.Wrap(err,
					"could not read events for session",
					`run "gil events `+sessionID+` --tail" to verify the session exists`)
			}

			if list {
				return renderPendingClarifications(cmd.OutOrStdout(), pendings)
			}

			if len(pendings) == 0 {
				return cliutil.New(
					"no pending clarification asks for this session",
					`the agent must call its 'clarify' tool first; tail the events stream to see the request`)
			}

			// Pick the target ask: explicit --ask-id wins; otherwise
			// most recent pending.
			var target *pendingClarification
			if askID != "" {
				for i := range pendings {
					if pendings[i].AskID == askID {
						target = &pendings[i]
						break
					}
				}
				if target == nil {
					return cliutil.New(
						fmt.Sprintf("no pending clarification with ask-id %q", askID),
						`pass --list to see the live askIDs`)
				}
			} else {
				target = &pendings[len(pendings)-1]
			}

			// Resolve answer.
			var answer string
			switch {
			case pick > 0:
				if pick > len(target.Suggestions) {
					return cliutil.New(
						fmt.Sprintf("--pick %d out of range (only %d suggestions)", pick, len(target.Suggestions)),
						`run with --list to see the available suggestions`)
				}
				answer = target.Suggestions[pick-1]
			case len(args) >= 2:
				answer = strings.Join(args[1:], " ")
			default:
				// Interactive: print the question + suggestions, read
				// one line from stdin. Trim newline; empty input is
				// permitted (matches the RPC contract).
				if err := renderClarifyPrompt(cmd.OutOrStdout(), *target); err != nil {
					return err
				}
				ans, rerr := readClarifyAnswer(cmd.InOrStdin())
				if rerr != nil && rerr != io.EOF {
					return cliutil.Wrap(rerr, "could not read answer from stdin",
						`pipe the answer in directly: echo "..." | gil clarify <id>`)
				}
				answer = strings.TrimRight(ans, "\r\n")
			}

			// Send the answer.
			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			delivered, err := cli.AnswerClarification(ctx, sessionID, target.AskID, answer)
			if err != nil {
				return wrapRPCError(err)
			}
			if !delivered {
				return cliutil.New(
					"answer not delivered (ask is no longer pending)",
					`the run may have timed out or already been answered; tail the events stream to confirm`)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "answered ask %s\n", target.AskID)
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().BoolVar(&list, "list", false, "list pending clarifications and exit")
	c.Flags().IntVar(&pick, "pick", 0, "answer with the Nth suggestion (1-indexed)")
	c.Flags().StringVar(&askID, "ask-id", "", "target a specific ask (default: most recent pending)")
	return c
}

// pendingClarification is the in-memory shape of a clarify_requested
// event we surface to the user. We deliberately keep it tiny — the
// CLI only needs enough to render a prompt + send the answer.
type pendingClarification struct {
	AskID       string
	Question    string
	Context     string
	Suggestions []string
	Urgency     string
}

// loadPendingClarifications walks the session's events.jsonl in order
// and returns clarify_requested asks for which we have NOT seen a
// matching clarify_answered (the daemon emits one when the runner
// resumes — see runner.go's tool_result emission). Returned slice is
// ordered oldest → newest; callers pick `[-1]` for "the latest".
func loadPendingClarifications(sessionID string) ([]pendingClarification, error) {
	path := filepath.Join(defaultBase(), "sessions", sessionID, "events", "events.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	events, err := event.LoadAll(path)
	if err != nil {
		return nil, err
	}
	answered := map[string]bool{}
	var asks []pendingClarification
	for _, e := range events {
		switch e.Type {
		case "clarify_requested":
			var d struct {
				AskID       string   `json:"ask_id"`
				Question    string   `json:"question"`
				Context     string   `json:"context"`
				Suggestions []string `json:"suggestions"`
				Urgency     string   `json:"urgency"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil || d.AskID == "" {
				continue
			}
			asks = append(asks, pendingClarification{
				AskID:       d.AskID,
				Question:    d.Question,
				Context:     d.Context,
				Suggestions: d.Suggestions,
				Urgency:     d.Urgency,
			})
		case "clarify_answered":
			var d struct {
				AskID string `json:"ask_id"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil {
				continue
			}
			if d.AskID != "" {
				answered[d.AskID] = true
			}
		}
	}
	pending := make([]pendingClarification, 0, len(asks))
	for _, a := range asks {
		if answered[a.AskID] {
			continue
		}
		pending = append(pending, a)
	}
	return pending, nil
}

// renderPendingClarifications writes a one-line-per-ask summary for
// the --list mode. Output stays plain so it pipes well into other
// tools (`gil clarify <id> --list | grep urgency:high`).
func renderPendingClarifications(out io.Writer, pendings []pendingClarification) error {
	if len(pendings) == 0 {
		fmt.Fprintln(out, "no pending clarifications")
		return nil
	}
	// Stable order: oldest first (already loaded that way).
	sort.SliceStable(pendings, func(i, j int) bool {
		return pendings[i].AskID < pendings[j].AskID
	})
	for _, p := range pendings {
		urg := p.Urgency
		if urg == "" {
			urg = "normal"
		}
		fmt.Fprintf(out, "ask=%s urgency=%s suggestions=%d\n", p.AskID, urg, len(p.Suggestions))
		fmt.Fprintf(out, "  Q: %s\n", p.Question)
		if p.Context != "" {
			fmt.Fprintf(out, "  context: %s\n", p.Context)
		}
		for i, s := range p.Suggestions {
			fmt.Fprintf(out, "  [%d] %s\n", i+1, s)
		}
	}
	return nil
}

// renderClarifyPrompt is the human surface shown when the user runs
// `gil clarify <id>` with no answer arg + no --pick. It mirrors the
// TUI modal's content so a user who switches surfaces sees the same
// thing. The prompt ends with "answer> " on a separate line so
// readLine's stdin scan stays unambiguous.
func renderClarifyPrompt(out io.Writer, p pendingClarification) error {
	fmt.Fprintf(out, "ask %s (urgency=%s)\n", p.AskID, defaultUrgency(p.Urgency))
	fmt.Fprintf(out, "Q: %s\n", p.Question)
	if p.Context != "" {
		fmt.Fprintf(out, "context: %s\n", p.Context)
	}
	for i, s := range p.Suggestions {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, s)
	}
	fmt.Fprint(out, "answer> ")
	return nil
}

func defaultUrgency(u string) string {
	if u == "" {
		return "normal"
	}
	return u
}

// readClarifyAnswer reads a single line from r without stripping
// the trailing newline (the caller does that). Used so we can swap
// stdin in tests without spinning up a real terminal. Distinct
// helper rather than reusing auth.go's readLine to keep this file
// self-contained for the e2e suite.
func readClarifyAnswer(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	return br.ReadString('\n')
}

// pickSuggestionByPrefix is a tiny convenience for tests: takes a
// "[2] foo" style line and returns "foo". Not used by the live CLI.
func pickSuggestionByPrefix(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return line
	}
	idx := strings.Index(line, "]")
	if idx < 0 || idx+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[idx+1:])
}

