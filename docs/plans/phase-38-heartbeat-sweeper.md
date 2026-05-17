# Phase 38 — Mid-session orphan sweeper (heartbeat)

## Why

P36 fires once, at daemon startup. While daemon is up, an agent loop
goroutine that dies silently (panic recovered into errgroup, hung
syscall, OOM-killed MCP child, dropped event subscriber) leaves the
DB in `status='running'` indefinitely. The user has no signal until
they restart the daemon — which they might never do for a "stable"
process.

For the autonomous coding harness goal, this is the same trust gap
as P36 but on a different timescale. The system should self-heal
without requiring the user to bounce gild.

## Design

### Heartbeat

`runProgressSnap` gains `lastHeartbeat time.Time`. Two write sites:
1. `RunService.Start` seeds it to `time.Now()` so a brand-new run
   isn't immediately flagged stale.
2. The progress-subscriber goroutine refreshes it on every event the
   run emits. Any event qualifies — the goroutine being alive enough
   to deliver events IS the heartbeat.

### Sweeper

`StartMidSessionOrphanSweeper(ctx)` launches a long-lived goroutine
that wakes every `sweepInterval` (2 min). Each tick:
- Lists sessions where `status='running'`
- For each, checks `runProgress[id]`:
  - Missing entry → stale (the goroutine never ran or got cleaned up)
  - `lastHeartbeat.IsZero()` → stale (snap never seeded)
  - `lastHeartbeat.Before(now - staleHeartbeatThreshold)` (10 min) →
    stale
- Reaps stale sessions: status='stopped', `run_orphaned` audit event
  with `reason=stale_heartbeat`, in-memory state cleared.

Threshold of 10 min is well above any reasonable LLM call duration
(Anthropic streaming on a long turn is < 2 min) and well below "user
walked away for hours" (where they'd notice nothing for a while
anyway). False-positive risk is low.

### Reason distinguishes from P36

The audit event uses `reason=stale_heartbeat` so consumers can tell
this from P36's `reason=daemon_restart`. P37's auto-resume opt-in
applies to P36 but NOT P38 — a hung mid-session run shouldn't auto-
restart blindly; the user should investigate first.

## Acceptance criteria

1. No running sessions → sweep is no-op.
2. Running session with no runProgress entry → reaped, audit event
   with `reason=stale_heartbeat`.
3. Running session with stale `lastHeartbeat` (older than threshold)
   → reaped, in-memory entry cleared so re-Start is clean.
4. Running session with fresh `lastHeartbeat` → NOT reaped.
5. Non-running sessions never touched.
6. Nil repo → no-op.
7. Multiple stale orphans + one live session → reaps stale, leaves
   live.

## Result (2026-05-17)

**Shipped.** 7 unit tests pass.

`StartMidSessionOrphanSweeper` started in `gild/main.go` immediately
after the P36 startup sweep, using `context.Background()` since
gild's lifecycle is process-bound (the goroutine dies with the
process).

Threshold is intentionally generous (10 min) — false positives on a
real long-running task would be terrible for autonomy, while a
true-positive on a hung goroutine surfaces within sweepInterval (2
min) past the threshold. Worst case: 12 minutes between actual
hang and user-visible flip.

**Files touched:** 3
- `server/internal/service/run.go` — `lastHeartbeat` field on
  runProgressSnap, refresh in progress subscriber, sweeper +
  sweepStaleHeartbeats methods, threshold/interval constants.
- `server/cmd/gild/main.go` — start sweeper after P36 reap.
- `server/internal/service/run_sweep_test.go` (new) — 7 tests.
- `docs/plans/phase-38-heartbeat-sweeper.md` — this doc.

Followups (deferred):
- Surface the stale_heartbeat event distinctly in `gil status` /
  chat banner (current display is "STOPPED" indistinguishable from
  user-initiated stop).
- Tighter threshold once we have prod telemetry on real LLM call
  durations (10 min is the conservative default).
