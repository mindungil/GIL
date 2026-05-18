# 2026-05-17 — 9hr dogfood v1: killed at 5:55 by my own redeploy

Honest gap-log entry. Documents an operator mistake + the lesson
learned, then sets up the v2 retry.

## What was attempted

P51 — first attempt at the long-run dogfood promised by the
P41 retrospective. Task: build a Markdown-to-HTML converter in Go
with lexer/parser/renderer/cli packages, corpus tests, and a
README. `timeout 32400` (9 hours) wall budget.

Started: 2026-05-17 23:52:29 UTC.

## What happened

I kicked off the dogfood in background. Then, while it was running,
I built P52 (`gil session orphans` CLI command) and **redeployed
gild to test the new command live**.

The redeploy did `pkill -9 gild` + `sudo cp gild-new /usr/local/bin/gild`
+ relaunch. The 9hr dogfood was a `gil chat` client connected to
that gild over UDS. When gild died, the client's stream got
"connection refused" — but the chat REPL just kept trying to
reconnect, writing 1000+ "send failed" lines until stdin EOF closed
the loop.

Killed time: 23:58:24 (5 min 55 sec after start).

## Lessons

1. **The daemon is a load-bearing shared resource.** Long-running
   chat sessions can't survive mid-session daemon redeploy. P36/P37/
   P38 only handle DAEMON CRASHES (where the entire daemon process
   is gone). They don't survive **intentional restart by an
   operator** mid-session.

2. **Mixed-mode development (write code + deploy + observe long-run)
   doesn't work.** When the daemon is running a long task, dev work
   that touches the binary breaks the task. Operationally these need
   to be separated:
   - Long-run dogfood = strictly observation, no daemon touches
   - Dev cycle = bench probes against a daemon you can freely bounce

3. **The chat REPL's reconnect-loop-on-disconnect is wrong.** When
   the daemon dies mid-stream, the client should EXIT with a clear
   message, not loop reconnecting forever. Documenting as a P53
   followup.

## What was salvaged

- Workspace at point of kill: `/tmp/p51-9hr-dogfood/lexer/token.go`
  (1 file written before kill).
- Agent had started the lexer package. Earlier trace showed it
  initially put everything under `md2html/` package, then mid-task
  restructured to separate `lexer/` per the prompt — adaptive
  restructuring behavior worth noting.

## v2 retry

Started fresh at 23:59:23 with the same prompt + workspace cleared.
Strict no-daemon-touch policy in force until the run completes or
the 9hr cap fires.

See `2026-05-17-9hr-dogfood-v2.md` (or the chronologically next
file) for the v2 result.

## Followup phase

**P53: `gil chat` should exit cleanly on daemon disappearance.**
Today: silent reconnect loop until stdin EOF. Want: detect
unrecoverable stream errors, emit a one-line `daemon disappeared`
message, exit with non-zero code. ~30 LOC plus tests.

Logged here so the next dev cycle (after the v2 dogfood completes)
picks it up.
