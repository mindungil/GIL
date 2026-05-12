#!/usr/bin/env bash
# Phase 18 Track D e2e: clarify tool — pause/resume flow.
#
# Scenario: scripted mock provider (GIL_MOCK_MODE=run-clarify) calls
# the clarify tool with 2 suggestions and urgency=high. We start the
# run detached, tail the live event stream until we see the
# clarify_requested event, capture its ask_id, then answer with `gil
# clarify <id> "yes, deploy" --ask-id <id>`. After the answer the run
# completes; events.jsonl carries one clarify_requested event with the
# right payload shape and a tool_result that echoes the user's answer.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/state/gild.sock"
WORK="$(mktemp -d)"
PATH="$ROOT/bin:$PATH"
export PATH

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  pkill -f "gil --output json events" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# 1. Start gild with mock-mode
GIL_MOCK_MODE=run-clarify "$ROOT/bin/gild" --foreground --base "$BASE" &
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
specId: test-spec-p18-clarify
sessionId: $ID
goal:
  oneLiner: ask the user before deploying
  successCriteriaNatural:
    - clarify question was raised and answered
constraints:
  techStack:
    - clarify
verification:
  checks:
    - name: noop
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
  maxIterations: 6
risk:
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 3. Start the run DETACHED so we can tail and answer in the same shell.
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock --detach 2>&1)
echo "$RUN_OUT" | grep -qiE "started|background" || { echo "FAIL: run did not start"; echo "$RUN_OUT"; exit 1; }
echo "OK: run started detached"

# 4. Tail the live event stream in JSON. The tail process exits when
# the daemon closes the per-session stream (run completes). We retry
# the tail attach a few times because the runStreams entry may race
# the gRPC subscribe on a fast-spawning detached run.
EVENTS_LOG="$BASE/clarify-events.log"
TAIL_PID=""
for _ in $(seq 1 30); do
  "$ROOT/bin/gil" --output json events "$ID" --tail --socket "$SOCK" > "$EVENTS_LOG" 2>&1 &
  TAIL_PID=$!
  sleep 0.2
  if kill -0 "$TAIL_PID" 2>/dev/null; then
    # Process is still alive — assume the tail attached successfully.
    break
  fi
  TAIL_PID=""
done
[ -n "$TAIL_PID" ] || { echo "FAIL: events tail never attached"; cat "$EVENTS_LOG"; exit 1; }

# 5. Wait for clarify_requested in the tailed events. We also fall
# back to the on-disk events.jsonl as a redundant signal in case the
# tail hasn't received the event yet (the daemon's persister and the
# stream's broadcast can interleave non-deterministically on busy
# systems).
ASK_ID=""
EVENTS_FILE="$BASE/data/sessions/$ID/events/events.jsonl"
for _ in $(seq 1 200); do
  if [ -s "$EVENTS_LOG" ] && grep -q "clarify_requested" "$EVENTS_LOG"; then
    ASK_ID=$(grep "clarify_requested" "$EVENTS_LOG" | head -1 | grep -oE '[0-9A-Z]{26}' | head -1)
    if [ -n "$ASK_ID" ]; then break; fi
  fi
  if [ -f "$EVENTS_FILE" ] && grep -q "clarify_requested" "$EVENTS_FILE" 2>/dev/null; then
    ASK_ID=$(grep "clarify_requested" "$EVENTS_FILE" | head -1 | grep -oE '[0-9A-Z]{26}' | head -1)
    if [ -n "$ASK_ID" ]; then break; fi
  fi
  sleep 0.1
done
[ -n "$ASK_ID" ] || {
  echo "FAIL: never saw clarify_requested ask_id"
  echo "--- events log ---"
  cat "$EVENTS_LOG"
  echo "--- /events log ---"
  exit 1
}
echo "OK: clarify_requested observed, ask_id=$ASK_ID"

# 6. Answer with the literal "yes, deploy" string. Pass --ask-id so we
# don't need the on-disk pendings list (which lags behind the live
# stream because of the events persister's bufio buffer). Retry to
# absorb any race between the tail seeing the event and the daemon
# fully registering the pending channel.
ANSWER_OUT=""
for _ in $(seq 1 30); do
  ANSWER_OUT=$(GIL_HOME="$BASE" "$ROOT/bin/gil" clarify "$ID" "yes, deploy" --ask-id "$ASK_ID" --socket "$SOCK" 2>&1 || true)
  if echo "$ANSWER_OUT" | grep -q "answered ask"; then break; fi
  sleep 0.1
