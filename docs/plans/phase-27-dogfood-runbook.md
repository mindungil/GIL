# Phase 27 — Dogfood Runbook

**Status**: pending user execution
**Branch**: `feat/p27-context-wiring`
**Last build verified**: 2026-04-28 (commit 572e5a2)

This runbook verifies that P27 V1's context-wiring fixes (Compactor
instantiation, cache markers, per-model context, per-role budget,
grace call) function correctly against a live Anthropic backend.

The integration tests in `server/internal/service/p27_integration_test.go`
already proved the wiring at composition level using a mock provider.
This runbook is the production-grade verification using a real model.

## Prerequisites

- `ANTHROPIC_API_KEY` exported in your shell
- `git checkout feat/p27-context-wiring` (or `develop` after merge)
- A clean test workspace (e.g., `/tmp/p27-dogfood-N`)

## Step 1: Build

```bash
cd /home/ubuntu/gil
go build -o /tmp/p27-gild ./server/cmd/gild
go build -o /tmp/p27-gil ./cli/cmd/gil
```

## Step 2: Start the daemon

```bash
/tmp/p27-gild --detach
# or in foreground for live logs:
/tmp/p27-gild
```

## Step 3: Run a context-heavy mission

The mission is intentionally chosen to grow conversation context fast
(many file reads + a long write) so compaction has reason to fire.

```bash
mkdir /tmp/p27-dogfood
/tmp/p27-gil new --working-dir /tmp/p27-dogfood --goal "explore /etc and write a 200-line analysis of what you find to ./ANALYSIS.md"
SID=<the-id-from-above>
/tmp/p27-gil interview $SID
# Drive interview to freeze. The default Anthropic model should be claude-sonnet-4-6 or claude-opus-4-7.
/tmp/p27-gil run $SID
```

## Step 4: Verify acceptance criteria

While the run executes (or after it completes):

### 4.1 No provider context-overflow errors

```bash
/tmp/p27-gil events $SID 2>&1 | grep -i 'context.*length\|too many tokens\|400'
```
Expected: NO matches. Provider 4xxx errors mean compaction failed to fire in time — that's a P27 V1 regression.

### 4.2 At least one compaction event

```bash
/tmp/p27-gil events $SID | grep -i 'compact'
```
Expected: at least one event with type containing "compact" (e.g., `compact_start`, `compaction_applied`). Zero matches means Compactor was never triggered — could mean (a) mission too small, or (b) wiring broken.

### 4.3 Cache hit rate > 0

For Anthropic specifically, prompt caching savings are visible in the
API response usage block. If the daemon log captures this:

```bash
grep -i 'cache_creation_input_tokens\|cache_read_input_tokens' /var/log/gild/*.log
```

Or check the Anthropic Console (https://console.anthropic.com) → Usage
→ filter by date for the run's cache_read_input_tokens > 0.

If neither shows cache reads, the cache markers from T4 may not be
landing on the wire — possible wiring regression.

### 4.4 Per-model context respected (optional)

Configure a small-context model (e.g., `ollama:llama3:8b` if you run
Ollama locally) for the editor role and re-run a small mission. Verify
the compaction trigger fires earlier than for a large-context main.

This requires a local Ollama setup; skip if not available.

### 4.5 Grace call works on forced budget exhaust

```bash
mkdir /tmp/p27-budget
/tmp/p27-gil new --working-dir /tmp/p27-budget --goal "echo done" --budget-max-tokens 5000
SID2=<id>
/tmp/p27-gil interview $SID2
/tmp/p27-gil run $SID2
```

When the run completes, check the final session status:

```bash
/tmp/p27-gil status $SID2
```

Expected status: `budget_exhausted_with_handoff` (NOT `budget_exhausted`).
The last assistant message in the rollout should be a wrap-up summary
(naming what's done, what's pending, where to resume).

```bash
/tmp/p27-gil events $SID2 | tail -20
```

If status is `budget_exhausted` with no wrap-up message, the grace
call was bypassed — possible regression.

## Step 5: Sanity-check architect/coder split (optional)

If your spec configures different models per role (e.g.,
`Models.Main = "claude-opus-4-7"`, `Models.Weak = "claude-haiku-4-5"`),
the Compactor should use the Weak model for summarization. Verify by
running the cost breakdown:

```bash
/tmp/p27-gil cost $SID --by-role
```

Expected: a small spend attributed to "weak" role (the summarizer).

## Pass / Fail

Phase 27 V1 dogfood passes when:

- 4.1 (no overflow), 4.2 (compaction fired), 4.3 (cache hit > 0) all green.
- 4.5 (grace call) green.
- 4.4 and Step 5 are nice-to-have (skip if no local Ollama / no role split).

If any of 4.1, 4.2, 4.3, or 4.5 fails:
- Capture session ID + relevant events
- Open a P27 regression issue with reproduction steps
- DO NOT merge feat/p27-context-wiring to develop until fixed

## After Pass

```bash
cd /home/ubuntu/gil
git checkout develop
git merge --no-ff feat/p27-context-wiring -m "merge P27: context wiring repair"
git tag p27-v1
git commit --allow-empty -m "chore: P27 V1 dogfood passed — context wiring is live"
```
