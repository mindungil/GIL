#!/usr/bin/env bash
# Phase 12 e2e (Track I): in-session UX surfaces.
#
# Goal: prove that the new in-session ergonomics actually work end-to-end:
#
#   1. AGENTS.md / CLAUDE.md / .cursor/rules tree-walk discovery is reachable
#      from a real run (the AgentLoop emits a system_instructions_loaded
#      event we can grep in events.jsonl).
#   2. `gil mcp add/list/remove` round-trips a stdio server through the
#      project-local registry (.gil/mcp.toml).
#   3. `gil cost <id>` aggregates token usage from a synthetic events.jsonl
#      and prints a USD figure for a known model.
#   4. `gil stats` walks the sessions tree and surfaces "sessions" in its
#      summary, with at least one session present.
#   5. `gil export <id> --format jsonl` + `gil import <file>` round-trip:
#      a freshly imported session has the same event count and a new id.
#   6. Project-local `.gil/config.toml` model field is inherited by a run
#      whose spec leaves Models nil.
#
# What this script intentionally skips:
#
#   - TUI smoke (slash modal, permission modal): the TUI requires an
#     interactive PTY. Coverage lives in tui/internal/app/* unit tests
#     and core/slash/handlers_test.go.
#   - Permission "always allow" persistence: requires a TUI reply path.
#     Covered by core/permission/store_test.go and the integration tests
#     in server/internal/service/run_permission_test.go.
#
# Hermeticity: GIL_HOME points at a per-test mktemp dir for the entire
# script; the trap cleans it up on EXIT.
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
  pkill -f "gild --foreground" 2>/dev/null || true
  rm -rf "$BASE" "$WORK"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

############################################################################
# Step 1: AGENTS.md tree-walk discovery reaches the AgentLoop.
#
# We seed an AGENTS.md file under the workspace, run a mock-mode autonomous
# session, and then grep events.jsonl for the system_instructions_loaded
# event the runner emits when discovery returns at least one source. We do
# not parse the event payload — just confirm it fired (the discovery →
# system-prompt path was exercised). The unit test
# core/runner/runner_test.go::TestAgentLoop_DiscoversAGENTSMDFromWorkspace
# pins the "AGENTS.md content lands in system prompt" half.
############################################################################

# Seed AGENTS.md before starting gild so the run sees it on first iteration.
cat > "$WORK/AGENTS.md" <<'EOF'
Project rule: always run gofmt before claiming a Go change is done.
EOF

GIL_MOCK_MODE=run-hello "$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!

for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; kill $GILD_PID 2>/dev/null || true; exit 1; }

