#!/usr/bin/env bash
# Phase 11 e2e (Track E): fresh-install onboarding path.
#
# Goal: prove that a brand-new user with NO env vars and NO manual setup can
# go from `gil init` through `gil auth login`, `gil doctor`, `gil status`
# (which auto-spawns gild), and back out via `gil auth logout` without ever
# touching ANTHROPIC_API_KEY or hand-editing a config file.
#
# We deliberately DO NOT exercise `gil interview` / `gil run` here because
# the fake API key cannot authenticate against a real provider — those flows
# belong to the dogfood suite, not the install smoke test. What we DO check:
#
#   1. gil --help is friendly (mentions auth) without any setup at all.
#   2. gil init --no-auth lays down the four XDG dirs + config.toml stub.
#   3. gil auth login --api-key writes auth.json with mode 0600 and the
#      key is masked in `gil auth list` output.
#   4. gil auth status / doctor surface the configured provider.
#   5. gil completion bash/zsh emits the cobra-generated scripts.
#   6. gil status auto-spawns gild and the UDS socket appears.
#   7. gil auth logout removes the credential idempotently.
#   8. After killing gild, the next `gil status` re-spawns it cleanly.
#
# Hermeticity: GIL_HOME is set to a mktemp dir for the whole test so the
# user's real ~/.config/gil and ~/.local/share/gil are never touched. The
# trap removes that dir on exit (success or failure).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BASE="$(mktemp -d)"
PATH="$ROOT/bin:$PATH"
export PATH
export GIL_HOME="$BASE"

cleanup() {
  pkill -f "gild --foreground" 2>/dev/null || true
  rm -rf "$BASE"
}
trap cleanup EXIT

cd "$ROOT" && make build > /dev/null

# stat permission bits: linux uses `-c %a`, macOS/BSD uses `-f %A`. Wrap
# both so the script runs identically on both platforms.
file_mode() {
  if stat -c "%a" "$1" >/dev/null 2>&1; then
    stat -c "%a" "$1"
  else
    stat -f "%A" "$1"
  fi
}

# 1. gil --help mentions auth without any prior setup. This is the very
#    first thing a user might type after install.
HELP_OUT="$("$ROOT/bin/gil" --help 2>&1)"
echo "$HELP_OUT" | grep -q "auth" || { echo "FAIL: gil --help does not mention auth"; exit 1; }
echo "OK: gil --help mentions auth"

# 2. gil init --no-auth creates the four XDG dirs + config.toml.
"$ROOT/bin/gil" init --no-auth > "$BASE/init.out" 2>&1
test -d "$BASE/config" || { echo "FAIL: config dir not created"; cat "$BASE/init.out"; exit 1; }
test -d "$BASE/data"   || { echo "FAIL: data dir not created"; exit 1; }
test -d "$BASE/state"  || { echo "FAIL: state dir not created"; exit 1; }
test -d "$BASE/cache"  || { echo "FAIL: cache dir not created"; exit 1; }
test -f "$BASE/config/config.toml" || { echo "FAIL: config.toml not created"; exit 1; }
echo "OK: gil init --no-auth created XDG layout + config.toml"

# Re-running init must be idempotent — no error, dirs still there.
"$ROOT/bin/gil" init --no-auth > "$BASE/init2.out" 2>&1 || { echo "FAIL: re-running init errored"; cat "$BASE/init2.out"; exit 1; }
echo "OK: gil init is idempotent on second run"

# 3. gil auth login --api-key (non-interactive, CI-style).
"$ROOT/bin/gil" auth login --api-key sk-ant-fake-test-key anthropic > "$BASE/login.out" 2>&1
test -f "$BASE/config/auth.json" || { echo "FAIL: auth.json not created"; cat "$BASE/login.out"; exit 1; }
PERMS="$(file_mode "$BASE/config/auth.json")"
[ "$PERMS" = "600" ] || { echo "FAIL: auth.json mode must be 600, got $PERMS"; exit 1; }
echo "OK: gil auth login created auth.json with mode 0600"

# 4. gil auth list shows masked key but NOT the full key.
LIST_OUT="$("$ROOT/bin/gil" auth list 2>&1)"
echo "$LIST_OUT" | grep -q "sk-ant-" || { echo "FAIL: auth list missing sk-ant- prefix"; echo "$LIST_OUT"; exit 1; }
echo "$LIST_OUT" | grep -q "anthropic" || { echo "FAIL: auth list missing provider name"; echo "$LIST_OUT"; exit 1; }
if echo "$LIST_OUT" | grep -q "fake-test-key"; then
  echo "FAIL: auth list leaked the full unmasked key"
  echo "$LIST_OUT"
  exit 1
