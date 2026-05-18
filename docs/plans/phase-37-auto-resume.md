# Phase 37 — Auto-resume opt-in on restart

## Why

P36 made orphan runs visible — daemon restart → orphan flipped to
stopped → audit event. The user can see "this run was interrupted."
But seeing isn't autonomy.

For the autonomous coding harness goal, the next gap is: the user
shouldn't have to manually re-trigger a long-running task they
explicitly trusted gil to complete. If they set autonomy=FULL on a
verify-gated task and walked away, gild dying shouldn't mean the
task dies with it.

## Goal

Sessions whose frozen spec opts in via `Risk.ResumeOnRestart=true`
auto-restart on the next daemon launch. The reaper still does the
status flip + audit event (so visibility from P36 is preserved), then
additionally kicks a fresh run for opted-in sessions.

## Why opt-in (not default)

Auto-restarting an arbitrary task is unsafe. The user might have
killed the daemon ON PURPOSE because the run was misbehaving; surprise
re-launch is exactly the wrong response. Opt-in means "you, the
spec author, explicitly trusted this task to be safely re-runnable."

In practice, two task shapes match:
1. Idempotent (re-running converges, doesn't double-edit)
2. Verify-gated (the verifier check is the source of truth; the agent
   will know if it has work to do or not on entry)

Tasks that don't fit either should leave `ResumeOnRestart=false`
(the default).

## Design

### Proto

Add to `RiskProfile` in `proto/gil/v1/spec.proto`:

```protobuf
message RiskProfile {
  AutonomyDial autonomy = 1;
  bool adversary_reviewer_enabled = 2;
  bool stuck_detector_enabled = 3;
  bool resume_on_restart = 4;  // NEW
}
```

Backwards-compatible — existing sessions default to false.

### Reaper integration

`ReapOrphanRuns` (P36) extension:

```go
for _, sess := range runs {
    repo.UpdateStatus(ctx, sess.ID, "stopped")  // P36 flip

    autoResume := false
    if spec, err := specstore.Load(sessionDir); err == nil &&
        spec.Risk != nil && spec.Risk.ResumeOnRestart {
        autoResume = true
    }

    // Audit event includes auto_resume:bool
    persister.Write(event.Event{
        Type: "run_orphaned",
        Data: []byte(fmt.Sprintf(`{"reason":"daemon_restart","prior_status":"running","auto_resume":%t}`, autoResume)),
    })

    if autoResume {
        // Fire-and-forget. Start runs in a goroutine; the new agent
        // loop picks up where the prior died (same spec, same
        // checkpoint, same chat history).
        go s.Start(context.Background(), &StartRunRequest{
            SessionId: sess.ID,
            Detach:    true,
        })
    }
}
```

### Inheritance

The auto-resumed run inherits:
- Same FrozenSpec (loaded via specstore from disk)
- Same workspace state (P5 checkpoint untouched by restart)
- Same chat history (P34 persisted)
- Same working set (P30 persisted)
- Same plan steps (P2 persisted)

It does NOT inherit:
- In-memory iteration counter (resets to 1)
- In-memory tool call queue (was lost with the goroutine)
- Provider rate-limit / cost accumulator (resets — fresh budget)

The first iteration of the auto-resumed run sees the same world the
prior agent left behind. The verify-loop discipline ensures it
re-checks status before declaring anything done.

## Acceptance criteria

1. Spec with `Risk.ResumeOnRestart=true` → reaper kicks Start. Event
   has `auto_resume:true`.
2. Spec without the flag (or false) → reaper stops, no Start. Event
   has `auto_resume:false`.
3. Session with no spec → defaults to `auto_resume:false`.
4. Auto-resume goroutine errors are logged but don't roll back the
   stop status — user can always manually re-trigger via `gil run`.

## Result (2026-05-17)

**Shipped.** Proto regenerated (`make gen`) with the new field on
RiskProfile. ReapOrphanRuns extended to load each orphan's spec,
check Risk.ResumeOnRestart, and fire a fresh Start goroutine when set.

Tests (8 total in run_orphan_test.go, all PASS):
- P36 carry-forward: EmptyDB / FlipsRunningToStopped / AppendsOrphanedEvent /
  MultipleOrphans / NilRepo
- P37 new: AutoResumeFlagFlowsToEvent / NoSpec_DefaultsToManualResume /
  SpecResumeFalse_DefaultsToManual

The auto-resume goroutine fires after the test cleanup closes the DB,
producing a WARN log line on the auto-resume test — the WARN is
expected (Start does need a live DB), and the test asserts on the
synchronous event row that was written BEFORE the goroutine fired.
Production reaper runs at daemon startup with the DB very much alive.

Live verification deferred to first real session with the flag set —
the production sessions DB has no sessions with this flag yet (it's
brand new). The P36 baseline reaping still works; that was already
verified end-to-end.

**Files touched:** 4
- `proto/gil/v1/spec.proto` — `bool resume_on_restart = 4`
- `proto/gen/gil/v1/spec.pb.go` — regenerated
- `server/internal/service/run.go` — P37 branch in ReapOrphanRuns
- `server/internal/service/run_orphan_test.go` — 3 new tests
- `docs/plans/phase-37-auto-resume.md` — this doc
