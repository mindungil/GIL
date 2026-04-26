#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
SOCK="$BASE/gild.sock"

cd "$ROOT"
make build > /dev/null

# 데몬 백그라운드 시작
"$ROOT/bin/gild" --foreground --base "$BASE" &
GILD_PID=$!
trap 'kill $GILD_PID 2>/dev/null || true; rm -rf "$BASE"' EXIT

# 소켓 대기
for _ in $(seq 1 50); do
  if [ -S "$SOCK" ]; then break; fi
  sleep 0.1
done
[ -S "$SOCK" ] || { echo "FAIL: socket did not appear"; exit 1; }

# 1. new 2회
ID1=$("$ROOT/bin/gil" new --socket "$SOCK" --working-dir /tmp/proj1 | awk '{print $3}')
ID2=$("$ROOT/bin/gil" new --socket "$SOCK" --working-dir /tmp/proj2 | awk '{print $3}')

# 2. status — 2 세션 확인
OUT=$("$ROOT/bin/gil" status --socket "$SOCK")
echo "$OUT" | grep -q "$ID1" || { echo "FAIL: $ID1 not in status"; exit 1; }
echo "$OUT" | grep -q "$ID2" || { echo "FAIL: $ID2 not in status"; exit 1; }
echo "$OUT" | grep -q CREATED || { echo "FAIL: status not CREATED"; exit 1; }

echo "OK: phase 1 e2e passed"
