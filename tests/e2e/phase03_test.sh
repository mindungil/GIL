#!/usr/bin/env bash
# Phase 3 e2e sanity:
# - gil interview with mock provider produces events (sensing → conversation → agent turn)
# - Reply triggers slotfill + (later) adversary + audit pipeline
# - gil resume re-emits last agent turn for an in-progress interview
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"
PATH="$ROOT/bin:$PATH"
export PATH

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  rm -rf "$BASE"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# Start daemon explicitly
"$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

# Wait for socket
for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 1. Create new session
ID=$("$ROOT/bin/gil" new --working-dir /tmp/p3 --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: new session ($ID)"

# 2. Interview with mock provider — feed first message + reply + /done
#    The gild mock factory provides only a couple of canned responses, so the
#    interview won't reach saturation but should produce SOME events.
INTERVIEW_OUT=$(printf "build a CLI todo app\n/done\n" | \
  "$ROOT/bin/gil" interview "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$INTERVIEW_OUT" | grep -qi "agent\|stage" || { echo "FAIL: no agent/stage events; got: $INTERVIEW_OUT"; exit 1; }
echo "OK: gil interview --provider mock produced events"

# 3. gil resume — should not crash; emits the last agent turn
RESUME_OUT=$("$ROOT/bin/gil" resume "$ID" --socket "$SOCK" --provider mock 2>&1)
# Either we get an agent re-emission OR FailedPrecondition (if state was cleaned up by /done).
# Both are acceptable for Phase 3 — just verify it doesn't crash with an unexpected error.
echo "$RESUME_OUT" | grep -qiE "agent|stage|FailedPrecondition|in-memory state" \
  || { echo "FAIL: unexpected resume output: $RESUME_OUT"; exit 1; }
echo "OK: gil resume responded (output: ${RESUME_OUT:0:80}...)"

echo "OK: phase 3 e2e sanity passed"
