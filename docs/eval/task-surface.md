# Task Surface Measurement

Living document. Measures **where the gil harness amplifies the underlying model
vs. where it can't**. The goal criterion under test is "drives to goal
completion" — the one criterion still unquantified.

**Harness under test**: develop @ 907c1f2 (2026-05-18). gil dogfood runner
(P61/P63b/P63c) with stall detection + assertion-driven recovery + pressure-
driven compaction.

**Model**: qwen3.6-27b via OSLab vllm (default workspace credential). Single
model for the first pass. If failures cluster by task pattern → fix harness.
If failures cluster by reasoning depth → measure ceiling with another model.

---

## Task Slate (8)

Each task: a single prompt file fed to `gil dogfood`, an assert command for
deterministic PASS, a max-wall budget. Easy → hard gradient.

| #  | Task             | Type                | Surface           | Budget  | Assert                                           |
|----|------------------|---------------------|-------------------|---------|--------------------------------------------------|
| 01 | md2html          | new code, single    | text proc         | 15m     | `go test ./...` + 3 fixture HTML files exist     |
| 02 | json-validator   | new code, single    | schema + CLI      | 20m     | `go test ./...` covering 5 schema cases          |
| 03 | bug-fix          | modification        | bounded diff      | 15m     | seed test passes + others stay green             |
| 04 | lru-cache        | algorithmic         | DS, O(1) ops      | 20m     | `go test ./...` 5 scenarios incl. eviction order |
| 05 | refactor         | multi-file, behav-preserve | integration-lite | 25m | all existing tests pass post-refactor          |
| 06 | regex-match      | DSL, state machine  | multi-pass        | 30m     | 10 (pattern,input,expected) table-driven cases   |
| 07 | chess-perft      | algorithmic, deep   | recursion + state | 60m     | perft(3) matches known counts on 2 positions     |
| 08 | http-kv          | integration         | runtime           | 30m     | 5 curl scenarios + JSON shape checks             |

PASS criteria intentionally deterministic (assert exits non-zero on fail).
Verdict per task is one of: **PASS / FAIL / INCOMPLETE / STALLED**.

- PASS: assertion green, end_turn clean
- FAIL: assertion red despite end_turn
- INCOMPLETE: max_turns or max_wall reached
- STALLED: 3 consecutive empty re-engagements detected (P63c)

---

## Result Log

Filled in as each task completes. One row per run, append-only.

| #  | Task         | Verdict | Wall    | Turns | Tokens in/out | Failure surface                |
|----|--------------|---------|---------|-------|---------------|--------------------------------|
| 01 | md2html      | PASS    | 1m27s   | 2     | 128k / 4.4k   | —                              |
| 02 | json-validator | PASS  | 3m12s   | 8     | 466k / 9.2k   | —                              |
| 03 | bug-fix      | PASS    | 19.9s   | 2     | 43k / 0.8k    | —                              |
| 04 | lru-cache    | PASS    | 52.6s   | 2     | 89k / 2.5k    | —                              |
| 05 | refactor     | PASS    | 41.5s   | 4     | 92k / 1.8k    | —                              |
| 06 | regex-match  | PASS    | 3m47s   | 13    | 699k / 10.3k  | turn count ↑↑ — DSL stress     |
| 07 | chess-perft  | PASS    | 30m1s   | 32    | 8.4M / 20.8k  | first PASS after 4× prior FAIL |
| 08 | http-kv      | PASS    | 2m23s   | 7     | 272k / 7k     | —                              |

---

## Analysis (slate 1, 2026-05-18)

**Result: 8 / 8 PASS.** Total wall ~42m, ~10M in / 56k out tokens, 70 turns
combined, $0 (vllm). The strongest dogfood data point gil has produced.

### Falsified hypotheses

- **H1** (multi-file state degrades harness) — **FALSIFIED.** Task 05 (refactor
  across 3 files) and task 08 (http handler + in-memory store + httptest)
  both PASSed in <5min and <10 turns.
- **H2** (3+ sub-objectives blocks verify-loop) — **FALSIFIED.** Chess perft
  has ~6 piece-type generators + check detection + castling + en-passant +
  legal-move filter and PASSed in 32 turns.
