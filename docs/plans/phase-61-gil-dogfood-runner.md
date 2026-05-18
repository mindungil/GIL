# Phase 61 — `gil dogfood` multi-turn runner

## Why

P57 chess engine dogfood (1h 47min, 21+ files, perft Kiwipete fail)
made the gap explicit: **single-shot heredoc input prevents the
agent from recovering when it hits verify_missing**. The agent was
actively debugging the Kiwipete failure when stdin EOF cut it off.
A 9-hour budget is worthless if the loop can only consume one user
turn before falling off the cliff.

## Goal

A new `gil dogfood` CLI command that wraps `gil chat`'s Prompt RPC
in a multi-turn loop. Auto-injects recovery prompts when the agent
hits common failure states. Bounded by either turn count or wall
clock. Captures a structured JSONL trace. Optional outcome
assertions.

## Design

### CLI shape

```
gil dogfood <prompt-file>
  --working-dir <dir>          (required)
  --provider vllm
  --model qwen3.6-27b
  --max-turns 50               (default 20)
  --max-wall 9h                (default 1h)
  --trace /tmp/dogfood-N.jsonl (default stdout)
  --assert "go test ./..."     (run after; non-zero exit fails dogfood)
```

### Turn loop

```
session_id := ""
for turn := 1; turn <= maxTurns && wall < maxWall; turn++ {
    prompt := initial-prompt-from-file (turn 1) OR recovery-prompt (turn >1)
    stream := sdk.Prompt(ctx, {SessionID: session_id, Text: prompt})
    drain stream, capture: text + tool_calls + tool_results + done
    if turn == 1 { session_id = stream.SessionAllocated }
    classify last Done.StopReason:
      "end_turn"        — agent completed → done
      "verify_missing"  — agent hit C1 backstop → recover
      "error"           — hard error → fail
      other             — log + continue
    if recover {
        prompt = recoveryPromptFor(stopReason, lastVerifyTail)
        continue
    }
    if done {
        break
    }
}
run assertions; report PASS/FAIL with structured summary
```

### Recovery prompt catalog

```
verify_missing → "The previous turn hit verify_missing. Look at the
verify output below and fix the failure, then call verify again.
DO NOT ask me anything — execute the fix.

Last verify output:
<tail>"

(turn-cap hit without verify_missing) → "Continue executing your plan.
DO NOT ask me anything — make reasonable choices and proceed."

(empty response) → "You returned no content. Continue the task; if
you're done, summarize what was built and call verify one final time."
```

### Structured trace JSONL

One record per turn:
```json
{
  "turn": 1,
  "ts": "2026-05-18T11:00:00Z",
  "prompt": "...truncated...",
  "stop_reason": "verify_missing",
  "tool_calls": [{"name":"write_file","input":"..."}, ...],
  "verify_outputs": [{"command":"...","exit":0,"tail":"..."}, ...],
  "tokens_in": 4231,
  "tokens_out": 982,
  "cost_usd": 0.0123,
  "wall_ms": 11340
}
```

End-of-run summary record:
```json
{
  "summary": true,
  "session_id": "01...",
  "turns": 8,
  "total_wall_ms": 1023400,
  "total_cost_usd": 0.0421,
  "final_stop": "end_turn",
  "assertion": {"command":"go test ./...","exit":0,"passed":true}
}
```

### Detection — when is the agent "done"?

Termination conditions (in order):
1. `Done.StopReason == "end_turn"` AND no tool calls this turn → agent
   declared completion in conversational form. Done.
2. `--max-turns` reached → budget exhausted (not done).
3. `--max-wall` reached → budget exhausted.
4. `Done.StopReason == "error"` → hard failure.
5. Daemon disappeared → exit with daemon-gone error.

### Assertions

`--assert "<shell cmd>"` runs the command in the working directory
after the turn loop ends. Exit code 0 → assertion passed; non-zero
→ failed (printed with stderr tail). Multiple --assert flags allowed.

## Non-goals (v1)

- LLM-quality scoring (e.g. did the agent take an efficient path).
  v1 just records the trace; analysis is offline.
- VCR-style replay. Mock provider already covers deterministic
  unit tests; dogfood is for live-LLM observation.
- Distributed runs (cluster, multiple sessions in parallel).
- FaultInjector composition (separate feature; can be a `--inject`
  flag wired into the provider factory in a followup).

## Acceptance criteria

1. Single-prompt-file → multi-turn execution against vllm/qwen3.6-27b
   until end_turn or budget exhaustion.
2. verify_missing automatically triggers a recovery prompt
   with the last verify output's tail in the recovery text.
3. JSONL trace written with one row per turn + final summary.
4. `--assert` runs the command post-loop; failure surfaces in summary.
5. Daemon-gone exits cleanly (P53 reuse — match the error string
   pattern from the SDK and exit non-zero).
6. Unit tests with a mock provider exercising: clean completion,
   verify_missing recovery, max-turns hit, assertion failure.

## Result (TBD)

To be filled after implementation + first live dogfood using the
runner. Initial smoke test: re-run the P57 chess engine task with
auto-recovery and see if it converges on Kiwipete.
