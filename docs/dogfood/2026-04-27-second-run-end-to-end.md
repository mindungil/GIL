# Second Run — full gild loop end-to-end

**Date:** 2026-04-27
**Scope:** drive a real session through `gil new` → spec freeze → `gil run`,
observe the same code paths qwen would exercise, and record what trips.
**Driver:** `bin/gil` + `bin/gild`, no fixture binary; same surface a user gets.

The first-run report (`2026-04-27-first-real-run-qwen.md`) validated the
`provider.OpenAI` adapter against the user's vLLM-served qwen3.6-27b. That
covered the wire and the tool-use contract, but it bypassed gild — three direct
`p.Complete(...)` calls from a smoke binary. This second run closes the loop:
session create, spec freeze, agent loop, tool dispatch, sandboxing, verifier,
checkpoints, memory milestone, all under the daemon.

To keep the run reproducible without touching the user's endpoint, the model
side is stubbed via `GIL_MOCK_MODE=run-hello` — a 2-turn scripted provider
(`provider.MockToolProvider`) that calls `write_file` then ends the turn. The
provider boundary is the only thing mocked; everything downstream of it
(agent loop, tool router, sandbox, verifier, shadow git, milestone summarizer)
is the production code path. Swapping `--provider mock` for `--provider vllm`
points the same binary at the qwen endpoint with no other diff.

## Setup

```text
gild     = bin/gild           (built from develop @ 341bf91)
gil      = bin/gil            (same)
GIL_HOME = $(mktemp -d)       hermetic per-run
WORK     = $(mktemp -d)       fresh empty workspace
SOCK     = $GIL_HOME/state/gild.sock
```

`auth login` was a no-op for this run (mock provider needs no credential).
For the live qwen variant the operator runs
`gil auth login vllm --base-url <redacted> --api-key <redacted>` once; that
plumbing was already validated in the first-run report and is unchanged.

## Task

> create `hello.txt` in the workspace.

Frozen spec (`spec.yaml`, written directly because the e2e helper bypasses the
interview to keep this run hermetic):

```yaml
specId: dogfood-2026-04-27-second
goal:
  oneLiner: create hello.txt with the date
  successCriteriaNatural:
    - file hello.txt exists in the workspace
verification:
  checks:
    - name: exists
      kind: SHELL
      command: test -f $WORK/hello.txt
workspace:
  backend: LOCAL_NATIVE
  path: $WORK
models:
  main:
    provider: mock
    modelId: mock-model
budget:
  maxIterations: 5
risk:
  autonomy: FULL
```

Status flipped to `frozen` via `tests/e2e/helpers/setfrozen.go` (a 12-line
sqlite UPDATE) — the same shortcut the existing `phase12_in_session_ux_test`
uses to skip the conversational interview.

## What the agent did

Turn 1 (input 10 / output 18 tokens, stop_reason `tool_use`):
- emits one `tool_use` for `write_file{path:"hello.txt", content:"hello\n"}`.
- environment (`source=3`, kind `OBSERVATION`) returns
  `wrote 6 bytes to hello.txt`, `is_error=false`.
- shadow-git checkpoint commits sha `7281887`.

Turn 2 (input 30 / output 5 tokens, stop_reason `end_turn`):
- agent emits `"Done."` text, no tool calls, ends the turn.
- verifier runs the lone shell check (`test -f hello.txt`) → exit 0 →
  `verify_result{passed:true}`.
- final checkpoint sha `4e118c3` committed with `final:true`.

Total wall-clock from `gil run` invocation to `Status: done` print:
**~80 ms**. Iteration count: 2. Token total: 63 (10+18+30+5). The CLI
`run` summary was:

```text
Status:     done
Iterations: 2
Tokens:     63
Verify results:
  ✓ exists (exit=0)
```

Workspace post-run:

```text
$ ls -la $WORK
-rw-r--r-- 1 ubuntu ubuntu 6 Apr 27 14:39 hello.txt
$ cat $WORK/hello.txt
hello
```

## What worked

- **Session lifecycle.** `gil new` → status `created`; helper flips to
  `frozen`; `gil run` accepts. The `must be frozen before run (current
  status: created)` precondition error fires correctly when freeze is
  skipped — it caught a typo in an earlier draft of this report.
- **Tool routing.** `write_file` reached the local-native sandbox, wrote 6
  bytes, and surfaced a clean `tool_result` event the next turn could see.
