#!/usr/bin/env bash
# tests/release/check_artifacts.sh
#
# Smoke-check the dist/ directory after `make release` (GoReleaser snapshot).
# Verifies that for the current host platform we got:
#   - 4 raw binaries (gil, gild, giltui, gilmcp) under dist/<bin>_<os>_<arch>_<variant>/
#   - at least one .tar.gz archive containing all 4 binaries
#   - on linux: at least one .deb and one .rpm
#   - checksums.txt
#   - dist/homebrew/gil.rb formula
#
# Exits non-zero on any failure with a clear PASS/FAIL summary.

set -u
cd "$(dirname "$0")/../.."
ROOT="$(pwd)"
DIST="$ROOT/dist"

GOOS="$(go env GOOS 2>/dev/null || uname -s | tr 'A-Z' 'a-z')"
GOARCH="$(go env GOARCH 2>/dev/null || uname -m)"

# Normalize a couple of common uname outputs into Go's vocabulary.
case "$GOARCH" in
  x86_64) GOARCH=amd64 ;;
  aarch64) GOARCH=arm64 ;;
esac

pass=0
fail=0
report() {
  local status="$1" msg="$2"
  if [[ "$status" == PASS ]]; then
    pass=$((pass + 1))
    echo "  PASS  $msg"
  else
    fail=$((fail + 1))
    echo "  FAIL  $msg"
  fi
}

echo "[release-check] dist=$DIST host=$GOOS/$GOARCH"

if [[ ! -d "$DIST" ]]; then
  echo "[release-check] FAIL: dist/ not found — run 'make release' first"
  exit 1
fi

shopt -s nullglob

# 1. Per-binary directories produced by the `go` builder.
#    GoReleaser writes binaries to dist/<bin>_<os>_<arch>_<variant>/<bin>
#    where <variant> is e.g. v1 (amd64) or v8.0 (arm64). We glob by prefix
#    so we don't have to hard-code the variant suffix.
for bin in gil gild giltui gilmcp; do
  matches=("$DIST/${bin}_${GOOS}_${GOARCH}"_*/"${bin}")
  if (( ${#matches[@]} > 0 )) && [[ -x "${matches[0]}" ]]; then
    report PASS "binary present: ${matches[0]}"
  else
    report FAIL "binary missing: dist/${bin}_${GOOS}_${GOARCH}_*/${bin}"
  fi
done

# 2. At least one tar.gz archive (host-platform bundle).
host_archives=("$DIST"/*"${GOOS}_${GOARCH}".tar.gz)
if (( ${#host_archives[@]} > 0 )); then
  report PASS "host tar.gz archive present: ${host_archives[0]}"
else
  report FAIL "no host tar.gz archive (looking for *_${GOOS}_${GOARCH}.tar.gz)"
fi

# Sanity: archive contains all four binaries.
if (( ${#host_archives[@]} > 0 )); then
  members="$(tar -tzf "${host_archives[0]}" 2>/dev/null)"
  missing=()
  for bin in gil gild giltui gilmcp; do
    if ! grep -qx "$bin" <<<"$members"; then
      missing+=("$bin")
    fi
  done
  if (( ${#missing[@]} == 0 )); then
    report PASS "host archive contains all 4 binaries"
  else
    report FAIL "host archive missing: ${missing[*]}"
  fi
fi

# 3. Cross-platform archive count — should be 4 (linux/darwin × amd64/arm64).
all_archives=("$DIST"/*.tar.gz)
if (( ${#all_archives[@]} == 4 )); then
  report PASS "tar.gz archive count = 4 (full matrix)"
else
  report FAIL "expected 4 tar.gz archives, found ${#all_archives[@]}"
fi

# 4. Checksums.
if [[ -s "$DIST/checksums.txt" ]]; then
  report PASS "checksums.txt present"
else
  report FAIL "checksums.txt missing or empty"
fi

# 5. Linux packages (nfpms only emits on linux targets).
debs=("$DIST"/*.deb)
rpms=("$DIST"/*.rpm)
if (( ${#debs[@]} >= 1 )); then
  report PASS ".deb package(s) present: ${#debs[@]} found"
else
  report FAIL "no .deb package in dist/"
fi
if (( ${#rpms[@]} >= 1 )); then
  report PASS ".rpm package(s) present: ${#rpms[@]} found"
else
  report FAIL "no .rpm package in dist/"
fi

# 6. Homebrew formula.
if [[ -f "$DIST/homebrew/gil.rb" ]]; then
  report PASS "homebrew formula present: dist/homebrew/gil.rb"
else
  report FAIL "homebrew formula missing: dist/homebrew/gil.rb"
fi

echo "[release-check] summary: pass=$pass fail=$fail"
if (( fail > 0 )); then
  exit 1
fi
echo "[release-check] OK"
