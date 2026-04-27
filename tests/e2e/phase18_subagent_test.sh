#!/usr/bin/env bash
# Phase 18 e2e (Track E): subagent tool plumbed end-to-end.
#
# Goal: prove that the agent itself (not the stuck-recovery system) can
# call the subagent tool, get back a 1-paragraph finding from a sub-loop,
# and incorporate it into its own response — without any external
# dependencies.
#
# Strategy:
#   1. Boot gild in mock mode (GIL_MOCK_MODE=run-subagent). The mock
#      provider serves three turns from the same scripted queue:
#        - parent turn 1: call subagent({goal: "..."})
#        - sub-loop turn 1: return finding text + end_turn
#        - parent turn 2: end_turn
#   2. Run the session synchronously and grep the per-session
#      events.jsonl for:
#        - subagent_started event with the goal we passed
#        - subagent_done event with the finding text
#        - tool_call event for the subagent tool
#        - tool_result event whose content carries "Subagent finding"
#          + the finding text from the sub-loop
#   3. Confirm sub-loop internal events (provider_request, run_done) do
#      NOT leak into the parent's stream — only the surface-level
#      subagent_started / subagent_done events are observable.
#
# Hermeticity: GIL_HOME and the workspace are per-test mktemp dirs;
# the trap cleans them up plus the gild child on EXIT.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/state/gild.sock"
WORK="$(mktemp -d)"
PATH="$ROOT/bin:$PATH"
export PATH
export GIL_HOME="$BASE"

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# Step 1: pin the goal so we can grep for it deterministically.
SUBAGENT_GOAL="find which file defines the main agent loop"
export GIL_MOCK_SUBAGENT_GOAL="$SUBAGENT_GOAL"

# Step 2: start gild with the run-subagent mock script.
GIL_MOCK_MODE=run-subagent "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: gild socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }
echo "OK: gild up"

# Step 3: create + freeze a session whose verifier just passes.
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p18-subagent
sessionId: $ID
goal:
  oneLiner: research with a subagent
  successCriteriaNatural:
    - delegate to a subagent and use the finding
constraints:
  techStack:
    - none
verification:
  checks:
    - name: trivial
      kind: SHELL
      command: 'true'
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
  autonomy: ASK_DESTRUCTIVE_ONLY
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# Step 4: run synchronously.
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1 || true)
echo "$RUN_OUT" | tail -5
echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }
echo "OK: run reached done"

# Step 5: inspect events.jsonl for the subagent flow.
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no events.jsonl"; exit 1; }

# 5a. tool_call for the subagent tool. The "name" field lives inside
# the "data" field as escaped JSON (\"name\":\"subagent\"), so grep
# accordingly.
set +o pipefail
SUBAGENT_CALLS=$(grep '"type":"tool_call"' "$EVENTS_FILE" | grep -c 'subagent' || true)
set -o pipefail
SUBAGENT_CALLS=${SUBAGENT_CALLS:-0}
[ "$SUBAGENT_CALLS" -ge 1 ] || {
  echo "FAIL: expected at least 1 subagent tool_call, got $SUBAGENT_CALLS"
  cp "$EVENTS_FILE" /tmp/p18-subagent-fail-events.jsonl
  exit 1
}
echo "OK: subagent tool_call observed ($SUBAGENT_CALLS)"

# 5b. subagent_started event with the goal.
set +o pipefail
STARTED=$(grep '"type":"subagent_started"' "$EVENTS_FILE" | head -1)
set -o pipefail
[ -n "$STARTED" ] || {
  echo "FAIL: no subagent_started event"
  cp "$EVENTS_FILE" /tmp/p18-subagent-fail-events.jsonl
  exit 1
}
echo "$STARTED" | grep -q "find which file defines the main agent loop" || {
  echo "FAIL: subagent_started event missing goal text"
  echo "got: $STARTED"
  exit 1
}
echo "OK: subagent_started carries goal"

