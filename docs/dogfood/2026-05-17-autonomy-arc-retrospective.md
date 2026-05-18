# 2026-05-17 — Autonomy arc retrospective (P34-P40 + bench loop)

Snapshot date: 2026-05-17.
Branch: `feat/p33-reasoning-surface` @ `82b04ed` (origin sync).
Provider/Model under bench: `vllm/qwen3.6-27b` at OSLab.

## TL;DR

Seven phases shipped in a single session, each one closing a
specific autonomy gap in the chat-and-run harness:

| phase | gap closed | LOC | tests |
|---|---|---|---|
| P34 | chat history wiped on daemon restart | +602 | 9 |
| P35 | chat history grew unbounded → context overflow | +483 | 7 |
| P36 | mid-run daemon death → ghost "running" sessions forever | +381 | 5 |
| P37 | restart resume required manual `gil run` invocation | +320 | 8 (3 new) |
| P38 | hung goroutine mid-session → no detection until restart | +411 | 7 |
| P39 | chat agent looping silently → no signal to user | +356 | 6 |
| P40 | autonomous decomposition capped at 2 hops | +200 | 6 (3 new) |

Plus 5 goal-fit cleanup iters (209-213) and one docstring refresh
(iter215). Total deletions ~250, total additions ~2,800.

All bench probes (L1, L25, L60, L92, f1, f5 across many iterations
of qwen3.6-27b chat sessions) PASS/REDACTED post-deploy.

## Autonomy posture: before vs after

### Before (2026-05-17 morning)

> User describes a coding task. Agent starts working. Daemon dies
> (OOM, restart, crash). Conversation gone. Workspace state stays
> (P5 checkpoint), but the agent's memory of what was happening, what
> they tried, what worked, is permanently lost. Status DB row says
> "running" forever; user has no signal anything died. Even if the
> user re-triggers manually, the new agent has zero context.

### After (2026-05-17 night)

> User describes a coding task. Agent starts working. Daemon dies.
>
> - Status row flips to "stopped" within 2 minutes (P38 heartbeat
>   sweep) or instantly on restart (P36 startup reap).
> - `events.jsonl` has a `run_orphaned` row with a clear reason
>   (`stale_heartbeat` or `daemon_restart`).
> - If the user's spec said `Risk.ResumeOnRestart=true` (P37),
>   the daemon kicks a fresh run automatically on next start.
> - The new agent loop sees the full chat history (P34) AND a
>   summarized version when it would otherwise overflow (P35).
> - The workspace, working set, plan steps are exactly where the
>   prior agent left them.
>
> Agent can now decompose 3 levels deep (P40 — coordinator →
> sub-coordinator → worker), with the per-root cap protecting
> against fork bombs.
>
> When the chat agent loops on the same call 3+ times, the user
> gets an inline `[system] stuck_detected` warning (P39).

The harness now SURVIVES failures cleanly + reports state honestly +
unlocks deeper decomposition. That's the autonomous coding harness
promise made credible.

## What's been validated against real LLMs

The bench loop has been running against `vllm/qwen3.6-27b` since
2026-05-15, with probes at every iter. Key signals:

- **Chat surface stability under tool-heavy turns**: L1 (multi-file
  rename across imports) routinely runs 16-22 tool calls per turn
  with PASS. Persistence + compaction don't break this path.
- **Run-mode dogfood (f1-f8)**: failure-floor tasks all PASS through
  the chat → freeze_spec → start_run handoff.
- **Reasoning split (P33)**: `[think]` lines surface live in
  transcripts; vllm's `reasoning` field is honored on tool-calling
  turns.
- **Verify-loop discipline (C1) + post-loop backstop (iter6)**: the
  verify_missing gate fires correctly on weak-model attempts to
  declare done without verification.
- **Token accounting chain (iter133a-c)**: list_sessions / wait_agent
  show real token totals, not zeros.
- **Secret redaction (iter36a/93a/156)**: both registry-based and
  inline-shape-based scrubbing catch in production runs.

## What's NOT yet validated end-to-end

These are honest gaps. The autonomy posture is stronger but
**hasn't been stress-tested on a multi-hour real task**:

1. **P37 auto-resume live cycle**: the unit tests cover the event
   payload and the Start kick. We haven't yet run a real
   long-running spec with `ResumeOnRestart=true`, killed the
   daemon mid-run, and confirmed the auto-resumed agent
   converges. The mechanism is wired but unobserved end-to-end
   under qwen3.6-27b.

2. **P38 heartbeat false-positive rate**: 10-minute threshold was
   chosen conservatively. We haven't measured real LLM call
   distributions under load to confirm there's no false positive
   on a legitimately slow Anthropic streaming turn. Production
   telemetry will refine this.

3. **P40 depth=2 in practice**: tests confirm the cap mechanics. We
   haven't seen the chat agent organically pick "coordinator of
   coordinators" structure on a real task. Worth observing
   whether qwen3.6-27b actually uses the extra layer.

4. **Real long-run dogfood (Sev 1 from roadmap)**: still pending.
   The pre-existing dogfood docs end at 2026-04-27 (second-run-end-
   to-end). Nothing since v0.2.x release. A 1-hour run on a real
   codebase with a non-trivial task is still the gold-standard
   missing.

5. **Failure injection** (503 / 429 / partial tool_use / context
   overflow under real model output, not mock turns): not done.

## What this session opens up

With the P34-P40 base, the next credible workstreams are:

**A. Long-run real-LLM dogfood**. Pick a meaningful task (e.g.
"add a CHANGELOG entry generator that reads recent commits and
formats them per Keep-a-Changelog"). Run gil end-to-end on it via
`gil chat` against vllm/qwen3.6-27b. Don't intervene unless the
agent asks. Record the trace.

**B. Auto-resume live verification**. Build a spec with
`ResumeOnRestart=true` and a deliberately long task. Kill daemon
at random points; verify the resume completes the task. Document
failure modes.

**C. Subagent decomposition observability**. Build a task that
naturally decomposes 3 levels (e.g. "refactor all uses of X in
modules A/B/C"). Watch whether qwen3.6-27b uses depth=2 or
flattens. Whichever happens, document why.

**D. Failure injection**. Set up VCR cassettes for anthropic /
openai / openrouter. Inject 503/429 mid-stream and confirm
retry/compact/stuck-detect/verify-loop all behave.

These can each be a focused phase. None is huge.

## Commit chain (this session)

```
82b04ed P40 subagent depth lift
cdc6b4d P39 chat-side stuck detection
ebc7c00 P38 mid-session heartbeat sweeper
7f4278f P37 opt-in auto-resume
adb2fa0 P36 orphan run reaping
e61cec8 iter215 session_prompt docstring
1582f29 P34 persistent chat history
b0155ca iter213 dead Config.Router
5cc586b iter212 show_spec description
8ed2472 iter211 interview residue cleanup
8cfe717 iter210 slash exit removal
940940b iter209 dead intent router cleanup
f91ec41 P35 chat history compaction
```

(P35 chronologically lands between P34 and P36 in the commit log
above as `f91ec41 → 1582f29 → adb2fa0`, listed out of HEAD order.)

## What the bench loop knows

DB state at snapshot time:

- 9 chat sessions persisted via P34 write-through
- 79 chat_messages rows
- Longest session: 17 rows (well below compaction threshold)
- Zero stale heartbeats during P38 sweeper's first 20 minutes of
  operation
- One P36 orphan reaped at deploy time (synthetic test session
  01KRR6QR9VJE26XR6Q3V2G7G0R)

The bench harness still ticks. Next iter: 217.
