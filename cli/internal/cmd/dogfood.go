// Package cmd — `gil dogfood` is the multi-turn dogfood runner.
// Wraps `gil chat`'s Prompt RPC in a loop that auto-injects
// recovery prompts when the agent hits common failure states
// (verify_missing, empty turn, etc.) so a single bash invocation
// can actually use a multi-hour budget instead of falling off the
// cliff at first turn cap.
//
// Built after P57 chess dogfood (1h 47min) made the gap explicit:
// the agent was actively debugging Kiwipete when stdin EOF cut it
// off. Heredoc single-shot input doesn't model how a real
// autonomous-coding session needs to recover.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mindungil/gil/sdk"
	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
)

func dogfoodCmd() *cobra.Command {
	var (
		socket     string
		workingDir string
		provider   string
		model      string
		maxTurns   int
		maxWall    time.Duration
		tracePath  string
		assertCmds []string
	)
	c := &cobra.Command{
		Use:   "dogfood <prompt-file>",
		Short: "Multi-turn dogfood runner with auto-recovery (P61)",
		Long: `Run a single prompt file through gil chat in a loop that
auto-injects recovery prompts when the agent hits common failure
states. Bounded by --max-turns and --max-wall. Captures a
structured JSONL trace. Optional --assert runs a shell command
after the loop and fails the dogfood on non-zero exit.

The single-shot ` + "`gil chat < prompt.txt`" + ` invocation hits a wall
when the agent's first turn fails verify_missing or otherwise
needs another iteration — heredoc EOF means no follow-up.
dogfood injects a recovery prompt (with the last verify output in
the prompt body) and continues, so a 9-hour budget actually
funds 9 hours of work.

Recovery prompt catalog:
  - verify_missing → "fix the failure and re-run verify"
  - end_turn with no tool calls (turn cap on incomplete task) →
    "continue executing"
  - empty stream → "you returned no content; continue or summarize"

Termination:
  - end_turn with no tool calls AND short response → assume done
  - --max-turns reached
  - --max-wall exceeded
  - error stop_reason
  - daemon disappears`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			promptFile := args[0]
			initialBytes, err := os.ReadFile(promptFile)
			if err != nil {
				return fmt.Errorf("read prompt file: %w", err)
			}
			if workingDir == "" {
				return fmt.Errorf("--working-dir is required")
			}
			absWD, err := filepath.Abs(workingDir)
			if err != nil {
				return fmt.Errorf("abs working dir: %w", err)
			}
			if info, err := os.Stat(absWD); err != nil || !info.IsDir() {
				return fmt.Errorf("--working-dir %q is not a directory", absWD)
			}

			if err := ensureDaemon(socket, defaultBase()); err != nil {
				return err
			}
			cli, err := sdk.Dial(socket)
			if err != nil {
				return fmt.Errorf("dial: %w", err)
			}
			defer cli.Close()

			out := cmd.OutOrStdout()
			var traceW io.Writer = out
			if tracePath != "" {
				f, err := os.Create(tracePath)
				if err != nil {
					return fmt.Errorf("create trace: %w", err)
				}
				defer f.Close()
				traceW = f
			}

			runner := &dogfoodRunner{
				cli:        cli,
				workingDir: absWD,
				provider:   provider,
				model:      model,
				maxTurns:   maxTurns,
				maxWall:    maxWall,
				initial:    string(initialBytes),
				trace:      json.NewEncoder(traceW),
				stdout:     out,
			}
			result, err := runner.Run(ctx)
			if err != nil {
				return err
			}
			// Run assertions in the working dir.
			result.runAssertions(ctx, absWD, assertCmds)
			// Final summary line on stdout (always — even when trace is
			// in a file, the user wants the verdict).
			fmt.Fprintln(out, result.summaryLine())
			// Emit the structured summary as the last JSONL record.
			_ = runner.trace.Encode(result.summaryRecord())
			if !result.allAssertionsPassed() {
				return fmt.Errorf("dogfood assertion failed")
			}
			return nil
		},
	}
	c.Flags().StringVar(&socket, "socket", defaultSocket(), "gild UDS socket path")
	c.Flags().StringVar(&workingDir, "working-dir", "", "project working directory (required)")
	c.Flags().StringVar(&provider, "provider", "", "LLM provider (empty → workspace config)")
	c.Flags().StringVar(&model, "model", "", "LLM model id (empty → provider default)")
	c.Flags().IntVar(&maxTurns, "max-turns", 20, "max recovery turns before giving up")
	c.Flags().DurationVar(&maxWall, "max-wall", time.Hour, "max wall-clock budget (e.g. 9h, 30m)")
	c.Flags().StringVar(&tracePath, "trace", "", "path for JSONL trace (default: stdout)")
	c.Flags().StringArrayVar(&assertCmds, "assert", nil, "shell command run after the loop; failure marks dogfood as failed (may be repeated)")
	return c
}

