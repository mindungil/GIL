#!/usr/bin/env bash
# variance-probe.sh — repeated-run variance driver for boundary tasks.
#
# Why this exists: the 2026-05-18 ceiling probe identified 3 tasks
# where qwen3.6-27b + default T=0.7 fails 0-1/3 attempts (chess perft,
# bytecode-vm, spmc-queue). Before wiring AdversaryConsultStrategy
# through the chat path ([[gil-adversary-seam]]), we need a real
# baseline pass-rate to beat. Single-shot results in task-surface.md
# are not enough — model variance dominates.
#
# Runs each task N times, captures pass/fail + turns + wall + tokens
# per run, prints a markdown table at the end. Does NOT touch
# task-surface.md — user pastes the table by hand.
#
# Usage:
#   bash docs/eval/variance-probe.sh           # N=2 smoke (default)
#   bash docs/eval/variance-probe.sh 5         # N=5 per task
#   bash docs/eval/variance-probe.sh 5 07      # only chess perft, N=5
#
# Output: per-run line + final per-task table.

set -uo pipefail

GIL_BIN=${GIL_BIN:-$(command -v gil || echo /usr/local/bin/gil)}
if [ ! -x "$GIL_BIN" ]; then
    echo "variance-probe: gil binary not found (set GIL_BIN)" >&2
    exit 2
fi

N=${1:-2}
FILTER=${2:-all}

OUT_DIR=${OUT_DIR:-/tmp/gil-variance-probe-$$}
mkdir -p "$OUT_DIR"
echo "variance-probe: N=$N filter=$FILTER traces=$OUT_DIR"

# Per-task config: name|prompt-path|max-turns|max-wall|asserts (newline-separated, ::-escaped)
# Boundary tasks live in /home/ubuntu/eval/, NOT docs/eval/tasks/ (the
# 14-task regression suite excludes the known FAILs).
# Asserts use the 3-layer template from docs/eval/run-suite.sh
# (test file exists + tests run + no FAIL lines). Multiple asserts
# separated by the literal token __SPLIT__ (avoids collision with
# common shell metacharacters in assert bodies).
TASKS=(
  "07-chess|/home/ubuntu/eval/task07-chess-perft/PROMPT.md|40|60m|find . -name '*_test.go' -type f | grep .__SPLIT__timeout 120s go test ./...__SPLIT__! timeout 120s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
  "11-vm|/home/ubuntu/eval/task11-bytecode-vm/PROMPT.md|30|30m|find . -name '*_test.go' -type f | grep .__SPLIT__timeout 120s go test ./...__SPLIT__! timeout 120s go test -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
  "17-spmc|/home/ubuntu/eval/task17-spmc-queue/PROMPT.md|30|30m|find . -name '*_test.go' -type f | grep .__SPLIT__timeout 120s go test -race ./...__SPLIT__! timeout 120s go test -race -count=1 -v ./... 2>&1 | grep -q '^--- FAIL:'"
)

run_task() {
    local id=$1 prompt=$2 maxturns=$3 maxwall=$4 asserts=$5 run=$6
    local ws trace verdict turns wall_ms cost in_tok out_tok
    ws=$(mktemp -d)
    trace="$OUT_DIR/${id}-r${run}.jsonl"

    local assert_args=()
    # Split asserts on the literal token __SPLIT__ via a tempfile
    # (sed/bash word-splitting tricks all mangle the shell metachars in
    # the assert bodies). Read each line back as one --assert arg.
    local atmp
    atmp=$(mktemp)
    printf '%s\n' "$asserts" | python3 -c 'import sys,re
data=sys.stdin.read()
for part in re.split(r"__SPLIT__", data):
    part = part.strip("\n")
    if part:
        print(part)' > "$atmp"
    while IFS= read -r a; do
        [ -z "$a" ] && continue
        assert_args+=(--assert "$a")
    done < "$atmp"
    rm -f "$atmp"

    local start_ts=$(date +%s)
    if "$GIL_BIN" dogfood "$prompt" \
        --working-dir "$ws" \
        --max-turns "$maxturns" \
        --max-wall "$maxwall" \
        --trace "$trace" \
        "${assert_args[@]}" > "$OUT_DIR/${id}-r${run}.log" 2>&1; then
        verdict="PASS"
    else
        verdict="FAIL"
    fi
    local end_ts=$(date +%s)
    local wall=$((end_ts - start_ts))

    # Extract from trace tail (summary record).
    local summary
    summary=$(tail -1 "$trace" 2>/dev/null || echo '{}')
    turns=$(echo "$summary" | python3 -c 'import sys,json;d=json.loads(sys.stdin.read());print(d.get("turns","?"))' 2>/dev/null || echo "?")
    in_tok=$(echo "$summary" | python3 -c 'import sys,json;d=json.loads(sys.stdin.read());print(d.get("total_in_tok","?"))' 2>/dev/null || echo "?")
    out_tok=$(echo "$summary" | python3 -c 'import sys,json;d=json.loads(sys.stdin.read());print(d.get("total_out_tok","?"))' 2>/dev/null || echo "?")

    printf '[%s r%d] %s turns=%s wall=%ds in=%s out=%s\n' "$id" "$run" "$verdict" "$turns" "$wall" "$in_tok" "$out_tok"
    rm -rf "$ws"

    # Append CSV-style line to OUT_DIR/results.csv for table rendering.
    printf '%s,%d,%s,%s,%d,%s,%s\n' "$id" "$run" "$verdict" "$turns" "$wall" "$in_tok" "$out_tok" >> "$OUT_DIR/results.csv"
}

# Header for CSV.
echo "task,run,verdict,turns,wall_s,in_tok,out_tok" > "$OUT_DIR/results.csv"

for spec in "${TASKS[@]}"; do
    IFS='|' read -r id prompt maxturns maxwall asserts <<< "$spec"
    case "$FILTER" in
        all) ;;
        "$id") ;;
        *) [[ "$id" == "$FILTER"* ]] || continue ;;
    esac
    for ((r=1; r<=N; r++)); do
        run_task "$id" "$prompt" "$maxturns" "$maxwall" "$asserts" "$r"
    done
done

echo
echo "=== variance summary (N=$N) ==="
echo
echo "| Task | PASS / N | turns (range) | wall (range) | in-tok (range) |"
echo "|---|---|---|---|---|"
python3 <<PYEOF
import csv, collections
rows = []
with open("$OUT_DIR/results.csv") as f:
    r = csv.DictReader(f)
    for row in r:
        rows.append(row)
by_task = collections.OrderedDict()
for row in rows:
    by_task.setdefault(row["task"], []).append(row)
def rng(vals):
    nums = [int(v) for v in vals if v.isdigit()]
    if not nums: return "?"
    if len(set(nums))==1: return str(nums[0])
    return f"{min(nums)}-{max(nums)}"
for task, runs in by_task.items():
    npass = sum(1 for r in runs if r["verdict"]=="PASS")
    turns_r = rng([r["turns"] for r in runs])
    wall_r = rng([r["wall_s"] for r in runs])
    intok_r = rng([r["in_tok"] for r in runs])
    print(f"| {task} | {npass}/{len(runs)} | {turns_r} | {wall_r}s | {intok_r} |")
PYEOF
echo
echo "raw results: $OUT_DIR/results.csv"