fi
echo "OK: gil auth list shows masked key, full key not leaked"

# 5. gil auth status mentions anthropic.
STATUS_OUT="$("$ROOT/bin/gil" auth status 2>&1)"
echo "$STATUS_OUT" | grep -qi "anthropic" || { echo "FAIL: auth status missing anthropic"; echo "$STATUS_OUT"; exit 1; }
echo "OK: gil auth status surfaces the configured provider"

# 6. gil doctor — exit 0 (no FAILs expected on a fresh install with creds
#    configured) and the five canonical group headers are present.
DOCTOR_OUT="$("$ROOT/bin/gil" doctor 2>&1)" || { echo "FAIL: gil doctor exited non-zero"; echo "$DOCTOR_OUT"; exit 1; }
for sec in "Layout:" "Daemon:" "Credentials:" "Sandboxes:" "Tools:"; do
  echo "$DOCTOR_OUT" | grep -q "$sec" || { echo "FAIL: doctor missing section $sec"; echo "$DOCTOR_OUT"; exit 1; }
done
echo "$DOCTOR_OUT" | grep -q "anthropic" || { echo "FAIL: doctor does not show anthropic credential"; echo "$DOCTOR_OUT"; exit 1; }
echo "$DOCTOR_OUT" | grep -Eq "[0-9]+ OK" || { echo "FAIL: doctor missing summary footer"; echo "$DOCTOR_OUT"; exit 1; }
echo "OK: gil doctor renders 5 sections + summary, anthropic visible"

# 7. gil completion bash/zsh produce real scripts.
"$ROOT/bin/gil" completion bash | head -5 | grep -q "bash completion" || { echo "FAIL: completion bash output missing header"; exit 1; }
"$ROOT/bin/gil" completion zsh  | head -5 | grep -q "compdef"          || { echo "FAIL: completion zsh output missing compdef"; exit 1; }
echo "OK: gil completion bash + zsh produce shell scripts"

# 8. gil status auto-spawns gild + the UDS appears + table header rendered.
# Phase 14: visual mode is default; --plain keeps the legacy table that
# this assertion expects (header line "ID  STATUS  ...").
STATUS_OUT="$("$ROOT/bin/gil" status --plain 2>&1)" || { echo "FAIL: gil status exited non-zero"; echo "$STATUS_OUT"; exit 1; }
echo "$STATUS_OUT" | grep -q "ID" || { echo "FAIL: gil status missing table header"; echo "$STATUS_OUT"; exit 1; }
test -S "$BASE/state/gild.sock" || { echo "FAIL: gild.sock did not appear after gil status"; ls -la "$BASE/state"; exit 1; }
echo "OK: gil status auto-spawned gild (socket present)"

# 9. gil auth logout removes the credential and `auth status` reflects it.
"$ROOT/bin/gil" auth logout anthropic > "$BASE/logout.out" 2>&1
LOGOUT_OUT="$("$ROOT/bin/gil" auth status 2>&1)"
echo "$LOGOUT_OUT" | grep -q "(none configured)" || { echo "FAIL: auth status still shows credentials after logout"; echo "$LOGOUT_OUT"; exit 1; }
echo "OK: gil auth logout removed the credential"

# Idempotent logout — second logout is a successful no-op.
"$ROOT/bin/gil" auth logout anthropic > "$BASE/logout2.out" 2>&1 || { echo "FAIL: idempotent logout errored"; cat "$BASE/logout2.out"; exit 1; }
echo "OK: gil auth logout is idempotent"

# 10. Kill gild + verify the next gil call re-spawns it cleanly. This is the
#     "user accidentally killed the daemon" recovery path.
pkill -f "gild --foreground" 2>/dev/null || true
# Wait for the socket to actually go away — pkill is async on busy systems.
for _ in $(seq 1 30); do
  [ -S "$BASE/state/gild.sock" ] || break
  sleep 0.1
done
"$ROOT/bin/gil" status > "$BASE/respawn.out" 2>&1 || { echo "FAIL: gil status did not re-spawn gild"; cat "$BASE/respawn.out"; exit 1; }
test -S "$BASE/state/gild.sock" || { echo "FAIL: gild.sock missing after re-spawn"; exit 1; }
echo "OK: gil status re-spawned gild after kill"

echo "OK: phase 11 e2e — fresh-install onboarding path"
