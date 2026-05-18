# Phase 36 — Orphan Run Reaping

## Why

**Goal alignment**: gil is an autonomous coding harness. The credibility
of "I describe a task and walk away" rests on whether the user can
trust the system state when they come back.

Today: chat history (P34), workspace (P5 checkpoint), working set (P30),
plan steps (P2) all persist across daemon restart. **In-flight run
state does not.** When `gild` exits (intentional stop, OOM kill, host
reboot, crash) mid-run:

- The agent loop goroutine on the `RunService` struct dies with the
  process.
- The DB row for that session keeps `status='running'` forever.
- `gil status` / `list_sessions` show ghost progress that never
  advances.
- The user has no way to distinguish "still working" from "died
  silently 3 hours ago."

This is the largest remaining `feedback_agent_drives_system_safeguards`
gap — the memory says the system owns "schemas / limits / objective
termination / persistence." Orphan visibility IS objective termination.

## Goal

After daemon restart, every session left in `status='running'` is
flipped to `status='stopped'` and a `run_orphaned` event is appended
to that session's `events.jsonl`. Both surface clients (`gil status`,
`list_sessions`) and the audit trail reflect reality.

## Non-goals

- Auto-resume the run. v1 stops at "make orphan visible." A future
  phase can add `auto_resume_on_restart` as a spec flag that the
  reaper checks before flipping to stopped.
- New status enum value ("orphaned" distinct from "stopped"). v1
  reuses "stopped" + the event-row marker. Visual distinction is a
  followup.
- Reap mid-session, not just at startup. Reaping mid-session would
  need a heartbeat mechanism to detect dead agent loops; the v1
  startup sweep covers the only common case (full daemon restart).

## Design

### Trigger

One call at `gild` startup, after `NewRunService` is constructed and
BEFORE the grpc server begins serving:

```go
runSvc := service.NewRunService(repo, sessionsBase, factory)
if reaped, err := runSvc.ReapOrphanRuns(context.Background()); err != nil {
    slog.Warn("reap orphan runs", "err", err)
} else if reaped > 0 {
    slog.Info("reaped orphan runs from prior daemon", "count", reaped)
}
```

### ReapOrphanRuns

`server/internal/service/run.go`:

```go
func (s *RunService) ReapOrphanRuns(ctx context.Context) (int, error) {
    runs, err := s.repo.List(ctx, session.ListOptions{
        StatusFilter: "running",
        Limit:        1000,
    })
    if err != nil { return 0, err }
    for _, sess := range runs {
        if uerr := s.repo.UpdateStatus(ctx, sess.ID, "stopped"); uerr != nil {
            slog.Warn... // best-effort
            continue
        }
        // Audit row in events.jsonl.
        if p, perr := event.NewPersister(s.sessionDir(sess.ID)); perr == nil {
            _ = p.Write(event.Event{
                Source: event.SourceSystem,
                Kind:   event.KindNote,
                Type:   "run_orphaned",
                Data:   []byte(`{"reason":"daemon_restart","prior_status":"running"}`),
            })
            _ = p.Sync()
            _ = p.Close()
        }
    }
    return reaped, nil
}
```

Best-effort throughout: failures on individual sessions don't stop
the sweep; failures to write the audit row don't undo the status
flip (the status flip is the load-bearing change, the event is
forensic).

### Why "stopped" not "orphaned"

A new status value would surface the orphaning visually in
`gil status`. But it would also require:
- Proto enum addition (wire-compat decision)
- statusToProto mapping update
- TUI / CLI render branches for the new state
- Test fixtures across cli/tui/sdk that assume the existing enum set

For v1 we want the LOAD-BEARING fix (stop the ghost) cheaply. The
events.jsonl row is enough audit. A future phase can graduate to a
real status enum when there's a concrete UX need.

## Acceptance criteria

1. After daemon restart with N sessions in `status='running'`, all N
   are flipped to `status='stopped'`.
2. Each reaped session gets exactly one `run_orphaned` event appended
   to `events.jsonl` with `data={"reason":"daemon_restart","prior_status":"running"}`.
3. Sessions in `status='created'` / `'done'` / `'frozen'` / other
   states are NOT touched.
4. Reaping is idempotent — a second call right after the first finds
   zero orphans.
5. When `repo` is nil (defensive), reap is a no-op without panic.
6. Live verification: synthetically UPDATE a session to
   `status='running'`, restart daemon, confirm log line + DB flip +
   events.jsonl entry.

## Result (2026-05-17)

**Shipped.** All 5 unit tests pass:
- `TestReapOrphanRuns_EmptyDB_NoOp`
- `TestReapOrphanRuns_FlipsRunningToStopped`
- `TestReapOrphanRuns_AppendsOrphanedEvent`
- `TestReapOrphanRuns_MultipleOrphans` (also covers idempotence)
- `TestReapOrphanRuns_NilRepo_NoOp`

Live verification:
1. Set session `01KRR6QR9VJE26XR6Q3V2G7G0R` to `status='running'`
   in the production DB.
2. `pkill -9 gild` + restart with new binary.
3. Startup log: `INFO reaped orphan runs from prior daemon count=1`.
4. Post-restart DB: 0 sessions in `running` (was 1).
5. `events.jsonl` for the reaped session: appended a single
   `run_orphaned` line with `reason=daemon_restart`
   `prior_status=running`.
6. `gil session show` reflects STOPPED.

Existing 100+ service tests pass unchanged.

**Files touched:** 4
- `server/internal/service/run.go` — `ReapOrphanRuns` method (+ `log`
  import).
- `server/internal/service/run_orphan_test.go` (new) — 5 tests.
- `server/cmd/gild/main.go` — startup call wired with slog reporting.
- `docs/plans/phase-36-orphan-run-reaping.md` — this doc.

Followups (deferred — not in this phase):
- Auto-resume on restart for sessions with that spec flag.
- "orphaned" status enum for visual distinction.
- Mid-session orphan detection via heartbeat.
