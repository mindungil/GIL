#!/usr/bin/env bash
# Phase 7 e2e: edit + apply_patch + permission gate sanity.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"
WORK="$(mktemp -d)"
PATH="$ROOT/bin:$PATH"
export PATH

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# 1. Start gild with the new mock mode
GIL_MOCK_MODE=run-edit-patch-permission "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Seed the workspace with main.go containing Bar()
cat > "$WORK/main.go" <<'EOF'
package main

func Bar() string { return "bar" }
EOF

# 3. Create session
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

# 4. Inject frozen spec with ASK_DESTRUCTIVE_ONLY autonomy so rm gets denied
mkdir -p "$BASE/sessions/$ID"
cat > "$BASE/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p7
sessionId: $ID
goal:
  oneLiner: add FOO function
  successCriteriaNatural:
    - file contains FOO
    - added.txt exists
constraints:
  techStack:
    - go
verification:
  checks:
    - name: foo_in_main
      kind: SHELL
      command: grep -q FOO $WORK/main.go
    - name: added_exists
      kind: SHELL
      command: test -f $WORK/added.txt
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
  autonomy: ASK_DESTRUCTIVE_ONLY
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/sessions.db" "$ID")

# 5. Run synchronously
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$RUN_OUT"

echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }

# 6. Verify edit tool worked: main.go contains FOO
grep -q "func FOO" "$WORK/main.go" || {
  echo "FAIL: edit tool did not add FOO function"
  cat "$WORK/main.go"
  exit 1
}
echo "OK: edit tool added FOO function"

# 7. Verify apply_patch worked: added.txt exists
[ -f "$WORK/added.txt" ] || { echo "FAIL: apply_patch did not add added.txt"; exit 1; }
grep -q "hello added" "$WORK/added.txt" || { echo "FAIL: added.txt has wrong content"; exit 1; }
echo "OK: apply_patch added added.txt"

# 8. Verify permission gate fired: events should contain permission_denied for rm
EVENTS_DIR="$BASE/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no jsonl event file"; exit 1; }

grep -q '"permission_denied"' "$EVENTS_FILE" || {
  echo "FAIL: permission_denied event missing"
  exit 1
}
echo "OK: permission gate denied rm command"

# Confirm rm did NOT actually run (would have removed everything)
[ -f "$WORK/main.go" ] || { echo "FAIL: rm got through somehow!"; exit 1; }

echo "OK: phase 7 e2e — edit + apply_patch + permission gate sanity!"
