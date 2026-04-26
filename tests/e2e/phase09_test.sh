#!/usr/bin/env bash
# Phase 9 e2e: multi-iteration soak with stuck detection + compaction +
# memory + many tool calls. Asserts no panic + verifier passes + memory
# bank evolved.
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

# 1. Start gild with soak mock
GIL_MOCK_MODE=run-soak "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Create session
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

# 3. Inject frozen spec — high iteration cap, FULL autonomy, soak.txt verifier
mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-soak
sessionId: $ID
goal:
  oneLiner: soak run
  successCriteriaNatural:
    - soak.txt exists
constraints:
  techStack:
    - bash
verification:
  checks:
    - name: soak_exists
      kind: SHELL
      command: test -f $WORK/soak.txt
workspace:
  backend: LOCAL_NATIVE
  path: $WORK
models:
  main:
    provider: mock
    modelId: mock-model
budget:
  maxIterations: 50
risk:
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 4. Run synchronously — capture wall time
START=$(date +%s)
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
END=$(date +%s)
ELAPSED=$((END - START))
echo "Run took ${ELAPSED}s"
echo "$RUN_OUT" | tail -10

# 5. Assert run completed (status done OR max_iterations OR stuck)
if echo "$RUN_OUT" | grep -qE "Status:.*(done|max_iterations|stuck)"; then
  echo "OK: run reached a terminal status"
else
  echo "FAIL: run didn't reach terminal status"
  exit 1
fi

# 6. Verify soak.txt exists (verifier passed if status=done)
[ -f "$WORK/soak.txt" ] || { echo "FAIL: soak.txt not created"; exit 1; }
echo "OK: soak.txt exists"

# 7. Verify many files were written
FILE_COUNT=$(ls "$WORK"/f*.txt 2>/dev/null | wc -l)
[ "$FILE_COUNT" -ge 10 ] || { echo "FAIL: expected >=10 fN.txt files, got $FILE_COUNT"; exit 1; }
echo "OK: $FILE_COUNT workspace files written"

# 8. Verify memory bank evolved
MEMDIR="$BASE/data/sessions/$ID/memory"
[ -d "$MEMDIR" ] || { echo "FAIL: memory dir missing"; exit 1; }
grep -q "wrote 10 files" "$MEMDIR/progress.md" || {
  echo "FAIL: progress.md missing 'wrote 10 files'"
  cat "$MEMDIR/progress.md"
  exit 1
}
echo "OK: memory bank captured progress"

# 9. Verify many event types fired
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no jsonl event file"; exit 1; }

EVENT_COUNT=$(wc -l < "$EVENTS_FILE")
[ "$EVENT_COUNT" -ge 50 ] || { echo "FAIL: expected >=50 events, got $EVENT_COUNT"; exit 1; }
echo "OK: $EVENT_COUNT events recorded"

# 10. Verify the major event types appeared
for typ in iteration_start tool_call tool_result; do
  grep -q "\"$typ\"" "$EVENTS_FILE" || {
    echo "FAIL: no '$typ' events in log"
    exit 1
  }
done
echo "OK: core event types present"

# 11. Verify stuck detection or compaction fired (at least one)
HAS_STUCK=$(grep -c '"stuck_detected"' "$EVENTS_FILE" 2>/dev/null || true)
HAS_COMPACT=$(grep -c '"compact_done"' "$EVENTS_FILE" 2>/dev/null || true)
HAS_MEMORY_MS=$(grep -c '"memory_milestone' "$EVENTS_FILE" 2>/dev/null || true)
# grep -c returns 0 (string) on no match with exit code 1; normalize to integer 0.
HAS_STUCK=${HAS_STUCK:-0}
HAS_COMPACT=${HAS_COMPACT:-0}
HAS_MEMORY_MS=${HAS_MEMORY_MS:-0}
echo "  stuck_detected: $HAS_STUCK"
echo "  compact_done:   $HAS_COMPACT"
echo "  memory_milestone: $HAS_MEMORY_MS"
# At least ONE of these should fire in a real soak; document but don't fail
# the test since exact triggering depends on threshold tuning.
TOTAL=$(( ${HAS_STUCK:-0} + ${HAS_COMPACT:-0} + ${HAS_MEMORY_MS:-0} ))
[ "$TOTAL" -ge 1 ] || { echo "FAIL: none of the long-run features fired (stuck/compact/memory_milestone)"; exit 1; }
echo "OK: $TOTAL long-run feature event(s) fired"

# 12. Sanity: gild process is still healthy (no panic)
kill -0 $GILD_PID 2>/dev/null || { echo "FAIL: gild died (panic?)"; exit 1; }
echo "OK: gild still running"

echo "OK: phase 9 e2e — soak sanity (${ELAPSED}s wall, $EVENT_COUNT events, $FILE_COUNT files)"