- **Verifier.** The shell-check runner shelled out, captured exit 0, and
  emitted `verify_result{passed:true}`. The runner did NOT continue past
  the verifier — exactly what `risk.autonomy=FULL` + a passing check should
  produce. (A failing check would loop another iteration up to
  `budget.maxIterations`.)
- **Shadow git checkpoints.** Two commits landed in
  `data/sessions/<id>/shadow/<hash>/.git`. The final commit was tagged
  `final:true` so `gil restore` has a clean rollback target.
- **Event log.** All 17 events written to `events/events.jsonl`,
  newline-delimited JSON, with monotonic ids and RFC3339 timestamps.
  `gil events` and `gil watch` consume the same file.
- **mcp_registry_loaded** fired with `server_count: 0` (no MCP servers
  registered in this hermetic run). The empty-registry branch executed
  without falling over.

## Bugs / quirks found

One real issue, surfaced for the first time by this dogfood:

- **`memory_milestone_error: mock-tool provider turns exhausted`** at event
  15. The milestone summarizer fires on `end_turn`, calls back into the
  provider for a one-shot summary, and the mock provider raised "turns
  exhausted" because the scripted scenario only had two turns budgeted.
  The runner correctly logged it as a non-fatal note and proceeded to
  `run_done`. Two follow-ups for **Phase 17**:

  1. The mock-mode scripted scenario should pre-allocate a 3rd turn (or a
     dedicated `summaryProv`) so milestone runs don't error on every
     `run-hello` invocation.
  2. On the production path, the milestone should fall back to an empty
     summary when the provider call fails, not a silent error log — a real
     network blip mid-milestone should not poison the event stream. The
     `summaryProv` plumbing already exists in `core/runner` (see
     `runner_test.go:724,801`); it just isn't wired through the daemon
     factory yet.

No other regressions. No stuck-detector trips, no permission denials,
no compactor triggers (well below context window).

## Where this leaves the qwen path

The boundary the live qwen run would cross is exactly one swap:
`--provider mock` → `--provider vllm`. Everything in the event log above
(`provider_request`, `provider_response`, `tool_call`, `tool_result`,
`verify_run`, `verify_result`, `checkpoint_committed`, `run_done`) is
provider-agnostic — the OpenAI adapter feeds the same `provider.Response`
shape. The first-run report independently validated that adapter against
qwen3.6-27b (text completion + tool-use + tool-result follow-up + parallel
tool calls), so the only remaining unknown for the full e2e qwen run is
**latency under the agent loop with real network round trips**. From the
first-run numbers (~1–4 s per provider call) and 2 iterations here, a live
qwen run of this same task should land near **3–8 s wall-clock** end to
end, plus whatever the milestone summarizer adds. That's a hypothesis,
not a measurement; it gets confirmed (or refuted) the next time the user
points the daemon at the live endpoint.

## Honest assessment

What worked: the daemon end-to-end. New session, freeze, run, verify,
checkpoint, event-stream — all behaved. The CLI summary printed
`Status: done` with correct iteration / token / verify counts. No code
changes were needed to make this run.

What's rough: the milestone summarizer's failure mode (above). Also, the
helper script for freeze is a sqlite UPDATE, which is fine for tests but
means the conversational `gil interview` path against a weaker model
(qwen) is still untested end-to-end for spec extraction. The first-run
report flagged qwen's verbose preambles at low `max_tokens`; the
interview's `Adversary` step does several short questions, and a model
that opens with `\n\n1. Let me think about this...` may hit the cap
before answering. That's the next thing to dogfood, after Phase 16
ecosystem activation lands.

## How to repro

```bash
make build

BASE=$(mktemp -d); WORK=$(mktemp -d); SOCK=$BASE/state/gild.sock
export GIL_HOME=$BASE
GIL_MOCK_MODE=run-hello bin/gild --foreground --base $BASE &

# wait for socket, then:
ID=$(bin/gil new --working-dir $WORK --socket $SOCK | awk '{print $3}')

# write spec.yaml + flip status (see Setup above for the full yaml)
mkdir -p $BASE/data/sessions/$ID
cat > $BASE/data/sessions/$ID/spec.yaml <<EOF
... (full spec)
EOF
(cd core && go run ../tests/e2e/helpers/setfrozen.go \
    $BASE/data/sessions.db $ID)

bin/gil run $ID --provider mock --socket $SOCK
```

To repeat against live qwen, replace `GIL_MOCK_MODE=run-hello` with
`gil auth login vllm --base-url <url> --api-key <key>` (one time) and
swap `--provider mock` for `--provider vllm` on the run line. No other
changes.