// dogfoodRunner holds the per-invocation state. One Run() per command.
type dogfoodRunner struct {
	cli        *sdk.Client
	workingDir string
	provider   string
	model      string
	maxTurns   int
	maxWall    time.Duration
	initial    string

	trace  *json.Encoder
	stdout io.Writer

	sessionID  string
	startedAt  time.Time
	totalCost  float64
	totalInTok int64
	totalOutTok int64
}

// dogfoodResult is the value returned from Run(). assertions are
// filled in by runAssertions.
type dogfoodResult struct {
	SessionID    string         `json:"session_id"`
	Turns        int            `json:"turns"`
	TotalWallMs  int64          `json:"total_wall_ms"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	TotalInTok   int64          `json:"total_in_tok"`
	TotalOutTok  int64          `json:"total_out_tok"`
	FinalStop    string         `json:"final_stop"`
	Reason       string         `json:"reason"` // "end_turn" / "max_turns" / "max_wall" / "error" / "daemon_gone"
	Assertions   []assertResult `json:"assertions,omitempty"`
}

type assertResult struct {
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Passed  bool   `json:"passed"`
	Tail    string `json:"tail,omitempty"`
}

// Run executes the multi-turn loop and returns the result. Errors
// from the loop (daemon-gone, unrecoverable) surface as Go errors;
// budget-exhaustion or hard-error stop_reasons go through the result
// without error so the caller can still report the structured trace.
func (r *dogfoodRunner) Run(ctx context.Context) (*dogfoodResult, error) {
	r.startedAt = time.Now()
	deadline := r.startedAt.Add(r.maxWall)
	result := &dogfoodResult{}

	nextPrompt := r.initial
	for turn := 1; turn <= r.maxTurns; turn++ {
		if time.Now().After(deadline) {
			result.Reason = "max_wall"
			break
		}
		t0 := time.Now()
		turnRec, err := r.runOneTurn(ctx, turn, nextPrompt)
		if err != nil {
			if isDaemonGoneClient(err) {
				result.Reason = "daemon_gone"
				return result, err
			}
			result.Reason = "error"
			return result, err
		}
		result.Turns = turn
		result.FinalStop = turnRec.StopReason
		r.totalCost += turnRec.CostUSD
		r.totalInTok += turnRec.TokensIn
		r.totalOutTok += turnRec.TokensOut
		turnRec.WallMs = time.Since(t0).Milliseconds()
		_ = r.trace.Encode(turnRec)
		fmt.Fprintf(r.stdout, "[dogfood] turn %d stop=%s wall=%dms cost=$%.4f\n",
			turn, turnRec.StopReason, turnRec.WallMs, turnRec.CostUSD)

		nextPrompt = recoveryPromptFor(turnRec)
		if nextPrompt == "" {
			// No recovery needed → agent is done.
			result.Reason = "end_turn"
			break
		}
	}
	if result.Reason == "" {
		result.Reason = "max_turns"
	}
	result.SessionID = r.sessionID
	result.TotalWallMs = time.Since(r.startedAt).Milliseconds()
	result.TotalCostUSD = r.totalCost
	result.TotalInTok = r.totalInTok
	result.TotalOutTok = r.totalOutTok
	return result, nil
}

// turnRecord is one row in the JSONL trace.
type turnRecord struct {
	Turn          int       `json:"turn"`
	Ts            time.Time `json:"ts"`
	PromptHead    string    `json:"prompt_head"` // first 200 chars
	StopReason    string    `json:"stop_reason"`
	ToolCallCount int       `json:"tool_call_count"`
	ResponseTail  string    `json:"response_tail"` // last 400 chars
	VerifyTail    string    `json:"verify_tail,omitempty"`
	TokensIn      int64     `json:"tokens_in"`
	TokensOut     int64     `json:"tokens_out"`
	CostUSD       float64   `json:"cost_usd"`
	WallMs        int64     `json:"wall_ms"`
}

func (r *dogfoodRunner) runOneTurn(ctx context.Context, turn int, prompt string) (*turnRecord, error) {
	rec := &turnRecord{
		Turn:       turn,
		Ts:         time.Now().UTC(),
		PromptHead: head(prompt, 200),
	}
	stream, err := r.cli.Prompt(ctx, sdk.PromptOptions{
		SessionID:  r.sessionID,
		Text:       prompt,
		Provider:   r.provider,
		Model:      r.model,
		WorkingDir: r.workingDir,
	})
	if err != nil {
		return rec, fmt.Errorf("prompt RPC: %w", err)
	}
	var responseChunks []string
	var lastVerifyTail string
	for {
		part, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// P61 v2 fix: the daemon sends a Done part (with
			// StopReason="verify_missing") AND THEN errors the stream
			// with FailedPrecondition. By the time we see the error
			// here, the Done was already processed and rec.StopReason
			// is set — so the error is just the trailing wire signal,
			// not a fatal condition. Continue normally; the recovery
			// loop sees verify_missing and injects a fix prompt.
			//
			// If rec.StopReason is empty but the error string mentions
			// verify_missing, synthesize the stop reason so the loop
			// recovers correctly even when wire ordering differs.
			if rec.StopReason != "" {
				break
			}
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "verify_missing") || strings.Contains(lower, "code-changing tool call") {
				rec.StopReason = "verify_missing"
				// Try to pull the verify tail out of the err message —
				// the daemon includes "Last verify output: <tail>" in
				// the message (P50).
				if idx := strings.Index(err.Error(), "Last verify output:"); idx >= 0 {
					lastVerifyTail = strings.TrimSpace(err.Error()[idx+len("Last verify output:"):])
				}
				break
			}
			return rec, fmt.Errorf("stream recv: %w", err)
		}
		if alloc := part.GetSessionAllocated(); alloc != nil {
			if r.sessionID == "" {
				r.sessionID = alloc.GetSessionId()
			}
		}
		if td := part.GetText(); td != nil {
			responseChunks = append(responseChunks, td.GetContent())
		}
		if tc := part.GetToolCall(); tc != nil {
			rec.ToolCallCount++
		}
		if tr := part.GetToolResult(); tr != nil {
			// Capture the most-recent verify output's tail so the
			// recovery prompt can echo it back to the agent.
			if isVerifyResult(tr) {
				lastVerifyTail = tailLines(tr.GetContent(), 600)
			}
		}
		if m := part.GetMetrics(); m != nil {
			rec.TokensIn = m.GetTokensIn()
			rec.TokensOut = m.GetTokensOut()
			rec.CostUSD = m.GetCostUsd()
		}
		if done := part.GetDone(); done != nil {
			rec.StopReason = done.GetStopReason()
		}
	}
	rec.ResponseTail = tailRunes(strings.Join(responseChunks, ""), 400)
	rec.VerifyTail = lastVerifyTail
	return rec, nil
}

// isVerifyResult heuristic: a tool_result whose Content looks like the
// verify tool's output. The chat path doesn't expose the tool name on
// the proto ToolResultPart (only the call_id), so we sniff for the
// canonical "[PASS]" / "[FAIL]" prefix the verify tool emits.
func isVerifyResult(tr *gilv1.ToolResultPart) bool {
	c := tr.GetContent()
	return strings.HasPrefix(c, "[PASS]") || strings.HasPrefix(c, "[FAIL]")
}

// recoveryPromptFor decides whether the loop should continue and, if
// so, what prompt to inject. Returns "" when the agent is done (and
// the loop should exit).
func recoveryPromptFor(rec *turnRecord) string {
	switch rec.StopReason {
	case "end_turn":
		// Agent declared completion. We treat end_turn + no tool
		// calls in the LAST turn as "done." If tool calls fired in
		// this turn, the agent did real work — keep going so it can
		// either summarize or continue.
		if rec.ToolCallCount == 0 {
			return ""
		}
		return "Continue executing your plan. DO NOT ask questions — if you have everything you need to finish, do the final verify and summarize. Otherwise continue with the next step."
	case "verify_missing":
		// C1 backstop fired. Reuse the verify tail captured by the
		// turn so the agent has concrete context.
		body := "The previous turn hit verify_missing — code-changing tools were called but verify did not report success. Fix the underlying failure and re-run verify.\n\nDO NOT ask me anything — execute the fix directly."
		if rec.VerifyTail != "" {
			body += "\n\nLast verify output (tail):\n" + rec.VerifyTail
		}
		return body
	case "error":
		// Hard error — give up.
		return ""
	default:
		// Unknown stop reason — assume continue, but don't loop
		// infinitely if the agent keeps producing nothing.
		return "Continue executing your plan. DO NOT ask me anything — make reasonable choices and proceed."
	}
}

// runAssertions executes each --assert command in workingDir.
// Records the exit + last 200 chars of combined output on the
// result.Assertions slice. Best-effort: even if one command fails
// to spawn, the rest run.
func (r *dogfoodResult) runAssertions(ctx context.Context, workingDir string, cmds []string) {
	for _, c := range cmds {
		ar := assertResult{Command: c}
		shell := exec.CommandContext(ctx, "bash", "-c", c)
		shell.Dir = workingDir
		out, err := shell.CombinedOutput()
		ar.Exit = shell.ProcessState.ExitCode()
		if err != nil && ar.Exit == 0 {
			ar.Exit = 1
		}
		ar.Passed = ar.Exit == 0
		ar.Tail = tailRunes(string(out), 200)
		r.Assertions = append(r.Assertions, ar)
	}
}

func (r *dogfoodResult) allAssertionsPassed() bool {
	for _, a := range r.Assertions {
		if !a.Passed {
			return false
		}
	}
	return true
}

func (r *dogfoodResult) summaryLine() string {
	verdict := "PASS"
	if !r.allAssertionsPassed() {
		verdict = "FAIL"
	} else if r.Reason == "max_turns" || r.Reason == "max_wall" {
		verdict = "INCOMPLETE"
	} else if r.Reason == "error" || r.Reason == "daemon_gone" {
		verdict = "ERROR"
	}
	return fmt.Sprintf("[dogfood] %s — turns=%d wall=%s cost=$%.4f session=%s reason=%s",
		verdict, r.Turns, time.Duration(r.TotalWallMs)*time.Millisecond,
		r.TotalCostUSD, r.SessionID, r.Reason)
}

func (r *dogfoodResult) summaryRecord() map[string]any {
	return map[string]any{
		"summary":        true,
		"session_id":     r.SessionID,
		"turns":          r.Turns,
		"total_wall_ms":  r.TotalWallMs,
		"total_cost_usd": r.TotalCostUSD,
		"total_in_tok":   r.TotalInTok,
		"total_out_tok":  r.TotalOutTok,
		"final_stop":     r.FinalStop,
		"reason":         r.Reason,
		"assertions":     r.Assertions,
		"verdict":        verdictFromReason(r),
	}
}

func verdictFromReason(r *dogfoodResult) string {
	if !r.allAssertionsPassed() {
		return "FAIL"
	}
	switch r.Reason {
	case "end_turn":
		return "PASS"
	case "max_turns", "max_wall":
		return "INCOMPLETE"
	default:
		return "ERROR"
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tailRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func tailLines(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Find a clean line boundary near the tail-start.
	cut := len(s) - n
	if idx := strings.IndexByte(s[cut:], '\n'); idx >= 0 {
		return s[cut+idx+1:]
	}
	return s[cut:]
}

// isDaemonGoneClient mirrors the chat REPL's isDaemonGoneErr but
// scoped to this command so the cli/internal/chat/repl import isn't
// required at the cmd layer. Matches the same error strings.
func isDaemonGoneClient(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range []string{
		"unavailable",
		"connection refused",
		"connection reset",
		"no such file or directory",
		"broken pipe",
		"transport: error while dialing",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
