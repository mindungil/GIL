# Phase 26 — Dogfood Runbook

**Status**: pending user execution
**Branch**: `feat/p26-chat-only-surface`
**Last build verified**: 2026-04-29 (commit `8829fbf`)

This runbook verifies that P26 V1's chat-only surface (single REPL,
slash commands, status strip, phase tracker) functions correctly
end-to-end against a real interview→run→done cycle.

The unit + integration tests in `cli/internal/chat/repl/...` already
proved the loop, slash dispatcher, tracker state machine, and gRPC
adapter at composition level (43 tests across render + repl). This
runbook is the production verification using a real LLM and live
daemon.

## Prerequisites

- A configured LLM credential (`gil auth login` or `ANTHROPIC_API_KEY` /
  equivalent in your shell)
- `git checkout feat/p26-chat-only-surface` (or `develop` after merge)
- A clean test workspace (e.g., `/tmp/p26-dogfood-1`)

## Step 1: Build

```bash
cd /home/ubuntu/gil
go build -o /tmp/p26-gild ./server/cmd/gild
go build -o /tmp/p26-gil  ./cli/cmd/gil
```

## Step 2: Verify the new --help grouping

```bash
/tmp/p26-gil --help
```

**Expect** these section headers, in order:
- `Setup:` (init, auth, doctor)
- `Sessions & runs:` (chat, new, resume, clarify, session)
- `Diagnostics & history:` (status, cost, restore)
- `Tools & integration:` (mcp, permissions)
- `Maintenance:` (daemon, update)
- `Advanced (headless / scripting):` (events, export, import, interview, run, spec, stats, watch)

Verb-mode commands like `gil status`, `gil interview <id>`, `gil run <id>`
should still work — only the `--help` grouping changed.

## Step 3: Start the daemon

```bash
/tmp/p26-gild --detach
# or in foreground for live logs:
/tmp/p26-gild
```

Expected: `daemon listening on ~/.gil/gild.sock` (or already-running notice).

## Step 4: Run a small mission via chat

```bash
mkdir -p /tmp/p26-dogfood-1 && cd /tmp/p26-dogfood-1
/tmp/p26-gil
```

The chat REPL should open. Then:

1. **Idle banner**: `[idle · type a prompt to start, or /sessions to resume]`
2. **Type a small mission**, e.g.:
   ```
   add a hello-world endpoint at /tmp/p26-dogfood-1
   ```
3. **Watch the strip update through interview**:
   `[interview · 1/11 slots · sat 9%]` → climbing as the agent fills slots.
4. **Verify inline system notes**: `slot filled (N/M, sat X%)`, occasional
   `N finding(s)` (adversary), eventually `ready to freeze — /run to start`.
5. **At ready-to-freeze**, type:
   ```
   /run
   ```
6. **Watch the strip switch**: `[run · iter 1/100 · $0.00 · ASK_DESTRUCTIVE]`
   ticking up.
7. **On done**, see `[done · N iters · $X.XX · OK 4/4 checks · /diff /merge]`.
8. **Inspect the diff**:
   ```
   /diff
   ```
9. **Apply** (note: V1 `/merge` returns an error pointing you to manual `git apply` —
   server-side merge endpoint is a P26.5 followup; verify the error message is
   user-friendly):
   ```
   /merge
   ```
10. **Quit**:
    ```
    /quit
    ```

## Step 5: Verify acceptance criteria from spec §10

Tick each off as you observe it:

- [ ] **Onboarding gate**: only visible on a fresh install (otherwise N/A).
      Run with no `~/.gil/` to verify if interested.
- [ ] **Slot / saturation / adversary surface inline** during interview
      (system notes appear between turns, strip counts climb).
- [ ] **Saturation→confirm dialog**: `ready to freeze` note + strip variant.
- [ ] **/run starts**, strip transitions interview → run, iter ticks up.
- [ ] **[done] strip** shows checks-passed and offers `/diff` `/merge`.
- [ ] **Verb-mode `gil status` still works** in another shell — same
      session list, no regression from the grouping change.
- [ ] **NO_COLOR honored**: `NO_COLOR=1 /tmp/p26-gil` produces no ANSI.
- [ ] **--ascii honored**: `/tmp/p26-gil --ascii` shows `OK` / `FAIL` on the
      done strip instead of `✓` / `✗`.

## Step 6: Capture any UX bugs

Anything jarring (jitter, off-by-one in strip counts, lost events,
unhelpful errors) → file under a "## Followups" section in
`docs/plans/phase-26-implementation.md`. Address before declaring V1
fully done; small bugs are OK to merge with followups recorded.

## Step 7: Phase-completion marker (after dogfood passes)

Once you've verified §10 acceptance criteria pass:

```bash
cd /home/ubuntu/gil
git commit --allow-empty -m "chore: P26 V1 dogfood passed — chat surface lands"
```

Then merge to develop:

```bash
git checkout develop
git merge --no-ff feat/p26-chat-only-surface -m "merge P26 — chat-only surface V1"
```

## Known V1 gaps (deferred to P26.5 or later)

- `/merge` returns an error — server-side merge endpoint not implemented.
  Workaround: `cd <session-workspace> && git apply <path-to-diff>`.
- `Spec` returns JSON not YAML (renderer prints it either way).
- `StartRun` uses empty provider/model — relies on server defaults.
- `MaxIter` not extracted from `run.started` — strip shows `iter N/0`
  instead of `iter N/100`.
- Run-time prompts are V1.1 (echoes a placeholder system note today).
  See `docs/plans/phase-26.5-runtime-control.md` (to be created).
- `drainInterviewStream` doesn't observe ctx — goroutine outlives `/quit`
  until the stream's underlying connection closes (acceptable for V1
  dogfood since process exit closes the gRPC connection).
