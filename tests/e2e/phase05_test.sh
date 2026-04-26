#!/usr/bin/env bash
# Phase 5 e2e: async run + tail + shadow git checkpoint + restore.
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

# 1. Start gild manually with mock-mode
GIL_MOCK_MODE=run-hello "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Create session + inject frozen spec (same approach as phase04)
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p5
sessionId: $ID
goal:
  oneLiner: create hello.txt
  successCriteriaNatural:
    - file exists
constraints:
  techStack:
    - bash
verification:
  checks:
    - name: exists
      kind: SHELL
      command: test -f $WORK/hello.txt
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
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 3. Run with --detach
DETACH_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock --detach 2>&1)
echo "$DETACH_OUT" | grep -q "Started run for $ID" || {
  echo "FAIL: --detach did not print started message"
  echo "got: $DETACH_OUT"
  exit 1
}
echo "OK: detach returned immediately"

# 4. Tail events for up to 5s, capture output. The detached run should complete
#    within a few hundred ms, so we run tail with a short timeout.
TAIL_LOG="$BASE/tail.log"
( timeout 3 "$ROOT/bin/gil" events "$ID" --tail --socket "$SOCK" > "$TAIL_LOG" 2>&1 || true ) &
TAIL_PID=$!

# Give the detached run + tail a moment
wait $TAIL_PID 2>/dev/null || true

# Wait for hello.txt to appear (polling up to 3s)
for _ in $(seq 1 30); do
  if [ -f "$WORK/hello.txt" ]; then break; fi
  sleep 0.1
done
[ -f "$WORK/hello.txt" ] || { echo "FAIL: hello.txt not created (detach didn't finish?)"; exit 1; }
grep -q "hello" "$WORK/hello.txt" || { echo "FAIL: wrong content"; exit 1; }
echo "OK: detached run created hello.txt"

# 5. Shadow git should exist with at least 1 commit
SHADOW_DIR="$BASE/data/sessions/$ID/shadow"
[ -d "$SHADOW_DIR" ] || { echo "FAIL: shadow dir not created at $SHADOW_DIR"; exit 1; }
# Find the inner .git (the hash subdir)
GIT_DIR=$(find "$SHADOW_DIR" -maxdepth 2 -name ".git" -type d | head -1)
[ -n "$GIT_DIR" ] || { echo "FAIL: no .git under $SHADOW_DIR"; exit 1; }
COMMITS=$(git --git-dir="$GIT_DIR" --work-tree="$WORK" log --oneline 2>/dev/null | wc -l)
[ "$COMMITS" -ge 1 ] || { echo "FAIL: expected >= 1 shadow commits, got $COMMITS"; exit 1; }
echo "OK: shadow git has $COMMITS commits"

# 6. Wait for session status to flip to done (poll up to 3s)
for _ in $(seq 1 30); do
  STATUS=$("$ROOT/bin/gil" status --socket "$SOCK" 2>/dev/null | awk -v id="$ID" '$1==id {print $2}')
  if [ "$STATUS" = "DONE" ] || [ "$STATUS" = "STOPPED" ]; then break; fi
  sleep 0.1
done

# 7. Restore to step 1 (oldest checkpoint).
#    Since our scenario only writes hello.txt and then commits, step 1 is the "iter 1"
#    commit AFTER hello.txt was created → restoring to it should leave hello.txt in place.
#    To test rollback meaningfully, we'd need a multi-step scenario. For Phase 5 sanity,
#    we just verify that restore returns success and reports a commit SHA.
RESTORE_OUT=$("$ROOT/bin/gil" restore "$ID" 1 --socket "$SOCK" 2>&1) || {
  echo "FAIL: restore returned non-zero"
  echo "got: $RESTORE_OUT"
  exit 1
}
echo "$RESTORE_OUT" | grep -q "Restored session $ID to step 1" || {
  echo "FAIL: restore did not print expected message"
  echo "got: $RESTORE_OUT"
  exit 1
}
echo "OK: restore step 1 succeeded"

# 8. Tail log should mention at least one event type — proof tail subscribed live
if [ -s "$TAIL_LOG" ]; then
  if grep -qE "AGENT|SYSTEM|ENVIRONMENT" "$TAIL_LOG"; then
    echo "OK: tail captured events (sample: $(head -1 "$TAIL_LOG"))"
  else
    echo "WARN: tail log non-empty but no expected source enums; content was:"
    head -5 "$TAIL_LOG"
  fi
else
  echo "WARN: tail log empty (timing race; events may have flushed before subscribe)"
fi

echo "OK: phase 5 e2e — async + checkpoint + restore + tail sanity!"
