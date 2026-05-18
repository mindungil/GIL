# 2026-05-17 — Real-LLM dogfood: multi-package calc (depth=2 attempt)

**P48.** Follow-up to P46 (Roman numerals success). Engineered to push
the agent toward depth=2 subagent use by giving it a 4-package
hierarchical task. **Outcome: mixed.** Documents an honest gap.

## Setup

- Model: `vllm/qwen3.6-27b`
- Working dir: `/tmp/p48-dogfood/`
- Daemon: gild from `b4747f0` (P49 cost surfacing live; P36-P47 base)
- Wall budget: `timeout 1500` (25 min)
- Task: 4 separate Go packages — lexer / parser / evaluator / cli —
  each with tests, all building under one `go test ./...`

## Result

**Build broken at turn cap.** 4 min 4 sec wall (23:41:03 → 23:45:07).

All 9 source files written. But:
- Agent set `module expr` in go.mod.
- Imports throughout used `calc/lexer`, `calc/parser`, etc.
- Mismatch → `go test ./...` fails with "package calc/lexer is not
  in std".
- Agent noticed the mismatch on the last few turns but ran into the
  C1 verify-missing gate (no successful verify after multiple
  code-changing tool calls) and the turn aborted before fixing.

```
$ cd /tmp/p48-dogfood && go test ./...
calc/cli/cli.go:4:2: package calc/evaluator is not in std
calc/cli/cli.go:5:2: package calc/lexer is not in std
calc/cli/cli.go:6:2: package calc/parser is not in std
```

## What this validates

| signal | observed |
|---|---|
| **Agent variance on simple cognitive errors** | YES — module/import mismatch is the kind of thing a human catches immediately; qwen needed 2-3 extra turns to notice. |
| **C1 verify-missing gate caught the broken state** | YES — turn aborted with `verify_missing` error, preventing the agent from declaring "done" on broken code. P32 / iter6 backstop working. |
| **maxAgentTurns cap held** | YES — turn ended at the cap (8 turns) with a real error surfaced. |

## What this does NOT validate (the original goal)

**P40 depth=2 organic use is STILL unobserved.** This task was
engineered to make hierarchical decomposition the natural shape, but
qwen did NOT call `spawn_agent` at all. It worked the whole task in
its own chat-agent context, file by file.

Possible reasons:
- qwen prefers serial work over delegation for tasks it thinks it can
  hold in one head
- The task description didn't explicitly mention "use subagents"
- The lexer/parser/evaluator dependencies don't parallelize as
  cleanly as Roman's pkg/test/cli split
- qwen's system-prompt rendering of spawn_agent is a hint, not a
  push

Followup task ideas for a depth=2-organic test:
1. Explicitly seed the prompt with "use spawn_agent to delegate each
   package to a child" — but that's not organic, that's directed.
2. Pick a task where serial work is OBVIOUSLY too much for one
   context: "refactor each of these 6 large files" → child
   coordinates "code edit" + "test update" workers per file.
3. Watch a real-world maintenance task (port a bench harness, write
   release notes from N commits) where the agent self-organizes.

## What we did learn about the agent's failure mode

The 4-package task surfaced a real-LLM weakness: **qwen3.6-27b
struggles with setup/configuration tasks (go.mod module name) where
the error is in metadata, not behavior.** It wrote all the code,
all the tests, then noticed the import mismatch only when actually
running `go test`.

For autonomous coding harness improvements this suggests:
- A `verify_setup` hint after the first write_file in a Go module
  could surface this earlier
- Or: the spec freeze step could capture module name as a slot,
  reducing the chance of drift

Not a P49 followup; documented as a finding for a future phase
considering setup-validation surfaces.

## Direct quotes from the trace

After 5 minutes of writing code, the agent finally checks:
> The module name doesn't match. Let me check what the `go.mod` says.

And then immediately hit the turn cap. With one more turn, it almost
certainly would have fixed it (`go mod edit -module calc` is one
shell call). The cap mechanism worked but the timing was unlucky.

## Honest summary

P46 was a clean win.
P48 was a mixed result that surfaced real model variance + caught
the variance via the C1 verify-missing gate. **The harness did its
job** — it didn't let the agent claim "done" on broken code. The
deliverable being broken is a model issue, not a harness issue.

The P40 depth=2 question remains open. Will try a more
demonstrative task in a future phase.