done
echo "$ANSWER_OUT" | grep -q "answered ask" || {
  echo "FAIL: answer not delivered"
  echo "$ANSWER_OUT"
  exit 1
}
echo "OK: answer delivered ($ANSWER_OUT)"

# 7. Wait for run completion by polling events.jsonl for run_done OR
# by polling the session status. The gRPC Tail subscription doesn't
# auto-close when the run ends (it's a forever-stream by design), so
# we kill the tail subprocess once we've seen the answer round-trip
# evidence in the events.jsonl on disk.
EVENTS_FILE="$BASE/data/sessions/$ID/events/events.jsonl"
for _ in $(seq 1 100); do
  if [ -f "$EVENTS_FILE" ] && grep -q '"type":"run_done"' "$EVENTS_FILE" 2>/dev/null; then
    break
  fi
  # Belt + suspenders: also accept a session that has flipped to
  # done/stopped (the runner may have written the rollup before
  # flushing the run_done event line).
  STATUS_OUT=$(GIL_HOME="$BASE" "$ROOT/bin/gil" --output json session show "$ID" --socket "$SOCK" 2>&1 || true)
  if echo "$STATUS_OUT" | grep -qE '"status":"(done|stopped)"'; then
    break
  fi
  sleep 0.1
done

# Stop the tail subprocess; it would otherwise hang forever waiting on
# a stream that never closes server-side.
kill "$TAIL_PID" 2>/dev/null || true
wait "$TAIL_PID" 2>/dev/null || true

# 8. clarify_requested event must have fired with the expected payload
# shape (question + suggestions + urgency=high). We grep the on-disk
# events.jsonl which is now flushed.
[ -f "$EVENTS_FILE" ] || { echo "FAIL: no events file"; exit 1; }
CLARIFY_EVT_COUNT=$(grep -c '"type":"clarify_requested"' "$EVENTS_FILE" || true)
[ "$CLARIFY_EVT_COUNT" -ge 1 ] || { echo "FAIL: expected >=1 clarify_requested events, got $CLARIFY_EVT_COUNT"; exit 1; }
echo "OK: $CLARIFY_EVT_COUNT clarify_requested event(s) on disk"

grep '"type":"clarify_requested"' "$EVENTS_FILE" | grep -q "Should I deploy now" || {
  echo "FAIL: clarify_requested event missing question text"
  exit 1
}
echo "OK: clarify_requested carries the question"

grep '"type":"clarify_requested"' "$EVENTS_FILE" | grep -q "yes, deploy" || {
  echo "FAIL: clarify_requested event missing suggestions"
  exit 1
}
echo "OK: clarify_requested carries the suggestions"

grep '"type":"clarify_requested"' "$EVENTS_FILE" | grep -q "high" || {
  echo "FAIL: clarify_requested event missing urgency=high"
  exit 1
}
echo "OK: clarify_requested carries urgency=high"

# 9. The clarify tool's tool_result must contain the user's chosen
# answer ("yes, deploy") — proving the answer round-tripped from
# AnswerClarification → tool callback → tool_result → next iteration.
# Note: tool_result events embed the data JSON as an escaped string,
# so the field appears as \"name\":\"clarify\" on disk.
TOOL_RESULTS=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep -c 'name\\":\\"clarify' || true)
[ "$TOOL_RESULTS" -ge 1 ] || {
  echo "FAIL: no tool_result for clarify"
  echo "--- events.jsonl ---"
  cat "$EVENTS_FILE"
  echo "--- /events.jsonl ---"
  exit 1
}
grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'name\\":\\"clarify' | grep -q "yes, deploy" || {
  echo "FAIL: clarify tool_result missing the chosen answer 'yes, deploy'"
  echo "--- tool_result lines ---"
  grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'name\\":\\"clarify'
  echo "--- /tool_result ---"
  exit 1
}
echo "OK: clarify tool_result carries the user's answer"

echo "OK: phase 18 Track D e2e — clarify pause/resume flow!"
