# Distribution channels

gil supports three install paths. Pick whichever matches your environment.

## 1. curl-installer (recommended for first-time users)

```
curl -fsSL https://raw.githubusercontent.com/mindungil/GIL/main/scripts/install.sh | bash
```

Detects host OS/arch, downloads the latest release tarball from GitHub
releases, and installs the four binaries (`gil`, `gild`, `giltui`,
`gilmcp`) to `/usr/local/bin/` (with a sudo prompt if the destination
is not writable as the calling user).

The install script also drops a tiny marker file
(`/usr/local/bin/.gil-installer-method`) recording the install method
so `gil update` later knows which upgrade path to take.

- Pros: one-line, no extra tools, works on Linux + macOS, supports
  pinning a version with `GIL_VERSION=v0.1.0`.
- Cons: requires `curl` and `bash`; not great for air-gapped
  environments (use the .deb/.rpm packages produced by GoReleaser
  instead).

Environment overrides:

| Variable           | Default                  | Purpose                       |
|--------------------|--------------------------|-------------------------------|
| `GIL_INSTALL_REPO` | `mindungil/GIL`          | GitHub `owner/repo` to fetch  |
| `GIL_BIN_DIR`      | `/usr/local/bin`         | Install destination directory |
| `GIL_VERSION`      | `latest`                 | Version tag (e.g. `v0.1.0`)   |

## 2. Homebrew tap (macOS / Linux)

```
brew tap mindungil/tap
brew install gil
```

Pros: standard macOS install path, automatic updates via `brew
upgrade`. The tap and formula are produced by GoReleaser on each
tagged release and pushed to `mindungil/homebrew-tap`.

Cons: requires Homebrew. The tap repo (`mindungil/homebrew-tap`) is a
placeholder until the first release tag actually publishes the
formula — see `.goreleaser.yaml`'s `brews:` block.

## 3. go install (developers only)

```
go install github.com/mindungil/gil/cli/cmd/gil@latest
go install github.com/mindungil/gil/server/cmd/gild@latest
go install github.com/mindungil/gil/tui/cmd/giltui@latest
go install github.com/mindungil/gil/mcp/cmd/gilmcp@latest
```

Pros: latest commit, no waiting for release.

Cons: requires Go 1.25+, downloads each module separately (gil is a
multi-module workspace), and produces binaries that `gil update` does
not know how to refresh — `go install` builds skip the marker file.

## Recommendation

| Audience                  | Channel                                          |
|---------------------------|--------------------------------------------------|
| First-time user (Linux)   | curl-installer                                   |
| First-time user (macOS)   | curl-installer or Homebrew tap                   |
| macOS regular user        | Homebrew tap                                     |
| Linux server / CI         | curl-installer or .deb/.rpm direct download      |
| Air-gapped environment    | .deb/.rpm direct download (no network at install)|
| Developer / contributor   | `git clone` + `make install`, or `go install`    |

## Marker file: how `gil update` decides

Each installer writes (or implies) one of three values:

| Marker contents | Method  | `gil update` action                                |
|-----------------|---------|----------------------------------------------------|
| `script`        | curl    | re-runs the install.sh one-liner                   |
| `brew`          | brew    | runs `brew upgrade gil`                            |
| (file missing)  | manual  | refuses to upgrade and points at this doc          |

The curl-installer writes the marker explicitly. The Homebrew tap's
formula sets the marker to `brew` in its `install` block. `make
install` (from-source builds) deliberately does not write a marker —
upgrading a from-source build means `git pull && make install`, not
`gil update`.

## .deb / .rpm direct download

Each tagged release also publishes a `.deb` and `.rpm` per
architecture (see `.goreleaser.yaml`'s `nfpms:` block). Install with
the system package manager:

```
sudo dpkg -i gil_<version>_linux_amd64.deb     # Debian / Ubuntu
sudo rpm  -i gil_<version>_linux_amd64.rpm     # RHEL / Fedora
```

Updates are managed by the system package manager; `gil update` will
report `manual` and refuse, which is the correct behaviour for
distro-managed binaries.

## Verifying a download

GoReleaser publishes a `checksums.txt` next to every tarball. After
downloading manually:

```
sha256sum -c checksums.txt --ignore-missing
```

The curl-installer does not currently verify checksums; that lands
together with Sigstore SLSA provenance in a future phase (compare
`goose update`'s sigstore-verify integration in
`research/goose/crates/goose-cli/src/commands/update.rs`).
