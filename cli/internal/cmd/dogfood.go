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

	gilv1 "github.com/mindungil/gil/proto/gen/gil/v1"
	"github.com/mindungil/gil/sdk"
)

func dogfoodCmd() *cobra.Command {
	var (
		socket         string
		workingDir     string
		provider       string
		model          string
		maxTurns       int
		maxWall        time.Duration
		tracePath      string
		assertCmds     []string
		temperature    float64
		adversaryModel string
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
			progressOut := out
			summaryOut := out
			var traceW io.Writer = out
			if tracePath != "" {
				f, err := os.Create(tracePath)
				if err != nil {
					return fmt.Errorf("create trace: %w", err)
				}
				defer f.Close()
				traceW = f
			} else {
				progressOut = cmd.ErrOrStderr()
				summaryOut = progressOut
			}

			runner := &dogfoodRunner{
				cli:            cli,
				workingDir:     absWD,
				provider:       provider,
				model:          model,
				maxTurns:       maxTurns,
				maxWall:        maxWall,
				initial:        string(initialBytes),
				assertCmds:     assertCmds,
				temperature:    temperature,
				adversaryModel: adversaryModel,
				trace:          json.NewEncoder(traceW),
				progress:       progressOut,
			}
			result, err := runner.Run(ctx)
			if err != nil {
				return err
			}
			// Run assertions in the working dir.
			result.runAssertions(ctx, absWD, assertCmds)
			// Final summary line stays human-readable. When trace
			// defaults to stdout, send human progress to stderr so stdout
			// remains valid JSONL.
			fmt.Fprintln(summaryOut, result.summaryLine())
			// Emit the structured summary as the last JSONL record.
			_ = runner.trace.Encode(result.summaryRecord())
			if !result.success() {
				return fmt.Errorf("dogfood %s", verdictFromReason(result))
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
	c.Flags().Float64Var(&temperature, "temperature", 0, "sampling temperature override (0 = use daemon default 0.7; try 0.2–0.3 for autonomous-coding tasks per Finding #6)")
	c.Flags().StringVar(&adversaryModel, "adversary-model", "", "model id to consult when the chat Detector emits a stuck signal (empty disables adversary; AltToolOrder / ModelEscalate still fire)")
	return c
}

// dogfoodRunner holds the per-invocation state. One Run() per command.
type dogfoodRunner struct {
	cli            *sdk.Client
	workingDir     string
	provider       string
	model          string
	maxTurns       int
	maxWall        time.Duration
	initial        string
	assertCmds     []string
	temperature    float64
	adversaryModel string

	trace    *json.Encoder
	progress io.Writer

	sessionID   string
	startedAt   time.Time
	totalCost   float64
	totalInTok  int64
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
//
// P63b assertion-driven recovery: when the agent declares end_turn
// but the user's --assert commands fail, the runner re-engages with
// a recovery prompt containing the failing assertion's output tail.
// This catches the "agent thinks it's done but the load-bearing
// external check disagrees" pattern observed in P63 chess v3.
func (r *dogfoodRunner) Run(ctx context.Context) (*dogfoodResult, error) {
	r.startedAt = time.Now()
	deadline := r.startedAt.Add(r.maxWall)
	result := &dogfoodResult{}

	// P63c: track consecutive empty end_turns AFTER an assertion
	// failure has triggered re-engagement. v4 chess data showed the
	// agent can fall into "I'm done" → "you're not done" → "ok still
	// done" loops where the recovery prompt fizzles and 15+ turns are
	// wasted on identical 0-tool responses. After 3 such consecutive
	// empty re-engagements, abandon — the agent is not making progress
	// and more turns won't help.
	const maxStalledRecoveries = 3
	consecutiveStalled := 0

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
		fmt.Fprintf(r.progress, "[dogfood] turn %d stop=%s wall=%dms cost=$%.4f\n",
			turn, turnRec.StopReason, turnRec.WallMs, turnRec.CostUSD)

		nextPrompt = recoveryPromptFor(turnRec)

		// P65 — accept end_turn early when the user's assertions are
		// green, even if the last turn had tool calls. Without this,
		// productive agents who end each turn with read-only verify
		// calls (go test, ls) trap in "death by verification": the
		// runner sees ToolCallCount > 0 → injects "Continue executing"
		// → agent re-verifies → loop burns max_turns despite asserts
		// being green throughout. Observed at task09 mini-compiler:
		// 25 turns INCOMPLETE despite `go test ./...` passing
		// continuously. The assertion is the authoritative "done"
		// oracle for the user; if it's green, accept the agent's
		// end_turn.
		if turnRec.StopReason == "end_turn" && turnRec.ToolCallCount > 0 && len(r.assertCmds) > 0 {
			if r.runAssertCheck(ctx) == "" {
				result.Reason = "end_turn"
				break
			}
			// Assertion failed and the agent did real work — reset
			// the stall counter and let recoveryPromptFor's
			// "Continue executing your plan" prompt re-engage.
			consecutiveStalled = 0
		}

		if nextPrompt == "" {
			// Agent says it's done. P63b: before believing it, run the
			// user's assertions. If any fail, inject a recovery prompt
			// with the failing tail and continue.
			if len(r.assertCmds) > 0 {
				failedTail := r.runAssertCheck(ctx)
				if failedTail != "" {
					// P63c stall detection: if THIS turn produced no
					// tool calls AND we already re-engaged on a prior
					// assertion failure, count it as a stalled
					// recovery. Three stalls in a row → abandon.
					if turnRec.ToolCallCount == 0 && consecutiveStalled > 0 {
						consecutiveStalled++
					} else if turnRec.ToolCallCount == 0 {
						consecutiveStalled = 1 // first stall after first assertion fail
					} else {
						consecutiveStalled = 0 // real work happened; reset
					}
					if consecutiveStalled >= maxStalledRecoveries {
						result.Reason = "stalled"
						fmt.Fprintf(r.progress, "[dogfood] turn %d ABANDONED — %d consecutive empty re-engagements; agent not making progress\n",
							turn, consecutiveStalled)
						break
					}
					nextPrompt = assertionRecoveryPrompt(failedTail)
					fmt.Fprintf(r.progress, "[dogfood] turn %d agent declared done BUT assertion failed — re-engaging (stalled=%d)\n", turn, consecutiveStalled)
					continue
				}
			}
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

// runAssertCheck runs each --assert command and returns the
// combined-output tail of the FIRST failing command. Returns "" when
// all assertions pass. Best-effort: errors spawning a command count
// as failure. Used by Run() to decide whether to accept end_turn
// or re-engage the agent.
func (r *dogfoodRunner) runAssertCheck(ctx context.Context) string {
	for _, c := range r.assertCmds {
		shell := exec.CommandContext(ctx, "bash", "-c", c)
		shell.Dir = r.workingDir
		out, err := shell.CombinedOutput()
		exit := shell.ProcessState.ExitCode()
		if err != nil && exit == 0 {
			exit = 1
		}
		if exit != 0 {
			return fmt.Sprintf("Command: %s\nExit: %d\nOutput tail:\n%s", c, exit, tailRunes(string(out), 1200))
		}
	}
	return ""
}

// assertionRecoveryPrompt builds the recovery prompt body for the
// "agent thinks done but assertion fails" case.
func assertionRecoveryPrompt(failedTail string) string {
	return "You declared the task complete (end_turn with no tool calls), " +
		"but the user's verification command FAILS. The task is NOT done. " +
		"Read the failure below carefully, find the actual root cause, fix " +
		"it, and re-run the verification before declaring done again. DO NOT " +
		"ask me anything — investigate and fix.\n\n" +
		"Failing verification output:\n" + failedTail
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
		SessionID:      r.sessionID,
		Text:           prompt,
		Provider:       r.provider,
		Model:          r.model,
		WorkingDir:     r.workingDir,
		Temperature:    r.temperature,
		AdversaryModel: r.adversaryModel,
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
	case "tool_timeout_loop":
		// P66 fired — agent's tools are hanging (deadlock/infinite
		// test loop). Recovery prompts won't help: the same tool
		// will hang again. Treat as hard stop; the post-loop
		// assertion check will record the actual workspace state
		// and the runner exits with the verdict.
		return ""
	case "tool_error_loop":
		// P69 fired — the agent locked onto a malformed tool call that
		// kept returning the same error (e.g. write_file with empty
		// args). Unlike a timeout loop, a fresh turn with explicit
		// guidance usually breaks the fixation. If it loops again the
		// breaker re-fires and max-turns / max-wall bound the total.
		return "Your previous turn repeated the SAME failing tool call several times and was aborted. STOP repeating that call. Re-read the tool's required arguments; if a tool keeps rejecting your input, switch approach — e.g. write the file with a run_bash heredoc, or split one large write into smaller edits. DO NOT ask me anything — adapt and proceed."
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
	verdict := verdictFromReason(r)
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
	case "max_turns", "max_wall", "stalled":
		return "INCOMPLETE"
	default:
		return "ERROR"
	}
}

func (r *dogfoodResult) success() bool {
	return verdictFromReason(r) == "PASS"
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
