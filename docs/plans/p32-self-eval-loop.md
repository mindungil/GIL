# P32 — 12-hour self-eval loop

Status: plan (2026-05-16)

> Continuous bench-fix-bench loop. Goal: surface gil's behavior under
> real load, identify weak spots iteration-by-iteration, fix them,
> and re-bench. Hard 12-hour cutoff. Branch `eval/loop-2026-05-16`.

## 1. Harness

Lives outside the repo under `/tmp/gil-eval-loop/` so a re-run on
another machine can drop in fresh tasks without polluting `docs/`.
The harness files are reference-only — the canonical artifacts
(decisions, fix commits, iteration findings) all land in the repo.

```
/tmp/gil-eval-loop/
├── state.json           # iter count, cutoff, branch, notes[]
├── tasks.sh             # bench tasks (f1-f8 reuse + L1-L4 new)
├── run_iter.sh          # one iteration runner
└── iter/
    ├── 1/
    │   ├── results.tsv  # task wall verify turns tool_calls log
    │   └── <task>/{gil.log, ...}
    ├── 2/
    └── ...
```

### 1.1 Task set

| ID | Pattern | Why |
|---|---|---|
| f1-f8 | Failure-floor stress (reuse) | Regression baseline |
| L1 | Multi-file rename across imports | Cross-file edit discipline |
| L2 | error wrap correctness (errors.Is) | Subtle semantic fix |
| L3 | Function extraction, behavior preserved | Refactor + test discipline |
| L4 | Top-K with tie semantics | Read-then-fix accuracy |

L-set grows over iterations. New tasks appended as the loop discovers
gaps the current set doesn't probe.

### 1.2 Metrics per task

- **wall_s** — end-to-end seconds.
- **exit** — gil chat exit code.
- **verify** — external ground-truth (PASS / FAIL / FAIL_* sub-codes).
- **turns** — approximate user→assistant turn count.
- **tool_calls** — count of ⚒ or `tool_call` lines.

These are crude but cheap. Detailed timing comes from
`/home/ubuntu/.local/share/gil/sessions.db` if we need it.

## 2. Iteration cycle

```
loop while now < cutoff and iter < max_iters:
    1. Pre-flight:
       - cutoff check (state.cutoff_at vs now)
       - gild liveness (pgrep -f 'gild -foreground'); restart if dead
       - build sanity (make build); skip iter if build fails
    2. Bench: ./run_iter.sh <N> <task list>
    3. Analyze (Claude):
       - read iter/<N>/results.tsv + per-task logs
       - identify one "weak spot" — failure, regression, anomaly,
         pattern. ONE per iteration. Bias to concrete + bounded.
    4. Spec the fix:
       - append a section to docs/research/2026-05-16-eval-loop.md
         (the loop's running log) with iter, finding, hypothesis.
       - if no fix is implementable in this iter → skip + note.
    5. Implement (Claude or subagent):
       - depends on size. Trivial: direct edit. Multi-step: subagent
         per writing-plans + subagent-driven-development conventions.
       - tests must pass before commit.
       - if tests fail, REVERT (do not commit). Mark iter as
         "fix-failed".
    6. Build + install:
       - make build && sudo install (so the next iter benches the
         fixed binary).
       - restart gild so the new binary takes effect.
    7. Commit on eval/loop-2026-05-16:
       - one commit per iteration. Message format:
         `iter N: <one-line finding> — <one-line fix>`.
    8. Push (best-effort).
    9. Update state.json (iter_count, last_iter_at, notes[]).
   10. ScheduleWakeup or /loop continuation for next iter.
```

### 2.1 Stop conditions

- now ≥ cutoff_at (12h hard).
- iter_count ≥ max_iters (default 30, soft).
- Build fails 3 iterations in a row (signals environmental issue).
- gild can't be restarted (signals binary/proto incompatibility).

### 2.2 Anti-patterns the loop must avoid

- Re-fixing the same finding. State.notes tracks completed findings;
  every analysis cross-checks notes before declaring a new finding.
- Speculative fixes (changes without a concrete trace). If the
  current iter results don't surface anything, the iter is a no-op
  on the code side; commit only the bench data.
- Architectural shifts. Per-iter changes are *tightening* /
  *narrowing*, not rewriting. Big design decisions land in their
  own P-numbered phase, not inside the loop.

## 3. Running log

`docs/research/2026-05-16-eval-loop.md` — appended each iteration:

- iter N
- findings (1 weak spot, ≤120 chars)
- hypothesis (why)
- fix shape (1-2 lines + commit SHA, OR "skipped: <reason>")
- post-fix bench delta vs previous iter

That doc is the deliverable. The loop's *value* is the
finding-fix-finding chain captured in one file, not 30 random
commits.

## 4. End-of-loop wrap-up

At cutoff or max_iters:

1. Final bench iteration (clean snapshot).
2. Wrap-up section in the running log:
   - tally: N iters, M fixes landed, K skipped.
   - top 3 surprising findings.
   - residual gaps (things the loop noticed but couldn't fix).
3. Open PR `eval/loop-2026-05-16 → develop` with the running log
   front-and-center in the body.
4. Mark task #68 complete in TaskCreate tracker.

## 5. Failure modes & recovery

| Mode | Detection | Recovery |
|---|---|---|
| Tests pass but build broken | `make build` exit != 0 | Skip iter, log, retry next |
| Bench hangs (one task >5min) | `timeout 300` in run_iter.sh | Task marked TIMEOUT, iter continues |
| gild crashes mid-iter | `pgrep` check at pre-flight | Restart, retry once, then skip iter |
| Fix breaks unrelated test | `go test ./...` in step 5 | `git reset --hard HEAD~1`, mark fail |
| Wake-up scheduler missed | manual reading of state.json | Re-invoke /loop with state context |

## 6. Where this fits

This is P32. It builds on P28 (chat-mode enforcement), P29 (verify
tool gate), P30 (workingset persist), P31 (surface decision). The
fixes the loop discovers will become P33+ if they're substantive,
or commit-level cleanups inside the loop branch otherwise.