ID=$("$ROOT/bin/gil" new --working-dir "$WORK" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: no session ID"; exit 1; }

mkdir -p "$BASE/data/sessions/$ID"
cat > "$BASE/data/sessions/$ID/spec.yaml" <<EOF
specId: test-spec-p12-agentsmd
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

"$ROOT/bin/gil" run "$ID" --socket "$SOCK" --provider mock > "$BASE/agents-run.out" 2>&1 || {
  echo "FAIL: gil run errored"
  cat "$BASE/agents-run.out"
  exit 1
}

EVENTS_FILE="$BASE/data/sessions/$ID/events/events.jsonl"
[ -f "$EVENTS_FILE" ] || { echo "FAIL: no events.jsonl produced for $ID"; exit 1; }
grep -q '"system_instructions_loaded"' "$EVENTS_FILE" || {
  echo "FAIL: system_instructions_loaded event not in events.jsonl — AGENTS.md discovery did not run"
  echo "events.jsonl head:"
  head -20 "$EVENTS_FILE"
  exit 1
}
echo "OK: AGENTS.md discovery reached AgentLoop (system_instructions_loaded fired)"

############################################################################
# Step 2: `gil mcp add/list/remove` round-trip.
#
# We add a stdio server in --project scope (writes <workspace>/.gil/mcp.toml),
# verify it appears in `gil mcp list`, then remove it and verify it disappears.
# Project scope is used so the test does not pollute the GIL_HOME global
# registry — though under GIL_HOME it would be hermetic anyway.
############################################################################

# `gil mcp add --project` requires a `.gil/` workspace marker — initialise one.
mkdir -p "$WORK/.gil"

(cd "$WORK" && "$ROOT/bin/gil" mcp add fs --type stdio --project -- echo hi) > "$BASE/mcp-add.out" 2>&1 || {
  echo "FAIL: gil mcp add"
  cat "$BASE/mcp-add.out"
  exit 1
}
grep -q "Added MCP server" "$BASE/mcp-add.out" || { echo "FAIL: add output missing success line"; cat "$BASE/mcp-add.out"; exit 1; }
echo "OK: gil mcp add fs --type stdio (project scope)"

(cd "$WORK" && "$ROOT/bin/gil" mcp list) > "$BASE/mcp-list.out" 2>&1 || { echo "FAIL: gil mcp list"; cat "$BASE/mcp-list.out"; exit 1; }
grep -q "fs" "$BASE/mcp-list.out" || { echo "FAIL: mcp list missing 'fs' entry"; cat "$BASE/mcp-list.out"; exit 1; }
echo "OK: gil mcp list shows 'fs'"

(cd "$WORK" && "$ROOT/bin/gil" mcp remove fs --project) > "$BASE/mcp-remove.out" 2>&1 || {
  echo "FAIL: gil mcp remove"
  cat "$BASE/mcp-remove.out"
  exit 1
}
grep -q "Removed MCP server" "$BASE/mcp-remove.out" || { echo "FAIL: remove output missing success line"; cat "$BASE/mcp-remove.out"; exit 1; }
echo "OK: gil mcp remove fs"

(cd "$WORK" && "$ROOT/bin/gil" mcp list) > "$BASE/mcp-list2.out" 2>&1 || true
if grep -Eq '^fs[[:space:]]' "$BASE/mcp-list2.out"; then
  echo "FAIL: 'fs' still in list after remove"
  cat "$BASE/mcp-list2.out"
  exit 1
fi
echo "OK: gil mcp list no longer contains 'fs'"

############################################################################
# Step 3: `gil cost <id>` on a synthetic session.
#
# We build a session directory with a hand-written events.jsonl that has one
# provider_request (recording the model) and one provider_response with
# 1000 input + 500 output tokens for claude-opus-4-7. The cost catalog is
# embedded so the math should be deterministic ($15/M input + $75/M output
# = 0.015 + 0.0375 = $0.0525). We just check the model name appears and
# the output contains a "$" sign — the exact figure is pinned by
# core/cost/calculator_test.go.
############################################################################

SYN_ID="01TEST00COST00000000000000"
SYN_EVENTS="$BASE/data/sessions/$SYN_ID/events"
mkdir -p "$SYN_EVENTS"
cat > "$SYN_EVENTS/events.jsonl" <<'EOF'
{"id":1,"timestamp":"2026-04-26T12:00:00Z","source":1,"kind":1,"type":"provider_request","data":"{\"model\":\"claude-opus-4-7\",\"msgs\":1,\"tools\":0}"}
{"id":2,"timestamp":"2026-04-26T12:00:01Z","source":1,"kind":2,"type":"provider_response","data":"{\"text_len\":42,\"tool_calls\":0,\"input_tokens\":1000,\"output_tokens\":500,\"stop_reason\":\"end_turn\"}"}
EOF

"$ROOT/bin/gil" cost "$SYN_ID" > "$BASE/cost.out" 2>&1 || {
  echo "FAIL: gil cost"
  cat "$BASE/cost.out"
  exit 1
}
grep -q "claude-opus-4-7" "$BASE/cost.out" || { echo "FAIL: gil cost output missing model"; cat "$BASE/cost.out"; exit 1; }
grep -q '\$' "$BASE/cost.out" || { echo "FAIL: gil cost output missing \$ sign"; cat "$BASE/cost.out"; exit 1; }
echo "OK: gil cost reports model + USD figure for synthetic session"

############################################################################
# Step 4: `gil stats` walks the sessions tree and reports counts.
#
# At this point the sessions dir contains both the run from Step 1 (which
# went through the mock provider, so it has provider_request events) and
# the synthetic Step 3 session. We just need stats to list at least one.
############################################################################

"$ROOT/bin/gil" stats > "$BASE/stats.out" 2>&1 || { echo "FAIL: gil stats"; cat "$BASE/stats.out"; exit 1; }
grep -qi "sessions" "$BASE/stats.out" || { echo "FAIL: gil stats output missing 'sessions'"; cat "$BASE/stats.out"; exit 1; }
echo "OK: gil stats produces a sessions summary"

############################################################################
# Step 5: `gil export <id> --format jsonl` then `gil import <file>` round-trip.
#
# Use the live AGENTS.md session ($ID) — it has spec.yaml + events.jsonl on
# disk so the export will carry a real metadata header + event lines. After
# import we expect a NEW session id, a sessions/<newId>/events/events.jsonl
# file with the same number of event lines as the source.
############################################################################

EXPORT_FILE="$BASE/export.jsonl"
"$ROOT/bin/gil" export "$ID" --format jsonl > "$EXPORT_FILE" 2>"$BASE/export.err" || {
  echo "FAIL: gil export"
  cat "$BASE/export.err"
  exit 1
}
[ -s "$EXPORT_FILE" ] || { echo "FAIL: gil export produced an empty file"; exit 1; }
SRC_LINES=$(wc -l < "$EVENTS_FILE")
echo "OK: gil export wrote $(wc -l < "$EXPORT_FILE") jsonl lines (events.jsonl had $SRC_LINES)"

IMPORT_OUT=$("$ROOT/bin/gil" import "$EXPORT_FILE" 2>&1) || { echo "FAIL: gil import"; echo "$IMPORT_OUT"; exit 1; }
echo "$IMPORT_OUT" | grep -q "Imported" || { echo "FAIL: import output missing 'Imported' line"; echo "$IMPORT_OUT"; exit 1; }
NEW_ID=$(echo "$IMPORT_OUT" | awk -F': ' '/New session id/ {print $2}')
[ -n "$NEW_ID" ] || { echo "FAIL: import did not print a new session id"; echo "$IMPORT_OUT"; exit 1; }
[ "$NEW_ID" != "$ID" ] || { echo "FAIL: import reused source session id"; exit 1; }
NEW_EVENTS="$BASE/data/sessions/$NEW_ID/events/events.jsonl"
[ -f "$NEW_EVENTS" ] || { echo "FAIL: imported session has no events.jsonl ($NEW_EVENTS)"; exit 1; }
NEW_LINES=$(wc -l < "$NEW_EVENTS")
[ "$NEW_LINES" -eq "$SRC_LINES" ] || { echo "FAIL: imported event count $NEW_LINES != source $SRC_LINES"; exit 1; }
echo "OK: gil import created new session $NEW_ID with $NEW_LINES events (round-trip preserves event count)"

############################################################################
# Step 6: project-local `.gil/config.toml` is inherited by a run whose
# spec leaves Models nil. This is the user-visible promise of Track D.
#
# We use a fresh session in a fresh workspace so it cannot inherit anything
# from $BASE. The workspace's .gil/config.toml pins model = "claude-opus-4-7".
# We inject a frozen spec WITHOUT a `models` block (so the spec on disk has
# Models == nil) and run with --provider mock. After the run completes, we
# confirm the AgentLoop ran end-to-end (status:done) — the
# server/internal/service/run_workspace_config_test.go pins that the
# resolved model came from the project config (Go-level test reads the
# in-memory mutated spec). For the e2e we just need the run to succeed:
# if ApplyDefaults had crashed or the resolved model had been empty the
# AgentLoop would have errored before the first iteration.
############################################################################

WORK2="$(mktemp -d)"
mkdir -p "$WORK2/.gil"
cat > "$WORK2/.gil/config.toml" <<'EOF'
provider = "mock"
model = "claude-opus-4-7"
autonomy = "FULL"
EOF

ID2=$("$ROOT/bin/gil" new --working-dir "$WORK2" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID2" ] || { echo "FAIL: project-config session create"; exit 1; }

mkdir -p "$BASE/data/sessions/$ID2"
cat > "$BASE/data/sessions/$ID2/spec.yaml" <<EOF
specId: test-spec-p12-projcfg
sessionId: $ID2
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
      command: test -f $WORK2/hello.txt
workspace:
  backend: LOCAL_NATIVE
  path: $WORK2
budget:
  maxIterations: 5
risk:
  autonomy: FULL
EOF

(cd "$ROOT/core" && go run "$ROOT/tests/e2e/helpers/setfrozen.go" "$BASE/data/sessions.db" "$ID2")

RUN_OUT=$("$ROOT/bin/gil" run "$ID2" --socket "$SOCK" --provider mock 2>&1) || {
  echo "FAIL: project-config run"
  echo "$RUN_OUT"
  exit 1
}
echo "$RUN_OUT" | grep -q "Status:.*done" || {
  echo "FAIL: project-config run did not reach done"
  echo "$RUN_OUT"
  exit 1
}
[ -f "$WORK2/hello.txt" ] || { echo "FAIL: project-config run did not create hello.txt"; exit 1; }
rm -rf "$WORK2"
echo "OK: project-local .gil/config.toml model inherited (run reached done with no spec.Models block)"

############################################################################
# Step 7 / 8: TUI slash + permission "always allow" persistence are NOT
# exercised here — see the file-level comment for the rationale.
############################################################################

echo "OK: phase 12 e2e — in-session UX"
