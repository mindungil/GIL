#!/usr/bin/env bash
# Phase 6 e2e: repomap + memory_update + write_file + post-verify milestone gate.
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
GIL_MOCK_MODE=run-memory-repomap "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# Seed the workspace with a small Go file so repomap has something to find
cat > "$WORK/main.go" <<'EOF'
package main

func main() {
    Helper()
}

func Helper() string {
    return "hi"
}
EOF

# 2. Create session + inject frozen spec
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p6
sessionId: $ID
goal:
  oneLiner: create hello.txt
  successCriteriaNatural:
    - file exists
constraints:
  techStack:
    - bash
    - go
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
  maxIterations: 10
risk:
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 3. Run synchronously so we can inspect everything when it returns
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$RUN_OUT"

echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }

# 4. Verify hello.txt was created
[ -f "$WORK/hello.txt" ] || { echo "FAIL: hello.txt not created"; exit 1; }
grep -q "hello" "$WORK/hello.txt" || { echo "FAIL: wrong content"; exit 1; }
echo "OK: write_file produced hello.txt"

# 5. Verify the memory bank captured both updates
MEMDIR="$BASE/data/sessions/$ID/memory"
[ -d "$MEMDIR" ] || { echo "FAIL: memory dir not created at $MEMDIR"; exit 1; }
[ -f "$MEMDIR/activeContext.md" ] || { echo "FAIL: activeContext.md missing"; exit 1; }
[ -f "$MEMDIR/progress.md" ] || { echo "FAIL: progress.md missing"; exit 1; }

grep -q "creating hello.txt" "$MEMDIR/activeContext.md" || {
  echo "FAIL: activeContext.md missing 'creating hello.txt'"
  cat "$MEMDIR/activeContext.md"
  exit 1
}
echo "OK: memory_update recorded activeContext"

grep -q "created hello.txt" "$MEMDIR/progress.md" || {
  echo "FAIL: milestone gate did not append to progress.md"
  cat "$MEMDIR/progress.md"
  exit 1
}
echo "OK: post-verify milestone gate recorded progress"

# 6. Verify the event log shows repomap + memory_milestone events
EVENTS_DIR="$BASE/data/sessions/$ID/events"
[ -d "$EVENTS_DIR" ] || { echo "FAIL: events dir missing"; exit 1; }
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no jsonl event file"; exit 1; }

grep '"type":"tool_call"' "$EVENTS_FILE" | grep -q 'repomap' || {
  echo "FAIL: no repomap tool_call event"
  exit 1
}
echo "OK: repomap tool was invoked"

grep -q '"memory_milestone_done"' "$EVENTS_FILE" || {
  echo "FAIL: memory_milestone_done event missing"
  exit 1
}
echo "OK: memory milestone gate ran"

echo "OK: phase 6 e2e — memory + repomap + compact + milestone gate sanity!"
