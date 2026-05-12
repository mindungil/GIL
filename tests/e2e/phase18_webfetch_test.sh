#!/usr/bin/env bash
# Phase 18 e2e (Track B): web_fetch tool plumbed end-to-end.
#
# Goal: prove that the agent can call web_fetch on a real HTTP server,
# get HTML back, and see it converted to markdown — without ever
# touching the public internet.
#
# Strategy:
#   1. Stand up a Python http.server in the workspace serving a
#      hand-rolled HTML doc page. The page is small (one heading + one
#      paragraph + one fenced code block) so the test can grep the
#      converted markdown precisely.
#   2. Boot gild in mock mode (GIL_MOCK_MODE=run-webfetch). The mock
#      provider scripts a single web_fetch call against
#      $GIL_MOCK_WEBFETCH_URL — we point that at the Python server.
#   3. Run the session synchronously and grep the per-session
#      events.jsonl for the tool_result whose content includes the
#      expected markdown headings + the fixture's title. The presence
#      of those substrings proves: tool registered, permission gate
#      passed (autonomy=ASK_DESTRUCTIVE_ONLY), HTML→markdown converted,
#      result fed back to the loop.
#
# Hermeticity: GIL_HOME and the workspace are per-test mktemp dirs;
# the trap cleans them up plus the http.server child on EXIT.
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
  if [ -n "${HTTP_PID:-}" ]; then
    kill "$HTTP_PID" 2>/dev/null || true
  fi
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# Step 1: serve a synthetic docs page.
DOCS_DIR="$WORK/docs"
mkdir -p "$DOCS_DIR"
cat > "$DOCS_DIR/index.html" <<'EOF'
<!DOCTYPE html>
<html>
<head>
  <title>Phase 18 Sample Docs</title>
  <style>.x { color: red; }</style>
  <script>console.log("should be stripped");</script>
</head>
<body>
  <nav>this nav should be stripped</nav>
  <main>
    <h1>Phase 18 Sample Docs</h1>
    <p>Welcome to the gil web_fetch e2e fixture.</p>
    <pre><code class="language-go">package main
func main() { println("hi") }</code></pre>
    <ul>
      <li>alpha</li>
      <li>beta</li>
    </ul>
  </main>
  <footer>copyright stripped</footer>
</body>
</html>
EOF

# Pick a free port, then start http.server there.
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
( cd "$DOCS_DIR" && python3 -m http.server "$PORT" --bind 127.0.0.1 ) > "$WORK/http.log" 2>&1 &
HTTP_PID=$!

# Wait for the server to come up.
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$PORT/" > /dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -sf "http://127.0.0.1:$PORT/" > /dev/null || { echo "FAIL: docs server did not come up"; exit 1; }
echo "OK: synthetic docs server up on :$PORT"

DOCS_URL="http://127.0.0.1:$PORT/"
export GIL_MOCK_WEBFETCH_URL="$DOCS_URL"

# Step 2: start gild with the run-webfetch mock script.
GIL_MOCK_MODE=run-webfetch "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: gild socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }
echo "OK: gild up"

# Step 3: create + freeze a session whose verifier just checks that
# the agent ran (test -f /dev/null always passes). The point of this
# e2e is the tool plumbing, not the verifier.
ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }
echo "OK: session created ($ID)"

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p18
sessionId: $ID
goal:
  oneLiner: fetch a docs page
  successCriteriaNatural:
    - read the page
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
echo "$RUN_OUT"
echo "$RUN_OUT" | grep -q "Status:.*done" || { echo "FAIL: run did not reach done"; exit 1; }
echo "OK: run reached done"

# Step 5: inspect events.jsonl for the tool_call + tool_result pair.
EVENTS_DIR="$BASE/data/sessions/$ID/events"
EVENTS_FILE=$(ls "$EVENTS_DIR"/*.jsonl 2>/dev/null | head -1)
[ -n "$EVENTS_FILE" ] || { echo "FAIL: no events.jsonl"; exit 1; }

grep '"type":"tool_call"' "$EVENTS_FILE" | grep -q 'web_fetch' || {
  echo "FAIL: no web_fetch tool_call in events"
  cp "$EVENTS_FILE" /tmp/p18-fail-events.jsonl
  exit 1
}
echo "OK: web_fetch tool_call observed"

# The tool_result content carries the markdown we expect. Use a
# temporary disable of pipefail so a no-match grep doesn't trip
# `set -euo pipefail` before we get to print the diagnostic.
set +o pipefail
TOOL_RESULT_LINE=$(grep '"type":"tool_result"' "$EVENTS_FILE" | grep '\\"name\\":\\"web_fetch\\"' | head -1)
set -o pipefail
[ -n "$TOOL_RESULT_LINE" ] || {
  echo "FAIL: no web_fetch tool_result"
  echo "events file dump:"
  cat "$EVENTS_FILE"
  cp "$EVENTS_FILE" /tmp/p18-fail-events.jsonl
  exit 1
}

# Title is rendered into the header line.
echo "$TOOL_RESULT_LINE" | grep -q "Phase 18 Sample Docs" || {
  echo "FAIL: tool_result missing fixture title"
  echo "$TOOL_RESULT_LINE"
  exit 1
}
echo "OK: tool_result includes fixture title"

# Heading was converted from <h1> to # heading.
echo "$TOOL_RESULT_LINE" | grep -q "# Phase 18 Sample Docs" || {
  echo "FAIL: tool_result missing markdown heading"
  exit 1
}
echo "OK: HTML heading converted to markdown"

# Code fence with language hint.
echo "$TOOL_RESULT_LINE" | grep -q '\\u0060\\u0060\\u0060go\|```go' || {
  echo "FAIL: tool_result missing fenced code block"
  exit 1
}
echo "OK: fenced code block preserved with language hint"

# Nav/footer/script/style stripped.
echo "$TOOL_RESULT_LINE" | grep -q "this nav should be stripped" && {
  echo "FAIL: nav content leaked into markdown"
  exit 1
}
echo "$TOOL_RESULT_LINE" | grep -q "console.log" && {
  echo "FAIL: script content leaked into markdown"
  exit 1
}
echo "OK: nav/script/style stripped"

# Permission gate did not block — no permission_denied event for web_fetch.
grep '"type":"permission_denied"' "$EVENTS_FILE" | grep -q 'web_fetch' && {
  echo "FAIL: web_fetch was unexpectedly blocked by permission gate"
  exit 1
}
echo "OK: permission gate passed (autonomy=ASK_DESTRUCTIVE_ONLY)"

echo "PASS: phase 18 (web_fetch)"