- **H3** (stall detection saves tokens on FAIL) — **UNTESTED** (no FAILs).
- **H4** (deterministic asserts → higher rate) — **CONSISTENT** but
  unfalsifiable without FAILs.

### Reversal of prior eval

The prior session evaluation (2026-05-18, pre-slate) concluded:

> 검증된 task 표면이 좁다 — 데이터 포인트는 6개고, 솔직히 성공은 md2html과
> gil-on-gil 두 종류뿐. chess는 model capability boundary.

This slate falsifies both claims. The task surface is wider than the prior
6 dogfood data points suggested, AND chess-perft-depth-3 is *not* the model
boundary when the harness has P63 lifted caps + P63b assertion recovery +
P63c stall detection. The first 4 chess FAILs (P57, P61, v3 cadence-driven
compaction, P63b) were harness-shaped, not model-shaped.

### Turn-count curve

Healthy stratification by task difficulty:

| Difficulty class            | Turns range | Tasks                 |
|-----------------------------|-------------|------------------------|
| Trivial (1-shot)            | 2           | md2html, bug-fix, lru |
| Moderate (multi-file)       | 4-7         | refactor, http-kv     |
| DSL / multi-pass reasoning  | 8-13        | json-validator, regex |
| Deep state machine          | 32          | chess-perft           |

