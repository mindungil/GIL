#!/usr/bin/env bash
# Phase 18 Track A e2e: plan tool flow.
#
# Scenario: scripted mock provider (GIL_MOCK_MODE=run-plan) walks the
# agent through a 3-item plan: set, mark in_progress, do work, mark
# completed, repeat. By the end the on-disk plan.json should have all
# three items completed and the events.jsonl should carry several
# plan_updated events.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/state/gild.sock"
WORK="$(mktemp -d)"
PATH="$ROOT/bin:$PATH"
export PATH

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# 1. Start gild with mock-mode
GIL_MOCK_MODE=run-plan "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Create session + frozen spec
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p18
sessionId: $ID
goal:
  oneLiner: walk the plan tool through 3 steps
  successCriteriaNatural:
    - all three plan-step files exist
constraints:
  techStack:
    - bash
verification:
  checks:
    - name: step1
      kind: SHELL
      command: test -f $WORK/plan-step-1.txt
    - name: step2
      kind: SHELL
      command: test -f $WORK/plan-step-2.txt
    - name: step3
      kind: SHELL
      command: test -f $WORK/plan-step-3.txt
workspace:
  backend: LOCAL_NATIVE
  path: $WORK
models:
  main:
    provider: mock
    modelId: mock-model
budget:
  maxIterations: 12
risk:
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 3. Run synchronously
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$RUN_OUT"

echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }

# 4. plan.json must exist with all 3 items completed
PLAN_FILE="$BASE/data/sessions/$ID/plan.json"
[ -f "$PLAN_FILE" ] || { echo "FAIL: plan.json not created at $PLAN_FILE"; exit 1; }
echo "OK: plan.json exists"
echo "--- plan.json contents ---"
cat "$PLAN_FILE"
echo "--- /plan.json ---"

COMPLETED=$(grep -c '"status": "completed"' "$PLAN_FILE" || true)
[ "$COMPLETED" -ge 3 ] || { echo "FAIL: expected >=3 completed items, got $COMPLETED"; exit 1; }
echo "OK: all 3 items marked completed"

# 5. plan.json mode is 0644 (plan content is not secret)
MODE=$(stat -c "%a" "$PLAN_FILE" 2>/dev/null || stat -f "%Lp" "$PLAN_FILE")
[ "$MODE" = "644" ] || { echo "FAIL: plan.json mode = $MODE, want 644"; exit 1; }
echo "OK: plan.json mode is 0644"

# 6. plan_updated events should appear in events.jsonl
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no jsonl event file"; exit 1; }

PLAN_EVT_COUNT=$(grep -c '"type":"plan_updated"' "$EVENTS_FILE" || true)
# Mock scenario does: 1 set + 5 update_item = 6 plan mutations.
[ "$PLAN_EVT_COUNT" -ge 4 ] || { echo "FAIL: expected >=4 plan_updated events, got $PLAN_EVT_COUNT"; exit 1; }
echo "OK: $PLAN_EVT_COUNT plan_updated events emitted"

# 7. tool_call events for plan tool should appear. The tool name is JSON-
# escaped inside the event's data field (\"name\":\"plan\") so we grep
# loosely for "name" + "plan" on the tool_call lines.
PLAN_CALLS=$(grep '"type":"tool_call"' "$EVENTS_FILE" | grep -c 'name.*plan' || true)
[ "$PLAN_CALLS" -ge 6 ] || { echo "FAIL: expected >=6 plan tool_call events, got $PLAN_CALLS"; exit 1; }
echo "OK: $PLAN_CALLS plan tool calls"

# 8. The workspace files from the plan steps should be present
for f in plan-step-1.txt plan-step-2.txt plan-step-3.txt; do
  [ -f "$WORK/$f" ] || { echo "FAIL: missing $f"; exit 1; }
done
echo "OK: all 3 plan-step files written to workspace"

# 9. The summary CLI should now show plan progress for this session.
# The bare `gil` (no subcommand) hits runSummary which dials the default
# socket via defaultLayout(); we point it at the same BASE via GIL_HOME
# so it picks up the running daemon's session list rather than the
# user's real one.
SUMMARY_OUT=$(GIL_HOME="$BASE" "$ROOT/bin/gil" 2>&1 || true)
echo "$SUMMARY_OUT" | grep -q "plan 3/3" || {
  echo "FAIL: gil summary did not show 'plan 3/3'"
  echo "--- summary output ---"
  echo "$SUMMARY_OUT"
  echo "--- /summary ---"
  exit 1
}
echo "OK: summary surface shows 'plan 3/3'"

echo "OK: phase 18 Track A e2e — plan tool flow!"
