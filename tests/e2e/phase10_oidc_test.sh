#!/usr/bin/env bash
# Phase 10 e2e (Track E): OIDC bearer-token auth on gild's gRPC TCP listener.
#
# Approach:
#   1. Spin up tests/e2e/oidc_mock as a standalone HTTP server (an httptest-
#      style fake OIDC issuer). It generates an RSA keypair and writes
#      issuer.txt, valid.jwt, expired.jwt to a working directory.
#   2. Start gild with --grpc-tcp + --auth-issuer + --auth-audience pointing
#      at the mock issuer. Auth on UDS is left enabled-but-bypassed
#      (--auth-allow-uds defaults to true) so the existing CLI path still
#      works for setup.
#   3. Use tests/e2e/oidc_client (a tiny gRPC client) to call
#      SessionService.List against the TCP listener three times:
#        a. no token            -> exit 2 (Unauthenticated)
#        b. valid bearer token  -> exit 0 (OK)
#        c. expired bearer token-> exit 2 (Unauthenticated)
#
# Runs with no external services — the mock OIDC + gild + client are all
# stdlib-only and run on localhost.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"
OIDC_DIR="$(mktemp -d)"
GILD_LOG="$BASE/gild.log"
MOCK_LOG="$BASE/oidc_mock.log"
PATH="$ROOT/bin:$PATH"
export PATH

# Pick a couple of free-ish high ports. We deliberately bind explicit ports
# (not :0) so the e2e client can be told the address via env without parsing
# gild's logs. If a port conflict arises in CI, bump these.
OIDC_PORT="${OIDC_PORT:-37071}"
GRPC_TCP_PORT="${GRPC_TCP_PORT:-37070}"

cleanup() {
  pkill -f "gild --foreground --base $BASE" 2>/dev/null || true
  if [ -n "${MOCK_PID:-}" ]; then kill "$MOCK_PID" 2>/dev/null || true; fi
  rm -rf "$BASE" "$OIDC_DIR"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# 1. Build mock OIDC + client. We point `go build` at the bare main.go so the
#    helpers don't need to be registered as modules in go.work — same trick
#    tests/e2e/helpers/setfrozen.go uses. The cwd is the server module so we
#    inherit its grpc + gilv1 deps for oidc_client.
(cd "$ROOT/server" && go build -o "$BASE/oidc_mock" "$ROOT/tests/e2e/oidc_mock/main.go") || {
  echo "FAIL: oidc_mock build"; exit 1; }
(cd "$ROOT/server" && go build -o "$BASE/oidc_client" "$ROOT/tests/e2e/oidc_client/main.go") || {
  echo "FAIL: oidc_client build"; exit 1; }

# 2. Start the mock OIDC issuer
"$BASE/oidc_mock" \
  -addr "127.0.0.1:$OIDC_PORT" \
  -audience "gil-test" \
  -subject "e2e-user" \
  -outdir "$OIDC_DIR" \
  > "$MOCK_LOG" 2>&1 &
MOCK_PID=$!

# Wait for issuer.txt to appear (mock writes it after binding).
for _ in $(seq 1 50); do
  [ -s "$OIDC_DIR/issuer.txt" ] && break
  sleep 0.1
done
[ -s "$OIDC_DIR/issuer.txt" ] || {
  echo "FAIL: oidc_mock did not write issuer.txt"
  cat "$MOCK_LOG" || true
  exit 1
}
ISSUER="$(cat "$OIDC_DIR/issuer.txt")"
echo "OK: mock OIDC issuer up at $ISSUER"

# Sanity-check discovery + jwks reachable
curl -fsS "$ISSUER/.well-known/openid-configuration" > /dev/null
curl -fsS "$ISSUER/jwks" > /dev/null
echo "OK: discovery + jwks reachable"

# 3. Start gild with auth wired
"$ROOT/bin/gild" --foreground \
  --base "$BASE" \
  --grpc-tcp "127.0.0.1:$GRPC_TCP_PORT" \
  --auth-issuer "$ISSUER" \
  --auth-audience "gil-test" \
  --auth-allow-uds=true \
  > "$GILD_LOG" 2>&1 &
GILD_PID=$!

for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.1
done
[ -S "$SOCK" ] || {
  echo "FAIL: gild socket did not appear"
  cat "$GILD_LOG" || true
  kill $GILD_PID 2>/dev/null || true
  exit 1
}

# Also wait until the TCP listener is accepting.
for _ in $(seq 1 50); do
  if (echo > "/dev/tcp/127.0.0.1/$GRPC_TCP_PORT") 2>/dev/null; then break; fi
  sleep 0.1
done
echo "OK: gild listening (UDS=$SOCK, TCP=:$GRPC_TCP_PORT)"

# 4a. Call without a token -> Unauthenticated (exit 2)
set +e
"$BASE/oidc_client" -addr "127.0.0.1:$GRPC_TCP_PORT"
RC=$?
set -e
if [ $RC -ne 2 ]; then
  echo "FAIL: expected Unauthenticated (exit 2), got $RC"
  cat "$GILD_LOG" | tail -20
  exit 1
fi
echo "OK: no-token call rejected with Unauthenticated"

# 4b. Call with valid token -> OK (exit 0)
set +e
"$BASE/oidc_client" -addr "127.0.0.1:$GRPC_TCP_PORT" -token-file "$OIDC_DIR/valid.jwt"
RC=$?
set -e
if [ $RC -ne 0 ]; then
  echo "FAIL: expected OK (exit 0) with valid token, got $RC"
  cat "$GILD_LOG" | tail -20
  exit 1
fi
echo "OK: valid bearer token accepted"

# 4c. Call with expired token -> Unauthenticated (exit 2)
set +e
"$BASE/oidc_client" -addr "127.0.0.1:$GRPC_TCP_PORT" -token-file "$OIDC_DIR/expired.jwt"
RC=$?
set -e
if [ $RC -ne 2 ]; then
  echo "FAIL: expected Unauthenticated (exit 2) with expired token, got $RC"
  cat "$GILD_LOG" | tail -20
  exit 1
fi
echo "OK: expired bearer token rejected"

# 5. UDS path still works without auth (assumed local-trusted)
ID=$("$ROOT/bin/gil" new --working-dir "$BASE" --socket "$SOCK" 2>/dev/null | awk '{print $3}')
[ -n "$ID" ] || { echo "FAIL: UDS new failed (auth-allow-uds bypass broken)"; exit 1; }
echo "OK: UDS bypass intact ($ID)"

# 6. Sanity: gild still healthy
kill -0 $GILD_PID 2>/dev/null || { echo "FAIL: gild died"; cat "$GILD_LOG"; exit 1; }
echo "OK: gild still running"

echo "OK: phase 10 e2e — OIDC bearer-token auth (issuer=$ISSUER, audience=gil-test)"
