# First Dogfood Procedure — gil-on-gil

Goal: have gil add a missing CLI command (e.g. `gil sessions delete <id>`) to its own codebase autonomously.

**Status:** Procedure documented; actual run requires an Anthropic API key. Mark Phase 8 T7 as "ready to execute" rather than "executed" — the user runs this themselves and captures results in a follow-up dogfood report.

---

## Prerequisites

- `make build` produces `bin/gild`, `bin/gil`, `bin/giltui`, `bin/gilmcp`
- `ANTHROPIC_API_KEY` environment variable is set
- (optional) `bwrap` installed for sandbox runs

## Procedure

### Step 1 — start gild

```bash
cd /home/ubuntu/gil
./bin/gild --foreground --base ~/.gil &
GILD_PID=$!
sleep 1
```

### Step 2 — create session in the gil repo

```bash
SESSION=$(./bin/gil new --working-dir /home/ubuntu/gil 2>/dev/null | awk '{print $3}')
echo "Created $SESSION"
```

### Step 3 — interview

```bash
./bin/gil interview $SESSION
```

When prompted, give answers like:
- **Goal**: "Add a `gil sessions delete <id>` CLI subcommand that calls a new SessionService.Delete RPC and removes the session from SQLite + the on-disk session directory."
- **Constraints**: "Go, gRPC. Follow existing CLI patterns at cli/internal/cmd/. Add a proto field, regenerate via buf, implement server handler, add SDK method, wire CLI."
- **Verification**: "make build passes; make test passes; `bin/gil sessions delete <some-id>` exits 0 and removes the SQLite row + on-disk dir."
- **Workspace**: "LOCAL_NATIVE; the gil repo at /home/ubuntu/gil"
- **Risk**: "ASK_DESTRUCTIVE_ONLY"
- **Models**: "anthropic / claude-opus-4-7"
- **Budget**: "100 iterations max"

When the system says "ready to confirm", confirm.

### Step 4 — run

```bash
./bin/gil run $SESSION --provider anthropic
```

Watch progress via `./bin/gil events $SESSION --tail` in another terminal, or use `./bin/giltui`.

### Step 5 — review

After "Status: done":
```bash
cd /home/ubuntu/gil
git status         # see what gil did
git diff --stat    # high-level summary
git log --oneline -5
```

Verify `make test` and `make build` pass. Optionally `make e2e-all` to confirm no regression.

### Step 6 — write up

Capture in `docs/dogfood/2026-04-XX-gil-sessions-delete.md`:
- Iteration count, total tokens, cost (~)
- Whether the verifier passed first try or after retries
- Any stuck-recovery events
- Quality assessment of the generated code (style match, edge cases, tests)
- Memory bank evolution (cat `~/.gil/sessions/$SESSION/memory/progress.md`)

## Why this task

`sessions delete` is intentionally chosen because:
1. It exercises ALL major components: proto, server, SDK, CLI (4 modules)
2. It has a clear verifier (a CLI command that produces filesystem effect)
3. It's small enough to fit in 1-2 iterations of context but big enough to need editing several files
4. It's a real missing feature (we have new/get/list/restore but no delete)

If gil completes this, we'll have empirical evidence that Phases 1-8 work together for a non-trivial autonomous task on its own codebase.

## Expected gotchas

- Proto regeneration: gil's spec must include `buf generate` as part of the verification pipeline (or as a tool the agent learns about). Add to spec constraints if needed.
- Permission gate: ASK_DESTRUCTIVE_ONLY allows everything except destructive bash; gil should be fine. If `git rm` triggers a deny, the agent will see `permission_denied` and need to use a different approach (e.g., `os.Remove` from inside Go code).
- Memory bank: gil should write progress notes as it goes. After the run, `progress.md` should narrate the work.
- Stuck detection: if gil loops on the same compile error, AltToolOrder should kick in. If shadow git is enabled, ResetSection can roll back a bad change.

## What we won't validate here

- Multi-day continuity (Phase 9+)
- Performance under 100M-token loads (Phase 9+)
- Real-world tasks beyond gil-on-gil (Phase 9+)

This is the first dogfood — proof of life. Subsequent dogfoods on bigger tasks belong in their own docs.
