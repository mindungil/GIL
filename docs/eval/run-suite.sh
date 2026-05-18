#!/usr/bin/env bash
# run-suite.sh — local eval suite driver
#
# Iterates docs/eval/tasks/*/ and runs each through `gil dogfood`.
# Each task directory must contain:
#   PROMPT.md     — the natural-language task prompt
#   meta.sh       — sets TASK_NAME, MAX_TURNS, MAX_WALL, ASSERTS=(...)
#   seed/         — optional; files copied into the workspace before
#                   the agent starts (use for bug-fix and refactor
#                   tasks that need starter code)
#
# Output: per-task verdict line + final tally. Exits non-zero if any
# task FAILed.
#
# Usage:
#   bash docs/eval/run-suite.sh [TASK_PATTERN]
#   bash docs/eval/run-suite.sh 03-bug-fix    # one task by name
#   bash docs/eval/run-suite.sh "*"           # all tasks (default)

set -uo pipefail

GIL_BIN=${GIL_BIN:-$(command -v gil || echo /usr/local/bin/gil)}
if [ ! -x "$GIL_BIN" ]; then
    echo "run-suite: gil binary not found (set GIL_BIN or install)" >&2
    exit 2
fi

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
TASKS_DIR=$REPO_ROOT/docs/eval/tasks
# Each positional arg is a glob expanded against $TASKS_DIR. Defaults
# to "*" if no args given. Repeated args run all matched tasks in order.
if [ $# -eq 0 ]; then
    PATTERNS=("*")
else
    PATTERNS=("$@")
fi

if [ ! -d "$TASKS_DIR" ]; then
    echo "run-suite: $TASKS_DIR does not exist" >&2
    exit 2
fi

# Optional output dir for traces (one JSONL per task).
OUT_DIR=${OUT_DIR:-/tmp/gil-eval-suite-$$}
mkdir -p "$OUT_DIR"
echo "run-suite: traces → $OUT_DIR"

PASS=0
FAIL=0
SKIP=0
declare -a FAILED_NAMES

seen=""
declare -a TASK_DIRS
for pat in "${PATTERNS[@]}"; do
    for t in "$TASKS_DIR"/$pat/; do
        [ -d "$t" ] || continue
        if [[ ":$seen:" == *":$t:"* ]]; then
            continue
        fi
        seen="$seen:$t"
        TASK_DIRS+=("$t")
    done
done

for task in "${TASK_DIRS[@]}"; do
    [ -d "$task" ] || continue
    name=$(basename "$task")
    if [ ! -f "$task/PROMPT.md" ] || [ ! -f "$task/meta.sh" ]; then
        echo "[$name] SKIP — missing PROMPT.md or meta.sh"
        SKIP=$((SKIP + 1))
        continue
    fi

    # Fresh workspace per task.
    ws=$(mktemp -d)
    if [ -d "$task/seed" ]; then
        cp -r "$task/seed/." "$ws/"
    fi

    # Source meta.sh in a subshell so vars don't leak between tasks.
    (
        # shellcheck disable=SC1091
        source "$task/meta.sh"
        : "${TASK_NAME:=$name}"
        : "${MAX_TURNS:=15}"
        : "${MAX_WALL:=20m}"

        # Build --assert array.
        assert_args=()
        for a in "${ASSERTS[@]:-}"; do
            assert_args+=(--assert "$a")
        done

        trace_file="$OUT_DIR/${name}.jsonl"
        printf '[%s] start (max_turns=%d max_wall=%s)\n' "$TASK_NAME" "$MAX_TURNS" "$MAX_WALL"
        if "$GIL_BIN" dogfood "$task/PROMPT.md" \
            --working-dir "$ws" \
            --max-turns "$MAX_TURNS" \
            --max-wall "$MAX_WALL" \
            --trace "$trace_file" \
            "${assert_args[@]}" > "$OUT_DIR/${name}.log" 2>&1; then
            verdict=$(tail -1 "$trace_file" | python3 -c \
                "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('verdict','?'))" 2>/dev/null || echo '?')
            printf '[%s] PASS verdict=%s\n' "$TASK_NAME" "$verdict"
        else
            verdict=$(tail -1 "$trace_file" | python3 -c \
                "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('verdict','FAIL'))" 2>/dev/null || echo 'FAIL')
            printf '[%s] FAIL verdict=%s (log: %s/%s.log)\n' "$TASK_NAME" "$verdict" "$OUT_DIR" "$name"
            exit 1
        fi
    )
    rc=$?
    rm -rf "$ws"
    if [ $rc -eq 0 ]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        FAILED_NAMES+=("$name")
    fi
done

echo
echo "=== suite summary ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
echo "SKIP: $SKIP"
if [ "$FAIL" -gt 0 ] && [ "${#FAILED_NAMES[@]}" -gt 0 ]; then
    echo "failed: ${FAILED_NAMES[*]}"
fi
echo "traces: $OUT_DIR"

if [ $FAIL -gt 0 ]; then
    exit 1
fi
