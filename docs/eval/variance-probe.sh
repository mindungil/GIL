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
#   bash docs/eval/variance-probe.sh 5 07 0.3  # ditto, override T=0.3
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
TEMPERATURE=${3:-0}  # 0 = use daemon default (0.7)

OUT_DIR=${OUT_DIR:-/tmp/gil-variance-probe-$$}
mkdir -p "$OUT_DIR"
echo "variance-probe: N=$N filter=$FILTER temperature=$TEMPERATURE traces=$OUT_DIR"

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
    local temp_args=()
    if [ "$TEMPERATURE" != "0" ]; then
        temp_args=(--temperature "$TEMPERATURE")
    fi
    if "$GIL_BIN" dogfood "$prompt" \
        --working-dir "$ws" \
        --max-turns "$maxturns" \
        --max-wall "$maxwall" \
        --trace "$trace" \
        "${temp_args[@]}" \
        "${assert_args[@]}" > "$OUT_DIR/${id}-r${run}.log" 2>&1; then
        verdict="PASS"
    else
        verdict="FAIL"
    fi
    local end_ts=$(date +%s)
    local wall=$((end_ts - start_ts))

    # Extract from trace: summary record (tail) + per-turn records (all).
    # Metric coverage (3 축):
    #   정확도:
    #     verdict      = PASS/FAIL (모든 assert 통과)
    #   context 유지도 (proxy only — see CAVEAT below):
    #     max_turn_tok = 한 user turn에서 daemon이 누적 보낸 input token 최대값.
    #                    CAVEAT: 이건 **per-turn 누적**임 (한 turn에 N번 Complete()
    #                    호출, 각 호출이 history 다 보냄 → 합산). single-call
    #                    window pressure가 아님. 진짜 context-pressure 측정은
    #                    daemon 쪽 per-Complete 인스트루멘테이션 필요 (별도 work).
    #     overflow     = 1 if 어떤 turn이 stop_reason=error로 종료. context
    #                    overflow가 진짜로 터졌는지의 binary signal (이건 신뢰 가능).
    #   완성도:
    #     recov        = dogfood가 주입한 recovery prompt 횟수 (= turns - 1)
    #     premature    = 1 if verdict==FAIL AND final_stop==end_turn
    #                    (agent가 "다 했다" 착각했는데 assert FAIL)
    local stats
    stats=$(python3 - "$trace" <<'PY'
import json, sys
path = sys.argv[1]
turns = []
summary = None
with open(path) as f:
    for line in f:
        try:
            d = json.loads(line)
        except Exception:
            continue
        if d.get("summary"):
            summary = d
        elif "turn" in d:
            turns.append(d)
if summary is None:
    summary = {}
max_turn_tok = max((t.get("tokens_in", 0) or 0) for t in turns) if turns else 0
total_turns = summary.get("turns", len(turns))
recov = max(0, total_turns - 1) if total_turns else 0
verdict = summary.get("verdict", "?")
final_stop = summary.get("final_stop", "?")
premature = 1 if (verdict == "FAIL" and final_stop == "end_turn") else 0
overflow = 0
for t in turns:
    sr = t.get("stop_reason", "")
    if sr == "error":
        overflow = 1
        break
print(f"{total_turns}|{summary.get('total_in_tok',0)}|{summary.get('total_out_tok',0)}|{max_turn_tok}|{recov}|{premature}|{overflow}")
PY
)
    IFS='|' read -r turns in_tok out_tok max_turn_tok recov premature overflow <<< "$stats"

    printf '[%s r%d] %s turns=%s wall=%ds in=%s max-turn-tok=%s recov=%s prem=%s ovf=%s\n' \
        "$id" "$run" "$verdict" "$turns" "$wall" "$in_tok" "$max_turn_tok" "$recov" "$premature" "$overflow"
    rm -rf "$ws"

    # Append CSV-style line.
    printf '%s,%d,%s,%s,%d,%s,%s,%s,%s,%s\n' \
        "$id" "$run" "$verdict" "$turns" "$wall" "$in_tok" "$max_turn_tok" "$recov" "$premature" "$overflow" \
        >> "$OUT_DIR/results.csv"
}

# Header for CSV.
echo "task,run,verdict,turns,wall_s,in_tok,max_turn_tok,recov,premature,overflow" > "$OUT_DIR/results.csv"

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
echo "=== variance summary (N=$N, T=$TEMPERATURE) ==="
echo
echo "정확도 + context 유지도 + 완성도 — 한 표:"
echo
echo "| Task | PASS/N | turns | wall | max-turn-tok | recov | prem-stop | ovf |"
echo "|---|---|---|---|---|---|---|---|"
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
def rng_int(vals):
    nums = [int(v) for v in vals if v.lstrip('-').isdigit()]
    if not nums: return "?"
    if len(set(nums))==1: return str(nums[0])
    return f"{min(nums)}-{max(nums)}"
def sum_int(vals):
    return sum(int(v) for v in vals if v.lstrip('-').isdigit())
for task, runs in by_task.items():
    npass = sum(1 for r in runs if r["verdict"]=="PASS")
    turns_r = rng_int([r["turns"] for r in runs])
    wall_r = rng_int([r["wall_s"] for r in runs])
    mtt_r = rng_int([r["max_turn_tok"] for r in runs])
    recov_r = rng_int([r["recov"] for r in runs])
    prem = sum_int([r["premature"] for r in runs])
    ovf = sum_int([r["overflow"] for r in runs])
    print(f"| {task} | {npass}/{len(runs)} | {turns_r} | {wall_r}s | {mtt_r} | {recov_r} | {prem}/{len(runs)} | {ovf}/{len(runs)} |")
PYEOF
echo
echo "축 정의:"
echo "  PASS/N        = 정확도 (모든 assert 통과)"
echo "  max-turn-tok  = 한 turn에서 daemon이 누적 보낸 input token. CAVEAT:"
echo "                  per-turn 누적 (한 turn에 N번 Complete 호출 → 합산).  "
echo "                  진짜 single-call window pressure가 아님. daemon 인스트루멘테이션 필요."
echo "  recov         = dogfood가 주입한 recovery prompt 횟수 (= turns-1) — 완성도 proxy"
echo "  prem-stop     = end_turn인데 assert FAIL (agent가 'done' 착각) — 완성도 핵심"
echo "  ovf           = context overflow로 stop_reason=error (0이어야 정상) — context 신뢰 가능 signal"
echo
echo "raw results: $OUT_DIR/results.csv"
echo
echo "raw results: $OUT_DIR/results.csv"
