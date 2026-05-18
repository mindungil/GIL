# 2026-05-17 — Real-LLM dogfood: Roman numeral converter

**P46 of the autonomy arc.** First end-to-end real-LLM dogfood since
2026-04-27 — validates the P34-P45 base under qwen3.6-27b on a real
deliverable, not a unit-test stub.

## Setup

- Model: `vllm/qwen3.6-27b` at OSLab
- Working dir: `/tmp/p46-dogfood/` (isolated; no gil-repo contact)
- Daemon: gild built from `9938b72` (P36-P45 bundle live)
- Wall budget: `timeout 900` (15 min)
- Input: single chat prompt (see [prompt below](#prompt))

## Result

**Success.** 4 min 23 sec wall (23:34:05 → 23:38:28). Tests all pass.

```
=== RUN   TestRoundTrip/3999
--- PASS: TestRoundTrip (0.00s)
    --- PASS: TestRoundTrip/1 (0.00s)
    ...
    --- PASS: TestRoundTrip/3999 (0.00s)
PASS
ok  	roman	0.004s
```

The agent delivered exactly what was asked plus a bonus CLI it
volunteered (`cmd/roman/main.go`).

## What the agent did, in order

1. **Tried to spawn subagents WITHOUT freezing a spec first** —
   `spawn_agent requires a frozen parent spec. Call freeze_spec first.`
   came back as IsError. Adaptive: agent immediately called
   `freeze_spec` with the goal/verification, then retried spawn.
2. **Spawned 3 parallel subagents at depth=1**:
   - `pkg` — write the Roman conversion functions
   - `tests` — write the test file
   - `cli` — write the CLI wrapper (volunteered; not requested)
3. **Waited via wait_agent (60s each, fired in parallel)**.
4. **Self-audited the test file** after children completed. Compared
   the existing test cases against the required cases in the prompt;
   identified gaps (49, 444, 999 missing in ToRoman; XLIX, CDXLIV,
   CMXCIX missing in FromRoman).
5. **Rewrote `roman_test.go`** with the corrections plus a targeted
   round-trip table for the specified values.
6. **Called `verify`** with `cd roman && go test ./... -v -count=1`.
   Verify-loop reported PASS, step transitioned to verified.
7. **Ended the turn cleanly** (no further tool calls).

## What this validates from the P34-P45 base

| phase | observed in this run |
|---|---|
| P34 chat persist | Implicit — daemon stayed up, conversation held |
| P35 compaction | Not triggered (single-prompt task, history short) |
| P36-P38 reaping | Not exercised (daemon didn't crash) |
| P39 chat stuck detection | Not triggered (no repeated identical calls) |
| **P40 subagent depth** | depth=1 used; depth=2 NOT used by this task |
| P43 retry resilience | Not exercised (vllm didn't 429) |
| Verify-loop discipline (C1) | **Exercised** — agent honored verify-gated workflow |
| Self-audit | **Exercised** — agent compared deliverable against requirements unprompted |
| plan_steps + verify integration | **Exercised** — step transitioned to verified |
| spawn_agent / wait_agent / freeze_spec adaptive flow | **Exercised** — agent recovered from "spec required" error |

## What this does NOT validate

- **P40 depth=2 organic use.** qwen on this task used depth=1
  (parallel children only). The infrastructure works (unit tests pin
  it), but real-LLM organic use of depth=2 needs a more complex task
  that naturally splits into a hierarchy. Followup task candidate:
  multi-module project with per-module test subagent + per-file
  edit grandchildren.
- **Long-run (>15 min) behavior.** 4:23 is fast — qwen converged
  quickly on a well-scoped task. A longer task with multi-stage
  iteration (refactor + migrate + verify) would stress P35
  compaction and P38 heartbeat.
- **P36/P37/P38 failure recovery.** Daemon stayed up the whole time.
  Need a dedicated test that kills gild mid-run.
- **MCP P47 IsAlive.** No MCP servers in spec; not exercised.
- **Failure injection.** No injected errors; only the natural
  freeze_spec misordering caught.

## Direct quote from the agent's self-audit

> Let me compare the required cases against what exists:
> **ToRoman required:** 1, 4, 9, 40, 49, 90, 400, 444, 999, 3999
> **ToRoman existing:** 1, 4, 9, 40, 90, 400, 3999
> **Missing:** 49→"XLIX", 444→"CDXLIV", 999→"CMXCIX"

This is exactly the kind of behavior the autonomous coding harness
goal hopes for — the agent treating its own output as something to
verify against the spec, not as "done because I emitted a tool
call."

## Followups identified

1. **Engineer a depth-2-pushing task** for the next dogfood.
   Candidate: "implement a small expression interpreter with
   separate lexer/parser/evaluator packages, each with tests; the
   CLI ties them together. All `go test ./...` must pass."
   Natural shape: coordinator → 3 subcoordinators → 6 workers
   (code + test per package).

2. **Engineer a failure-recovery dogfood**: kill gild at iteration
   N of a multi-stage task with `Risk.ResumeOnRestart=true`,
   confirm the agent picks up cleanly.

3. **Long-run dogfood (>1 hr)**: pick a task with real iteration
   depth (e.g. "port the bench harness to Go") and run it under a
   heartbeat-stressed daemon.

## Prompt

Used verbatim:

```
Implement a Roman numeral converter in Go.

Requirements:
- File `roman.go` with package `dogfood`
- Function `ToRoman(n int) (string, error)` — converts int (1..3999) to Roman numeral; errors on out-of-range
- Function `FromRoman(s string) (int, error)` — parses Roman numeral; errors on invalid input
- File `roman_test.go` with table-driven tests covering: 1, 4, 9, 40, 49, 90, 400, 444, 999, 3999, "I", "IV", "IX", "XL", "XLIX", "XC", "CD", "CDXLIV", "CMXCIX", "MMMCMXCIX". Include round-trip tests (ToRoman → FromRoman should reconstruct the original).
- Negative tests: ToRoman(0), ToRoman(-1), ToRoman(4000), FromRoman(""), FromRoman("IIII"), FromRoman("ABC") all return errors.

Verify with `go test ./...`. Show the final test output.
```

The agent re-interpreted "package `dogfood`" as "package `roman`
under a subdirectory" — minor deviation that improved the layout.
All other requirements met exactly.
