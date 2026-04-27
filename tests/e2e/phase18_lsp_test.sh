#!/usr/bin/env bash
# Phase 18 e2e (Track C): lsp tool plumbed end-to-end with gopls.
#
# Goal: prove that the agent can call the lsp tool against a real Go
# workspace and get back deterministic symbol info (definition lands in
# the right file, references include both call site and declaration,
# hover surfaces the function name).
#
# Strategy:
#   1. If gopls is missing, exit 0 with a "skipped" message — Track C
#      explicitly says LSP is OPTIONAL and e2e-all should stay green
#      on hosts that don't ship a Go toolchain.
#   2. Materialise a tiny Go workspace with a function defined in lib.go
#      and called in use.go.
#   3. Boot gild in mock mode (GIL_MOCK_MODE=run-lsp). The mock provider
#      scripts four lsp tool calls (definition / references / hover /
#      document_symbols) targeting the Hello call site in use.go.
#   4. Run the session synchronously and grep the per-session
#      events.jsonl for tool_result content matching the expected
#      lib.go target (definition), use.go + lib.go (references), and
#      "Hello" (document_symbols outline).
#
# Hermeticity: GIL_HOME and the Go workspace are per-test mktemp dirs;
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

# Step 0: gating. Look for gopls; if absent, skip cleanly.
if ! command -v gopls >/dev/null 2>&1; then
  if [ -x "${HOME:-}/go/bin/gopls" ]; then
    PATH="${HOME}/go/bin:$PATH"
    export PATH
  fi
fi
if ! command -v gopls >/dev/null 2>&1; then
  echo "SKIP: gopls not in PATH — install with: go install golang.org/x/tools/gopls@latest"
  exit 0
fi
echo "OK: gopls available at $(command -v gopls)"

cd "$ROOT" && make build > /dev/null

# Step 1: tiny Go workspace.
cat > "$WORK/go.mod" <<'EOF'
module gilsmoke

go 1.21
EOF
cat > "$WORK/lib.go" <<'EOF'
package gilsmoke

// Hello says hi.
func Hello(name string) string {
	return "hi " + name
}
EOF
cat > "$WORK/use.go" <<'EOF'
package gilsmoke

func Use() string { return Hello("world") }
EOF
echo "OK: Go workspace materialised at $WORK"

# Hello is at use.go line 3, column ~28 (inside the call). gopls is
# tolerant about exact column — anywhere inside the identifier works.
export GIL_MOCK_LSP_FILE="use.go"
export GIL_MOCK_LSP_LINE=3
export GIL_MOCK_LSP_COL=28

# Step 2: start gild with the run-lsp mock script.
GIL_MOCK_MODE=run-lsp "$ROOT/bin/gild" --foreground --base "$BASE" &
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
specId: test-spec-p18-lsp
sessionId: $ID
goal:
  oneLiner: explore symbols via lsp
  successCriteriaNatural:
    - call lsp four times
constraints:
  techStack:
    - go
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
  maxIterations: 10
risk:
  autonomy: ASK_DESTRUCTIVE_ONLY
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID")

# Step 4: run synchronously.
RUN_OUT=$("$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock 2>&1 || true)
echo "$RUN_OUT" | tail -10
echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }
echo "OK: run reached done"

# Step 5: inspect events.jsonl for the lsp tool calls and their results.
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no events.jsonl"; exit 1; }

# All four lsp tool calls fired. The event log stores tool_call data as
# escaped JSON inside a "data" field, so we grep for the literal substring
# 'lsp' inside tool_call lines. Four scripted calls in run-lsp mock mode.
set +o pipefail
LSP_CALLS=$(grep '"type":"tool_call"' "$EVENTS_FILE" | grep -c 'lsp' || true)
set -o pipefail
LSP_CALLS=${LSP_CALLS:-0}
[ "$LSP_CALLS" -ge 4 ] || {
  echo "FAIL: expected 4 lsp tool_calls, got $LSP_CALLS"
  cp "$EVENTS_FILE" /tmp/p18-lsp-fail-events.jsonl
  exit 1
}
echo "OK: $LSP_CALLS lsp tool_calls observed"

# definition result mentions lib.go (the file where Hello is declared).
set +o pipefail
DEF_HIT=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'lsp' | grep -c 'lib\.go' || true)
set -o pipefail
DEF_HIT=${DEF_HIT:-0}
[ "$DEF_HIT" -ge 1 ] || {
  echo "FAIL: no lsp tool_result mentions lib.go (definition target)"
  echo "events file dump:"
  cat "$EVENTS_FILE"
  cp "$EVENTS_FILE" /tmp/p18-lsp-fail-events.jsonl
  exit 1
}
echo "OK: lsp definition resolved to lib.go"

# References result mentions BOTH lib.go (declaration) and use.go (call site).
set +o pipefail
HAS_LIB=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'lsp' | grep -c 'lib\.go' || true)
HAS_USE=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'lsp' | grep -c 'use\.go' || true)
set -o pipefail
HAS_LIB=${HAS_LIB:-0}
HAS_USE=${HAS_USE:-0}
[ "$HAS_LIB" -ge 1 ] || { echo "FAIL: references didn't mention lib.go"; exit 1; }
[ "$HAS_USE" -ge 1 ] || { echo "FAIL: references didn't mention use.go"; exit 1; }
echo "OK: references span lib.go + use.go"

# Hover surfaced the function name "Hello".
set +o pipefail
HOVER_HIT=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep 'lsp' | grep -c 'Hello' || true)
set -o pipefail
HOVER_HIT=${HOVER_HIT:-0}
[ "$HOVER_HIT" -ge 1 ] || { echo "FAIL: hover did not surface 'Hello'"; exit 1; }
echo "OK: hover surfaced symbol name"

# Permission gate did not block any lsp call (autonomy=ASK_DESTRUCTIVE_ONLY).
set +o pipefail
DENIED=$(grep '"type":"permission_denied"' "$EVENTS_FILE" 2>/dev/null | grep -c 'lsp' || true)
set -o pipefail
DENIED=${DENIED:-0}
[ "$DENIED" -eq 0 ] || {
  echo "FAIL: lsp call(s) blocked by permission gate ($DENIED denials)"
  exit 1
}
echo "OK: permission gate passed (autonomy=ASK_DESTRUCTIVE_ONLY)"

# Sanity: gild process is still healthy (no panic from spawning gopls).
kill -0 $GILD_PID 2>/dev/null || { echo "FAIL: gild died (panic?)"; exit 1; }
echo "OK: gild still running"

echo "PASS: phase 18 (lsp)"
