# gil eval task suite

Each subdirectory is one autonomous task that the harness runs end-to-end
via `gil dogfood`. The suite driver (`../run-suite.sh`) iterates these
and reports per-task verdicts plus a final tally.

## Directory layout per task

```
NN-name/
├── PROMPT.md     # natural-language task (fed to gil dogfood)
├── meta.sh       # TASK_NAME, MAX_TURNS, MAX_WALL, ASSERTS=(...)
└── seed/         # optional; copied into the workspace before agent starts
```

`meta.sh` is sourced by the driver — keep it shell-safe (no inline output,
no side effects).

`seed/` exists for tasks that begin from an existing codebase: bug-fix
(seed has the buggy parser), refactor (seed has the to-be-refactored
file + tests). Tasks that start from scratch have no `seed/`.

## Assert template

Every task should include the three baseline asserts (use `! grep '^--- FAIL:'`
form so vacuous-pass and individual-test-FAIL both surface):

```sh
ASSERTS=(
    "find . -name '*_test.go' -type f | grep ."
    "timeout 60s go test ./..."
    "! timeout 60s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
)
```

Wrap with `-race` for concurrency tasks. Append task-specific structural
checks (e.g. `test -f tiers.go` for refactor). Always include a `timeout`
prefix so a hung implementation can't burn the whole budget — see
task-surface.md Finding #5.

## Running

```sh
bash docs/eval/run-suite.sh                # all tasks
bash docs/eval/run-suite.sh 03-bug-fix     # one task
bash docs/eval/run-suite.sh "0[1-3]-*"     # glob
```

`OUT_DIR=...` overrides the trace + log directory (default `/tmp/gil-eval-suite-$$`).

`GIL_BIN=...` overrides the gil binary (default `which gil`).

## Status

Current PASS-known set: tasks 01-10 (slates 1-2 confirmed PASS in
prior runs). See `../task-surface.md` for per-task results and findings.

Not yet wired into this suite: tasks 09-20 (slates 2-5). Pattern is
straightforward — copy PROMPT.md, add meta.sh, optionally seed/.