Harness does not waste turns on easy tasks (chunky 60-100s "first turn does
everything" pattern) and sustains 32 productive turns on hard tasks without
the compaction-induced wipeout that killed earlier chess runs.

### Ceiling not yet found

8 / 8 PASS means **the harness ceiling lies above this slate**, not at it.
The data does not say what the harness *can't* do — only what it can.

### Followup options

1. **Push the ceiling** — add 3-4 harder tasks: chess perft(4+) where node
   counts grow 20× (memory + speed pressure), tiny compiler frontend
   (lexer + parser + AST + eval), git-style content-addressed store with
   pack files, distributed-lock primitive with property tests.

2. **Lock in the wins (regression CI)** — wire the 8 PASS tasks into a
   GitHub Actions matrix that runs on every push to develop. Drift detector
   for the harness itself. Bigger compounding value once ceiling-finding is
   done.

3. **Cross-model ceiling check** — run the slate against a second model
   (claude or different qwen size) to measure how much harness compensates
   for model size vs how much it inherits ceiling.

Recommend: **1 → 2 → 3.** Find the ceiling first, then CI, then cross-model.

---

## Slate 2 (harder probe, 3 tasks)

Different shapes from slate 1: multi-stage pipeline, algorithmic depth, state-heavy VM. Goal: find where 8/8 starts to fail.

| #  | Task            | Type                      | Surface           | Budget  | Assert        |
|----|-----------------|---------------------------|-------------------|---------|---------------|
| 09 | mini-compiler   | 3-stage pipeline          | lex+parse+eval    | 45m     | `go test ./...` covering 8 cases |
| 10 | bst-delete      | algorithmic (3-case del)  | recursion + stress | 40m    | `go test ./...` incl. 50-seed stress |
| 11 | bytecode-vm     | state machine + assembler | 2-pass label res  | 45m    | `go test ./...` incl. factorial-via-jumps |

| #  | Task            | Verdict | Wall    | Turns | Tokens in/out | Failure surface |
|----|-----------------|---------|---------|-------|---------------|-----------------|
| 09 | mini-compiler   | INCOMPLETE→PASS | 11m→2m34s | 25→1 | 2M→211k / 30k→8k | **death-by-verify** (P65 fix) |
| 10 | bst-delete      | PASS    | 1m22s   | 1     | 74k / 4.5k    | —               |
| 11 | bytecode-vm     | FAIL    | 10m39s  | 7     | 253k / 13.2k  | model boundary — self-imposed SWAP, no design rewind |

### Slate 2 findings

**Finding #1 — "Death by verification" harness bug (FIXED via P65).**
Task 09 first run: 25 turns INCOMPLETE despite `go test ./...` passing
throughout. Root cause: `recoveryPromptFor` accepted end_turn as "done" only
when `ToolCallCount == 0`. Productive agents who end each turn with a
read-only verify call (go test, ls, cat) had `ToolCallCount > 0`, runner
injected "Continue executing", agent re-verified, loop. Burned full budget.

Fix (`cli/internal/cmd/dogfood.go`): also run user assertions on
end_turn-with-tools. If green, accept as done. If failed, fall through to
existing "Continue" prompt. Re-run: **25 turns → 1 turn**, 11m → 2m34s,
2M tokens → 211k. 10× efficiency improvement, same task.

**Finding #2 — Vacuous-assert false positive (METHODOLOGY).**
Task 11 first run: `go test ./...` exit 0 with `[no test files]` — agent
never wrote tests, assertion silently passed. Fix going forward: 3-layer
assert per task:
1. `find . -name '*_test.go' -type f | grep .` (test file exists)
2. `go test ./...` (tests pass)
3. `go test -count=1 -v ./... 2>&1 | grep -q '^--- PASS:'` (actual PASS line visible)

Slate 1 is not retroactively at risk — manually verified each task wrote
test files. But slate 3+ MUST use the 3-layer assert.

**Finding #3 — First genuine model boundary (UNFIXED — model-shaped).**
Task 11 re-run with 3-layer assert: agent wrote `vm_test.go` (213 LOC)
including a `factorial_5_via_labeled_loop` test that uses a `SWAP` opcode
the spec doesn't have. Agent's `vm.go` doesn't implement SWAP. Across 7
turns of P63c-driven recovery, agent never rewinded the design decision —
instead kept trying to fix surface-level errors. P63c stall detection
correctly abandoned after 3 consecutive empty re-engagements (saved
~10+ turns vs the prior wasteful behavior).

This is the first failure attributable to the model, not the harness.
The prior 4 chess perft FAILs were harness-shaped (cadence compaction,
heredoc EOF, etc.) — that's now confirmed by this contrast: with the
harness improvements, chess perft PASSes, but bytecode-vm with a
self-imposed circular dependency in the test design FAILs.

### Updated assessment after slate 2

| Class                                  | Count | Verdict signature                |
|----------------------------------------|-------|----------------------------------|
| Trivial / known-shape (1-4 turns)      | 7     | all PASS, fast                    |
| Multi-stage / DSL (8-25 turns)         | 3     | all PASS after P65 fix            |
| Deep state machine (32 turns)          | 1     | PASS (chess perft, prior fail)    |
| Self-imposed circular dependency       | 1     | FAIL (genuine model boundary)     |

The harness is **stronger than expected**. 10 / 11 PASS. The single FAIL
is a clean model-boundary diagnosis (agent designs a test that requires
a feature not in spec, then can't reorient). Harness saved tokens via
P63c stall detection rather than burning budget.

### Next-step recommendation update

After slate 2 evidence, original next-step ordering (find ceiling → CI →
cross-model) holds but with adjustments:

1. **Push the ceiling further** with 2-3 more "reorientation-requiring"
   tasks. Specifically tasks where the agent must (a) discover a bad
   design choice through testing, and (b) rewrite a chunk of code, not
   just patch a line. Examples: a graph algorithm where a wrong data
   structure choice manifests at depth 4+, a state-machine where
   transitions interact across files.
2. **CI wiring with 3-layer assert template** — the assert pattern is
   ready to template; wire 9-10 known-PASS tasks into GitHub Actions.
3. **Cross-model after CI** — compare qwen3.6-27b reorientation behavior
   vs another model on task 11.

---

## Slate 3 (reorientation-required probe)

Tasks where naive design passes basic tests but a later stress test forces
a design rewrite. Probes whether task 11 SWAP failure is a deep boundary
or shape-specific.

| #  | Task              | Reorientation trigger                        | Budget | Assert layers |
|----|-------------------|----------------------------------------------|--------|---------------|
| 12 | dijkstra-perf     | N=2000 perf test → naive O(V²) times out    | 30m / 25t | 3-layer + perf bar |
| 13 | atomic-batch      | concurrent read → single-mutex insufficient  | 30m / 25t | 3-layer + -race    |
| 14 | rate-limiter      | concurrency test → non-atomic counter races  | 30m / 25t | 3-layer + -race    |

| #  | Task              | Verdict | Wall    | Turns | Tokens in/out | Failure surface |
|----|-------------------|---------|---------|-------|---------------|-----------------|
| 12 | dijkstra-perf     | PASS    | 1m58s   | 1     | 137k / 6.2k   | trap skipped — agent picked heap from start |
| 13 | atomic-batch      | PASS    | 3m42s   | 1     | 178k / 12.2k  | trap skipped — copy-on-write snapshot from start |
| 14 | rate-limiter      | PASS    | 1m4s    | 1     | 68k / 2.9k    | trap skipped — textbook token-bucket from start |

### Slate 3 finding

**Finding #4 — reorientation-required traps fail to trip when textbook
patterns exist.** All three tasks (Dijkstra, atomic batch, rate limiter)
have well-known correct designs. qwen3.6-27b knows them and writes them
from the first turn — heap, copy-on-write snapshot, token bucket — no
"naive impl → stress test → redesign" cycle observed.

Contrast with task 11 (bytecode-vm): the failure was the agent INVENTING
a wrong design (factorial using a non-existent SWAP opcode) and being
unable to reorient out of its own invention. Reorientation pressure
applies to novel design choices, not textbook patterns.

**Implication for slate 4**: target tasks where there's no obvious
textbook pattern. The agent must make a novel design call, and the
constraint must force a redesign if the call was wrong. Constraints
like: unusual semantics, performance bound that doesn't match standard
big-O, multi-objective tradeoff.

### Updated tally after slate 3

| Verdict        | Count | Notes |
|----------------|-------|-------|
| PASS (real)    | 13    | textbook patterns + multi-stage + chess perft |
| PASS (1-shot)  | 6     | done in single agent turn |
| FAIL (model)   | 1     | bytecode-vm SWAP self-trap |
| INCOMPLETE→PASS| 1     | mini-compiler, P65 fix unblocked |
| Total          | 14    |       |

---

## Slate 4 (novel-design reorientation probe)

Tasks where there's no obvious textbook pattern. Agent must make a novel
design call. Constraint forces redesign if the call was wrong.

| #  | Task              | Trap                                          | Budget   | Assert |
|----|-------------------|-----------------------------------------------|----------|--------|
| 15 | sliding-dedup     | memory unbounded if no eviction mechanism     | 30m / 25t | 3-layer + -race |
| 16 | diff-reverse      | reverse needs OldValue captured at Diff time  | 30m / 25t | 3-layer (round-trip properties) |
| 17 | spmc-queue        | lock-free SPMC requires per-slot seq stamps   | 30m / 25t | 3-layer + -race |

| #  | Task              | Verdict | Wall    | Turns | Tokens in/out | Failure surface |
|----|-------------------|---------|---------|-------|---------------|-----------------|
| 15 | sliding-dedup     | PASS    | 3m53s   | 1     | 159k / 8.8k   | trap skipped — min-heap eviction from start |
| 16 | diff-reverse      | PASS    | 3m25s   | 1     | 367k / 10.9k  | trap skipped — OldValue captured, semantics clean |
| 17 | spmc-queue        | FAIL    | 43m35s  | 3     | 59k / 4.1k    | livelock + harness 32m verify_missing burn |

### Slate 4 finding

**Finding #5 — first concurrency-bug FAIL + harness time-bound gap (P66 fix).**
Agent wrote SPMC with `diff := seq - pos` check. Handled diff==0 (free)
and diff==1 (occupied per current push) but missed diff < 0 (slot has
older un-popped data from a previous wrap). Producer livelocks waiting
for diff to become 0 when queue is full mid-wraparound. The agent's
single-producer assumption blinded them to the queue-full case under
heavy contention.

Compounding harness fault: `go test -race` hangs for 600s (internal Go
test timeout) → assert fails → re-engage → agent runs another verify
that also hangs → turn 2 wall hits **32 minutes** before stream ended.
Total budget 40m exhausted with only 3 turns done. P63c stall detection
fired (stalled=2) but max_wall hit first.

The pattern: agent wrote a subtle concurrency bug AND the harness has
no per-assert (or per-tool-call) timeout to bound the cost of a hung
test. Combined effect: 43min wall on 3 turns.

**Root-cause fix (P66, commit a0a433b)**: chat agent loop now tracks
consecutive timeouts across calls. When ≥3 in a row, emits
`stop_reason=tool_timeout_loop` and ends the Prompt RPC cleanly.
Bounds worst-case turn wall at 3 × per-tool-timeout = ~3 minutes
instead of unbounded. Non-timeout results (success OR other errors)
reset the counter so legitimate one-off slow tests still work.

**Methodology layer (also kept)**: slate-5+ assert template wraps
tests in `timeout 60s` and uses `! grep '^--- FAIL:'` for stricter
PASS. This is defense-in-depth — P66 catches in-turn hangs, but the
assert wrapper catches post-turn assert hangs that don't go through
chat tools (e.g. if a future suite extension runs custom external
checks).

**P66 v3 re-run on task17** (post-fix): still FAIL (model boundary
unchanged), wall 43m → 40m. P66 didn't trigger in this run because
the agent's tool calls weren't 3-in-a-row timeouts — the agent's
in-turn `verify` calls actually PASSed (test ran in 1.7s without
race conditions surfacing at that scale), but the runner's external
`--assert "go test -race"` caught a race condition the agent's
verify didn't. P66 hits the wrong layer here; the bottleneck is
agent-side reasoning about "my impl LOOKS fine but the -race assert
disagrees — let me redesign", not tool hangs. The original 32-min
single-turn pathology is structurally prevented by P66's per-turn
counter even though it didn't fire on the v3 reproducer.

**Standard 3-layer assert template (slate 5+)**:
```
--assert "find . -name '*_test.go' -type f | grep ."
--assert "timeout 60s go test -race ./..."
--assert "! timeout 60s go test -count=1 -race -v ./... 2>&1 | grep -q '^--- FAIL:'"
```

(For non-concurrent tasks: drop `-race`.)

### Slate 4 tally

| #  | Verdict | Notes                                              |
|----|---------|----------------------------------------------------|
| 15 | PASS    | min-heap eviction from start                       |
| 16 | PASS    | OldValue captured at Diff time — semantics clean   |
| 17 | FAIL    | wraparound livelock — model + harness time-bound   |

After 17 tasks total: 14 PASS, 2 FAIL (both model-shaped), 1 P65-unblock.

---

## Slate 5 (novel shape probe — non-concurrency)

Shift focus from concurrency to: closures/lexical scope, search/backtracking,
classic algorithm with non-obvious optimization. Different reasoning shape
than slates 3-4.

| #  | Task          | Trap                                                  | Budget | Assert template |
|----|---------------|-------------------------------------------------------|--------|-----------------|
| 18 | lisp          | closure semantics, lexical scoping consistency        | 30m / 25t | slate-5 (timeout 60s + FAIL-grep) |
| 19 | sudoku        | plain DFS times out on Norvig hard; need heuristic   | 30m / 25t | slate-5 + perf bar             |
| 20 | union-find    | need BOTH path compression AND union-by-rank          | 30m / 25t | slate-5 + perf bar             |

| #  | Task          | Verdict | Wall    | Turns | Tokens in/out | Failure surface |
|----|---------------|---------|---------|-------|---------------|-----------------|
| 18 | lisp          | PASS    | 23m3s   | 4     | 1.4M / 14k    | turn 3 verify_missing 7.7m, recovered turn 4 |
| 19 | sudoku        | PASS    | 21m54s  | 7     | 2.3M / 30k    | turn 1 verify hung 11m, recovered |
| 20 | union-find    | PASS    | 1m25s   | 1     | 75k / 4.6k    | trap skipped — both optimizations from start |

### Slate 5 finding

| #  | Verdict | Notes                                              |
|----|---------|----------------------------------------------------|
| 18 | PASS    | 4 turns / 23m — closure semantics required recovery |
| 19 | PASS    | 7 turns / 22m — sudoku hard timed out turn 1, retried |
| 20 | PASS    | 1 turn / 1m25s — textbook                          |

Slate 5 confirms: harness drives to completion on non-concurrency
novel-shape tasks too. Long-tail (lisp 23m, sudoku 22m) is agent
exploration time, not harness pathology — P63c stall detection
correctly did NOT fire (real work happening each turn).

### Final tally — 20 tasks across 5 slates

| Verdict          | Count | Notes |
|------------------|-------|-------|
| PASS             | 18    | 13 in 1 turn, 5 in 2-13 turns |
| FAIL (model)     | 2     | bytecode-vm SWAP self-trap, spmc-queue livelock |
| Harness fixes    | 1     | P65 death-by-verify (commit 84beb62) |
| Methodology      | 1     | 3-layer assert + timeout wrapper |

**Harness ceiling ≥ model ceiling** on most tasks, with **boundary
tasks** where the agent's first design either is or isn't correct
(non-deterministic across runs). Confirmed across 20 distinct task
shapes: pure code, modifications, multi-stage pipelines, deep state
machines, concurrency, novel-design reorientation, search +
heuristic, classic algorithms.

### Boundary-task discovery — chess perft re-run

Slate 1 ran chess perft (task 07) once and saw PASS (32 turns, 30m).
Concluded "harness now exceeds model" on this task.

Post-P65/P66 re-run (2026-05-19): chess perft FAILed at 8 turns,
30m, reason=stalled (P63c abandoned at 3 consecutive empty
re-engagements). Assertion tail:
```
Perft(Kiwipete, 2) = 2043; want 2039
Perft(Kiwipete, 3) = 98249; want 97862
```

Move-gen has a real bug — probably en-passant or pin-handling on
Kiwipete (initial position likely PASSes; the standard catch-bugs
position fails). Different LLM sampling produced a different (less
correct) first design than the slate-1 attempt.

**Updated assessment**: chess perft is a **boundary task**, not a
"harness > model" data point. Aggregate so far:

| Attempt | Outcome | Notes                                      |
|---------|---------|--------------------------------------------|
| Pre-P63b/c (×4) | FAIL | budget burned on hung tests / wrong design |
| Slate 1 (post-P63b/c) | PASS | 32 turns / 30m / 8.4M tokens                |
| 2026-05-19 #2 (P65/P66) | FAIL | 8 turns / 30m / 2.9M tokens, P63c abandon  |
| 2026-05-19 #3 (P65/P66) | FAIL | 9 turns / 27m / 0.4M, Kiwipete depth-1 off by 5! |

Pass rate ≈ 1/3 in post-P63b/c attempts (N=3, only one PASS). Note
that Kiwipete depth-1 = 43 vs 48 in attempt #3 means the first
design attempt missed FIVE legal moves at depth 1 — agent never
recovered. P63b/c improvements eliminated
budget-burn FAILs but didn't move the model ceiling — they enabled
the model to occasionally land on the right design but also enabled
the harness to abandon faster when the model doesn't.

This is the more honest framing. The harness drives to completion
*when the model's first attempt is workable*. When the agent
picks a wrong design path on first attempt and the path doesn't
have a clean local fix, no harness improvement makes the model
re-architect. That's a model-side property.

**Implication for the harness ceiling claim**: the harness is
strictly better than the underlying model at "drive to completion
once a workable design is in place". It does NOT compensate for
the model's design-quality variance on first attempt.

### Task 11 (bytecode-vm) variance re-run

Original FAIL: agent invented `SWAP` opcode in test, couldn't fix.

Re-run (2026-05-19, P65/P66 active): different FAIL mode. Agent
wrote `vm_test.go` with a syntax error (`expected '}', found 'EOF'`
at line 215) and couldn't fix it before P63c stalled at 6 turns
(11.5m wall). N=2: 2 FAILs, each with a distinct self-imposed bug.

Conclusion: task 11 is **firmly below model ceiling** for
qwen3.6-27b, but the FAILure manifests differently each attempt.
What varies is *which* self-imposed bug the agent makes; what's
constant is the model's inability to step back and redesign once
a bug surfaces.

### Finding #6 — temperature 0.7 may be too high for autonomous coding

The variance observed on chess perft (1 PASS / 2 FAIL) and the
different bug shapes on task 11 (SWAP / syntax error) are consistent
with high-temperature sampling. `server/internal/service/session_prompt.go:705`
hardcodes `Temperature: 0.7` on every `prov.Complete` call.

Reference points:
- Codex defaults to 0.0-0.2 for code generation
- Anthropic's tool-use docs suggest 0.0-0.2 for deterministic
  tool selection
- 0.7 is more typical for creative writing / brainstorming

**Hypothesis (not yet tested)**: lowering to T=0.3 or T=0.2 would
reduce variance on boundary tasks and reduce the design-quality
spread on novel-design tasks (task 11, 17). It would also reduce
exploration creativity — possibly hurting tasks that benefit from
trying alternate approaches.

**Not permanently changed in this loop** — temperature is a
user-visible behavior choice. Probe ran with a *local-only* edit
to `Temperature: 0.3`, then reverted to 0.7 before committing.

### T=0.3 probe results (2026-05-19)

Ran the 3 boundary/FAIL tasks once each at T=0.3, then reverted:

| Task                  | T=0.7 record | T=0.3 outcome                |
|-----------------------|--------------|------------------------------|
| 07 chess perft        | 1 PASS / 2 FAIL | **PASS** (2 turns / 15m43s) |
| 17 spmc-queue         | 0 PASS / 2 FAIL | **PASS** (5 turns / 14m08s) |
| 11 bytecode-vm        | 0 PASS / 2 FAIL | FAIL (no test file written) |

Two of three boundary/FAIL tasks shifted to PASS at T=0.3. Task 11
remains FAIL (different mode this time — agent skipped test
writing entirely). Task 11 looks like a genuinely below-model-
ceiling task, not just sampling unlucky.

**Recommendation (data-supported)**: change default Temperature
from 0.7 to 0.3 (or 0.2) in `session_prompt.go:705` for the chat
agent loop. Expected impact:

- Boundary tasks (chess, spmc, possibly others) shift from
  "sometimes PASS" to "more often PASS"
- Existing-PASS tasks should be unaffected or slightly faster
  (less variance ≈ less wasted exploration)
- Cost: model creativity reduced — could hurt tasks that need
  the agent to try unusual approaches (none observed in this slate)

**Tradeoff caveat**: T=0.3 isn't strictly better. Task 11 at T=0.3
FAILed with a NEW mode — agent skipped test writing entirely
(`go test ./...` returned `[no test files]`). At T=0.7 the agent
wrote tests but with bugs. Lower temperature ≈ more conservative
= agent less likely to explore unprompted requirements. For tasks
needing both precision AND exploration, a middle value (T=0.4 or
T=0.5) might be the sweet spot, not measured in this loop.

Did NOT make the change in this commit since it affects all chat
behavior, not just dogfood. Leaving as documented follow-up with
the caveat — not a slam-dunk default switch, more like "T=0.7 is
too high for many tasks but T=0.3 is too low for others, sweet
spot likely 0.4-0.5, needs more probing".

### T=0.5 sweet-spot probe

Ran chess perft at T=0.5 once: FAIL (7 turns / 18m, P63c stall).
Agent wrote `debug_test.go` to inspect moves (real debugging
behavior) but still couldn't get perft correct.

Chess perft pass rate by temperature (small N):

| T    | PASS / total | Notes |
|------|--------------|-------|
| 0.7  | 1 / 3        | one PASS in slate 1                |
| 0.5  | 0 / 1        | this probe                         |
| 0.3  | 1 / 1        | T=0.3 probe earlier in this loop  |

T=0.5 doesn't obviously beat T=0.7 on chess at N=1. T=0.3 is
the only temperature with a clean PASS in this loop's reruns,
but task 11 T=0.3 FAILed (no tests). The right answer is
per-task: precision tasks (move-gen correctness) want low T,
exploration tasks (write tests for an unfamiliar API) want
higher T. A per-task temperature override on `gil dogfood`
(e.g. `--temperature 0.3`) is probably more useful than picking
one global default.

### Honest tally (post-variance probing)

| Class                                            | Count | Notes |
|--------------------------------------------------|-------|-------|
| Distinct tasks with at least 1 PASS              | 18    | Slate 1-5 PASSes |
| Distinct tasks consistently FAIL across attempts | 2     | task 11 bytecode-vm, task 17 spmc-queue |
| Boundary tasks (some PASS, some FAIL)            | 1     | task 07 chess-perft (1 PASS / 2 FAIL = ~33%) |
| Harness fixes applied during measurement         | 2     | P65 (death-by-verify), P66 (timeout-loop) |

The original "harness ceiling > model ceiling" headline was
overconfident — it was inferred from a single attempt per task.
With variance data (chess perft N=3, bytecode-vm N=2), the honest
framing is:

- The harness is robust at "drive to completion once a workable
  design exists" (18 / 18 distinct PASS-shapes at least once)
- The harness does not lift the model out of a bad first-attempt
  design (2 distinct tasks with 100% FAIL rate)
- Some tasks straddle the boundary (chess perft, possibly others
  not yet measured)
- Variance data for the other 17 distinct PASS tasks is not yet
  collected; treat the 14/14 suite PASS as a single attempt, not
  a robust baseline.

---

## Regression suite — full run (2026-05-18, post-P66)

Ran `docs/eval/run-suite.sh "0[1-6]-*" "0[89]-*" "1[0-6]-*"` against
the daemon built from commit `a0a433b` (P65 + P66). All 14 wired
known-PASS tasks PASSed cleanly:

| Task             | Wall   | Tokens in | Notes |
|------------------|--------|-----------|-------|
| 01 md2html       | 270s   | 331k      |       |
| 02 json-validator| 191s   | 339k      |       |
| 03 bug-fix       | 26s    | 63k       |       |
| 04 lru-cache     | 41s    | 44k       |       |
| 05 refactor      | 27s    | 51k       |       |
| 06 regex         | 160s   | 97k       |       |
| 08 http-kv       | 74s    | 99k       |       |
| 09 mini-compiler | 128s   | 173k      | P65 unblock confirmed (not INCOMPLETE) |
| 10 bst-delete    | 103s   | 103k      |       |
| 12 dijkstra-perf | 185s   | 225k      |       |
| 13 atomic-batch  | 201s   | 175k      |       |
| 14 rate-limiter  | 87s    | 117k      |       |
| 15 sliding-dedup | 169s   | 111k      |       |
| 16 diff-reverse  | 206s   | 158k      |       |

**Totals**: 1867s ≈ 31m wall, 2.1M input tokens, 14/14 PASS, $0 cost.

This is the first confirmed end-to-end regression baseline for the
gil harness. Suite is now a load-bearing artifact — every push to
develop should run it (currently manual; CI wiring deferred until a
CI-friendly model credential is solved).

Task 07 (chess-perft, ~30min slow) excluded from default suite run;
runnable separately via `run-suite.sh "07-*"`. Tasks 11, 17 (known
model-boundary FAIL) not wired into suite; runnable from
`/home/ubuntu/eval/task1?-*/` for re-investigation.

---

## Finding #6 close — `--temperature` flag on `gil dogfood` (2026-05-19)

Per-task temperature override shipped as `gil dogfood --temperature
<float>` (commit `8f02984`). Default 0 falls through to the daemon's
existing 0.7; values >0 override per request.

Wire path: `PromptRequest.temperature` (proto field 6) → SDK
`PromptOptions.Temperature` → daemon picks `req.GetTemperature()`
when >0 in `session_prompt.go`. Chat surface unchanged — only the
explicit `--temperature` flag changes behaviour.

Recommended usage from probe data:
- `--temperature 0.3` for precision / "first design must be right"
  tasks (chess move-gen, lock-free SPMC). One PASS each at T=0.3
  where T=0.7 fails.
- Leave unset (0.7 default) for exploration-heavy tasks (bytecode
  VM where the agent has to invent its own test suite — at T=0.3
  the agent skipped writing tests).

Did NOT change the global default to 0.3: at small N the 0.3 vs 0.7
tradeoff cuts both ways. Per-task knob lets dogfood probes use the
right T without affecting interactive chat.

Smoke: `gil dogfood --temperature 0.3` on hello-go PROMPT — PASS
1 turn / 8.5s / vet clean.
