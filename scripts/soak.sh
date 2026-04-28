#!/usr/bin/env bash
# 24h soak harness — runs N gil sessions sequentially with rotating tasks.
# Goal: validate gil's stability over many-hour runs.
#
# Usage:
#   ./scripts/soak.sh [--tasks-file TASKS] [--num N] [--provider P] [--model M]
#
# Tasks file format (one per line):
#   <task-name>|<spec-yaml-path>
#
# Example:
#   ./scripts/soak.sh --tasks-file scripts/soak-tasks.txt --num 50
#
# Output:
#   $SOAK_HOME/run-<timestamp>/
#     ├── tasks/<i>.spec.yaml      # spec used
#     ├── tasks/<i>.events.jsonl    # event log
#     ├── tasks/<i>.cost.json       # cost + tokens + status
#     └── summary.csv               # one row per task
#
# Author: mindungil <alswnsrlf12@naver.com>
# Phase: 23.B

set -euo pipefail

TASKS_FILE=""
NUM=20
PROVIDER="vllm"
MODEL="qwen3.6-27b"
SOAK_HOME="${SOAK_HOME:-/tmp/gil-soak-$(date +%Y%m%d-%H%M%S)}"
GIL_BIN="${GIL_BIN:-./bin/gil}"
HELPER_DIR="${HELPER_DIR:-./tests/e2e/helpers}"

usage() {
  cat <<EOF
soak.sh — gil multi-task soak harness

Options:
  --tasks-file FILE   path to task list (default: synthetic 5-task rotation)
  --num N             total tasks to run (default: 20)
  --provider P        provider name (default: vllm)
  --model M           model id (default: qwen3.6-27b)
  --soak-home DIR     output base dir (default: \$SOAK_HOME)

Synthetic task rotation (when no --tasks-file given):
  T1 — write hello.txt
  T2 — fibonacci.go
  T3 — reverse string
  T4 — simple grep
  T5 — multi-file refactor (small)

Each task gets a fresh \$GIL_HOME under \$SOAK_HOME/gil-N/.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --tasks-file) TASKS_FILE="$2"; shift 2 ;;
    --num) NUM="$2"; shift 2 ;;
    --provider) PROVIDER="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --soak-home) SOAK_HOME="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 1 ;;
  esac
done

mkdir -p "$SOAK_HOME"
echo "soak-home: $SOAK_HOME"
echo "summary: $SOAK_HOME/summary.csv"
echo "i,task,status,iters,tokens,cost_usd,wall_seconds" > "$SOAK_HOME/summary.csv"

# Synthetic tasks if no --tasks-file
synthetic_spec() {
  local idx="$1" workspace="$2"
  local rot=$(( idx % 5 ))
  cat <<EOF
specId: soak-$idx
sessionId: __filled__
goal:
  oneLiner: "soak task $idx (rotation $rot)"
  successCriteriaNatural:
    - "verifier passes"
constraints:
  techStack: [bash]
verification:
  checks:
    - name: target-exists
      kind: SHELL
      command: test -f $workspace/soak-task-$idx.txt
workspace:
  backend: LOCAL_NATIVE
  path: $workspace
models:
  main:
    provider: $PROVIDER
    modelId: $MODEL
budget:
  maxIterations: 8
  maxTotalTokens: 80000
  reserveTokens: 8000
risk:
  autonomy: ASK_DESTRUCTIVE_ONLY
EOF
}

# Run a single task
run_task() {
  local i="$1"
  local task_home="$SOAK_HOME/gil-$i"
  local workspace="$SOAK_HOME/ws-$i"
  mkdir -p "$task_home" "$workspace"

  export GIL_HOME="$task_home"

  # Create session
  local sid
  sid="$($GIL_BIN new --working-dir "$workspace" 2>&1 | awk '{print $NF}')"

  # Write spec
  mkdir -p "$task_home/data/sessions/$sid"
  if [ -n "$TASKS_FILE" ]; then
    # User-supplied task — copy from list
    local line
    line="$(awk -v n="$((i % $(wc -l < "$TASKS_FILE"))) + 1" 'NR==n' "$TASKS_FILE")"
    local spec_path="${line##*|}"
    sed "s|__SESSION__|$sid|g; s|__WORKSPACE__|$workspace|g" "$spec_path" \
      > "$task_home/data/sessions/$sid/spec.yaml"
  else
    synthetic_spec "$i" "$workspace" \
      | sed "s|__filled__|$sid|" \
      > "$task_home/data/sessions/$sid/spec.yaml"
  fi

  # Freeze
  (cd "$(dirname "$0")/.." && go run "$HELPER_DIR/setfrozen.go" "$task_home/data/sessions.db" "$sid")

  # Run + capture
  local t0=$(date +%s)
  local out
  out="$($GIL_BIN run "$sid" --provider "$PROVIDER" --model "$MODEL" 2>&1 || true)"
  local t1=$(date +%s)
  local wall=$((t1 - t0))

  # Save artifacts
  echo "$out" > "$task_home/run.log"
  cp "$task_home/data/sessions/$sid/events/events.jsonl" "$task_home/events.jsonl" 2>/dev/null || true

  # Parse status / iters / tokens from output
  local status iters tokens
  status="$(echo "$out" | awk -F: '/^Status:/{gsub(/^ +/, "", $2); print $2}')"
  iters="$(echo "$out" | awk -F: '/^Iterations:/{gsub(/^ +/, "", $2); print $2}')"
  tokens="$(echo "$out" | awk -F: '/^Tokens:/{gsub(/^ +/, "", $2); print $2}')"

  # Cost from gil cost
  local cost
  cost="$(GIL_HOME=$task_home $GIL_BIN cost "$sid" --output json 2>/dev/null \
    | python3 -c "import json,sys;print(json.load(sys.stdin).get('cost_usd',0))" 2>/dev/null || echo 0)"

  echo "$i,task-$i,$status,$iters,$tokens,$cost,$wall" >> "$SOAK_HOME/summary.csv"
  echo "[$i/$NUM] $status iters=$iters tokens=$tokens cost=\$$cost wall=${wall}s"
}

# Main loop
for i in $(seq 1 "$NUM"); do
  run_task "$i" || echo "[$i] failed"
done

# Summary
echo
echo "=== soak summary ==="
echo "tasks total: $NUM"
echo "by status:"
awk -F, 'NR>1{c[$3]++}END{for(s in c)printf "  %s: %d\n", s, c[s]}' "$SOAK_HOME/summary.csv"
echo
echo "totals:"
awk -F, 'NR>1{t+=$5; c+=$6; w+=$7}END{printf "  tokens: %d\n  cost_usd: %.4f\n  wall_total_s: %d\n", t, c, w}' \
  "$SOAK_HOME/summary.csv"
echo
echo "summary: $SOAK_HOME/summary.csv"
