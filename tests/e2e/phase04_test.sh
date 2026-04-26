#!/usr/bin/env bash
# Phase 4 e2e: full autonomous run with mock provider creates hello.txt + verifier passes.
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

# 1. Start gild manually with GIL_MOCK_MODE=run-hello
GIL_MOCK_MODE=run-hello "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

# Wait for socket
for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Create session
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

# 3. Manually inject a frozen spec — skip interview for sanity test
mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec
sessionId: $ID
goal:
  oneLiner: create hello.txt
  successCriteriaNatural:
    - file exists
    - content is "hello"
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

# Compute the lock — use the Go helper to write spec.lock
# First, we need to freeze the spec. This requires computing the proto hash.
# The easiest approach: let specstore.Freeze() do it by loading the spec and computing the hash.
# However, we can't call that from bash. Instead, we'll create the lock manually by computing the hash.
# For now, let's skip the lock file and rely on Load() to work without it (it's optional).

# Actually, Load() will check if IsFrozen() (lockfile exists) and if so, verify the hash.
# We'll create a minimal spec.lock file. To properly compute it, we'd need the proto hash.
# For testing, we can skip the lock: the Run service will load the spec without verification.

# Update DB: mark session as frozen so RunService accepts it
(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# 4. Run!
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$RUN_OUT"

echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done; got: $RUN_OUT"; exit 1; }
[ -f "$WORK/hello.txt" ] || { echo "FAIL: hello.txt not created in $WORK"; exit 1; }
grep -q "hello" "$WORK/hello.txt" || { echo "FAIL: hello.txt does not contain 'hello'"; exit 1; }

echo "OK: phase 4 e2e — autonomous run created hello.txt and verifier passed!"
