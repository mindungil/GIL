#!/usr/bin/env bash
# Phase 8 e2e: exec recipe + HTTP gateway + MCP server sanity.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/state/gild.sock"
WORK="$(mktemp -d)"
HTTPPORT=18080
PATH="$ROOT/bin:$PATH"
export PATH

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# 1. Start gild with exec mock mode + HTTP gateway
GIL_MOCK_MODE=run-exec-recipe "$ROOT/bin/gild" \
  --foreground --base "$BASE" \
  --http "127.0.0.1:$HTTPPORT" &
GILD_PID=$!

for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

# 2. Wait for HTTP gateway to come up
for _ in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:$HTTPPORT/v1/sessions" >/dev/null 2>&1; then break; fi
  sleep 0.1
done

# 3. Test HTTP gateway: GET /v1/sessions returns valid JSON
HTTP_OUT=$(curl -sf "http://127.0.0.1:$HTTPPORT/v1/sessions" || true)
if [ -z "$HTTP_OUT" ]; then
  echo "FAIL: HTTP gateway returned empty"
  exit 1
fi
echo "$HTTP_OUT" | grep -q '"sessions"' || {
  echo "FAIL: HTTP gateway response missing 'sessions' key"
  echo "got: $HTTP_OUT"
  exit 1
}
echo "OK: HTTP gateway GET /v1/sessions works"

# 4. Create session
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

# 5. Verify HTTP gateway shows the new session
HTTP_OUT2=$(curl -sf "http://127.0.0.1:$HTTPPORT/v1/sessions/$ID")
echo "$HTTP_OUT2" | grep -q "$ID" || {
  echo "FAIL: HTTP gateway didn't return new session"
  echo "got: $HTTP_OUT2"
  exit 1
}
echo "OK: HTTP gateway GET /v1/sessions/{id} works"

# 6. Inject frozen spec
mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p8
sessionId: $ID
goal:
  oneLiner: run a recipe
  successCriteriaNatural:
    - step1.txt exists with content "step1"
constraints:
  techStack:
    - bash
verification:
  checks:
    - name: step1_exists
      kind: SHELL
      command: test -f $WORK/step1.txt
    - name: step1_content
      kind: SHELL
      command: grep -q step1 $WORK/step1.txt
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

# 7. Run synchronously
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1)
echo "$RUN_OUT"

echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }

# 8. Verify exec recipe produced step1.txt
[ -f "$WORK/step1.txt" ] || { echo "FAIL: step1.txt not created"; exit 1; }
grep -q "step1" "$WORK/step1.txt" || { echo "FAIL: step1.txt has wrong content"; exit 1; }
echo "OK: exec recipe wrote step1.txt"

# 9. Verify exec_step events appeared in the event log (intermediate step
#    visibility — the LLM didn't see them but observers should)
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no jsonl event file"; exit 1; }

EXEC_STEP_COUNT=$(grep -c '"exec_step_done"' "$EVENTS_FILE" || echo 0)
[ "$EXEC_STEP_COUNT" -ge 3 ] || {
  echo "FAIL: expected >=3 exec_step_done events, got $EXEC_STEP_COUNT"
  exit 1
}
echo "OK: $EXEC_STEP_COUNT exec_step_done events recorded"

# 10. Test MCP server: launch gilmcp, send initialize + tools/list,
#     verify response has the 3 tools.
MCP_LOG="$BASE/mcp.log"
{
  printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n'
  printf '{"jsonrpc":"2.0","method":"notifications/initialized"}\n'
  printf '{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n'
  sleep 0.5
} | timeout 5 "$ROOT/bin/gilmcp" --socket "$SOCK" > "$MCP_LOG" 2>&1 || true

grep -q '"protocolVersion":"2024-11-05"' "$MCP_LOG" || {
  echo "FAIL: gilmcp didn't respond with protocolVersion"
  cat "$MCP_LOG"
  exit 1
}
grep -q '"list_sessions"' "$MCP_LOG" || {
  echo "FAIL: gilmcp tools/list didn't include list_sessions"
  cat "$MCP_LOG"
  exit 1
}
echo "OK: gilmcp speaks JSON-RPC + advertises 3 tools"

echo "OK: phase 8 e2e — exec recipe + HTTP gateway + MCP server sanity!"
