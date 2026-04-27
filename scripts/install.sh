#!/usr/bin/env bash
# install.sh — gil curl-pipe-able installer.
#
# Usage (curl pipe):
#   curl -fsSL https://raw.githubusercontent.com/mindungil/GIL/main/scripts/install.sh | bash
#
# Usage (local):
#   bash scripts/install.sh
#
# Environment overrides:
#   GIL_INSTALL_REPO   GitHub owner/repo  (default: mindungil/GIL)
#   GIL_BIN_DIR        Install dir        (default: /usr/local/bin)
#   GIL_VERSION        Tag to install     (default: latest)
#
# Detects host OS/arch, fetches the matching release tarball produced by
# .goreleaser.yaml, and installs the four binaries (gil, gild, giltui,
# gilmcp) into GIL_BIN_DIR. Falls back to sudo when the destination is
# not writable as the calling user. Drops a marker file so `gil update`
# knows it was installed via this script.
#
# POSIX bash only (no zsh-isms, no GNU-only flags). Tested with bash 4+.

set -euo pipefail

REPO=${GIL_INSTALL_REPO:-mindungil/GIL}
BIN_DIR=${GIL_BIN_DIR:-/usr/local/bin}
VERSION=${GIL_VERSION:-latest}

# ---------------------------------------------------------------------------
# OS / architecture detection
# ---------------------------------------------------------------------------
case "$(uname -s)" in
  Linux*)  os=linux ;;
  Darwin*) os=darwin ;;
  *) echo "gil install: unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)   arch=amd64 ;;
  arm64|aarch64)  arch=arm64 ;;
  *) echo "gil install: unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# Resolve version (latest -> redirect off the GitHub releases/latest URL)
# ---------------------------------------------------------------------------
# We avoid the JSON API on purpose: the redirect target works without a
# token and never trips the unauthenticated rate-limit (60/hr/IP), which
# matters when the installer runs in CI.
if [ "$VERSION" = "latest" ]; then
  latest_url="https://github.com/${REPO}/releases/latest"
  resolved=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$latest_url" || true)
  if [ -z "$resolved" ]; then
    echo "gil install: failed to resolve latest version from $latest_url" >&2
    echo "gil install: set GIL_VERSION=v0.X.Y to pin a specific tag" >&2
    exit 1
  fi
  VERSION=${resolved##*/}
fi

# ---------------------------------------------------------------------------
# Download tarball
# ---------------------------------------------------------------------------
# GoReleaser strips the leading 'v' from {{ .Version }} in archive names,
# so the file on disk is gil_0.1.0_linux_amd64.tar.gz even though the tag
# is v0.1.0. Mirror that behaviour here.
ARCHIVE="gil_${VERSION#v}_${os}_${arch}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "gil install: downloading ${URL}"
if ! curl -fsSL "$URL" -o "${TMPDIR}/gil.tar.gz"; then
  echo "gil install: download failed (URL: $URL)" >&2
  echo "gil install: check that the release tag exists and the asset name matches" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Extract
# ---------------------------------------------------------------------------
( cd "$TMPDIR" && tar -xzf gil.tar.gz )

# ---------------------------------------------------------------------------
# Install with sudo fallback
# ---------------------------------------------------------------------------
# install(1) is preferred over cp because it sets mode atomically and
# returns a clear error when the destination is a directory.
install_bin() {
  local src=$1 dst=$2
  if install -m 0755 "$src" "$dst" 2>/dev/null; then
    return 0
  fi
  echo "gil install: needs sudo to write ${dst}"
  sudo install -m 0755 "$src" "$dst"
}

installed=0
for bin in gil gild giltui gilmcp; do
  src="${TMPDIR}/${bin}"
  if [ -f "$src" ]; then
    install_bin "$src" "${BIN_DIR}/${bin}"
    installed=$((installed + 1))
  else
    echo "gil install: warning — ${bin} not present in tarball" >&2
  fi
done

if [ "$installed" -eq 0 ]; then
  echo "gil install: no binaries were installed (tarball layout mismatch?)" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Marker file: tells `gil update` which channel re-installed us.
# ---------------------------------------------------------------------------
marker="${BIN_DIR}/.gil-installer-method"
if printf 'script\n' > "${TMPDIR}/marker" 2>/dev/null; then
  if ! install -m 0644 "${TMPDIR}/marker" "$marker" 2>/dev/null; then
    sudo install -m 0644 "${TMPDIR}/marker" "$marker"
  fi
fi

# ---------------------------------------------------------------------------
# Friendly post-install message
# ---------------------------------------------------------------------------
echo
echo "gil ${VERSION} installed to ${BIN_DIR}"
echo
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *) echo "Note: ${BIN_DIR} is not on your PATH — add it to your shell rc file." ;;
esac
echo "Next: run 'gil init' to set up the XDG layout and credentials."