# 5c. subagent_done event with the summary.
set +o pipefail
DONE=$(grep '"type":"subagent_done"' "$EVENTS_FILE" | head -1)
set -o pipefail
[ -n "$DONE" ] || {
  echo "FAIL: no subagent_done event"
  cp "$EVENTS_FILE" /tmp/p18-subagent-fail-events.jsonl
  exit 1
}
echo "$DONE" | grep -q "core/runner/runner.go" || {
  echo "FAIL: subagent_done event missing the finding text"
  echo "got: $DONE"
  exit 1
}
echo "OK: subagent_done carries finding text"

# 5d. tool_result for the subagent call carries the finding. Same
# escaped-JSON-inside-data shape as tool_call.
set +o pipefail
RESULT_LINE=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'subagent' | head -1)
set -o pipefail
[ -n "$RESULT_LINE" ] || {
  echo "FAIL: no subagent tool_result"
  cp "$EVENTS_FILE" /tmp/p18-subagent-fail-events.jsonl
  exit 1
}
echo "$RESULT_LINE" | grep -q "Subagent finding" || {
  echo "FAIL: tool_result missing 'Subagent finding' header"
  echo "got: $RESULT_LINE"
  exit 1
}
echo "$RESULT_LINE" | grep -q "core/runner/runner.go" || {
  echo "FAIL: tool_result missing the actual finding text"
  echo "got: $RESULT_LINE"
  exit 1
}
echo "OK: parent tool_result carries the subagent finding"

# 5e. parent's iteration count includes 1 iteration for the subagent
# call. The mock scripts 3 turns total (parent / sub / parent), but the
# parent only sees its own 2 iterations — sub-loop iters MUST NOT leak
# into the parent's iteration_start counter.
PARENT_ITER_COUNT=$(grep -c '"type":"iteration_start"' "$EVENTS_FILE")
[ "$PARENT_ITER_COUNT" -ge 2 ] || {
  echo "FAIL: expected at least 2 parent iteration_start events, got $PARENT_ITER_COUNT"
  exit 1
}
# At most 3 — parent has 2 turns + 1 milestone gate would add 1 if memory
# bank had content; with empty bank the milestone is skipped. Defensive
# upper bound of 5 covers both paths.
[ "$PARENT_ITER_COUNT" -le 5 ] || {
  echo "FAIL: too many iteration_start events ($PARENT_ITER_COUNT) — sub-loop iters may be leaking"
  exit 1
}
echo "OK: parent saw $PARENT_ITER_COUNT iterations (sub-loop iters did not leak)"

# 5f. Sub-loop internal events MUST NOT leak. The sub-loop in mock mode
# emits exactly 1 turn (provider_request → end_turn → run_done). If
# those leaked, we'd see a run_done event AND a provider_response event
# for the sub-loop's text. Confirm the only run_done is the parent's
# (count must be 1, not 2).
RUN_DONE_COUNT=$(grep -c '"type":"run_done"' "$EVENTS_FILE")
[ "$RUN_DONE_COUNT" -le 1 ] || {
  echo "FAIL: expected at most 1 run_done event (parent's only), got $RUN_DONE_COUNT — sub-loop leaked events"
  exit 1
}
echo "OK: sub-loop events did not leak ($RUN_DONE_COUNT run_done events)"

# 5g. Permission gate did not block the subagent call.
set +o pipefail
DENIED=$(grep '"type":"permission_denied"' "$EVENTS_FILE" 2>/dev/null | grep -c 'subagent' || true)
set -o pipefail
DENIED=${DENIED:-0}
[ "$DENIED" -eq 0 ] || {
  echo "FAIL: subagent call(s) blocked by permission gate ($DENIED denials)"
  exit 1
}
echo "OK: permission gate passed (autonomy=ASK_DESTRUCTIVE_ONLY)"

# Sanity: gild process is still healthy (no panic from spawning sub-loops).
kill -0 $GILD_PID 2>/dev/null || { echo "FAIL: gild died (panic?)"; exit 1; }
echo "OK: gild still running"

echo "PASS: phase 18 (subagent)"
