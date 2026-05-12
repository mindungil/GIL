# Install

## 요구사항

- **Go 1.25+**
- **git**
- 선택: bwrap (Linux sandbox), docker, ssh + rsync

## 옵션 1 — Build from source (권장 dev)

```bash
git clone https://github.com/mindungil/GIL.git
cd GIL
make install   # → /usr/local/bin/{gil,gild,giltui,gilmcp}
```

## 옵션 2 — curl installer (release tag 후 가능)

```bash
curl -fsSL https://raw.githubusercontent.com/mindungil/GIL/main/scripts/install.sh | bash
```

GitHub release에 binary archives가 있을 때만 작동. tag 안 찍힌 시점에는 옵션 1 사용.

## 옵션 3 — go install

```bash
go install github.com/mindungil/gil/cli/cmd/gil@latest
go install github.com/mindungil/gil/server/cmd/gild@latest
go install github.com/mindungil/gil/tui/cmd/giltui@latest
go install github.com/mindungil/gil/mcp/cmd/gilmcp@latest
```

## 옵션 4 — Homebrew (mindungil/homebrew-tap 등록 후 가능)

```bash
brew tap mindungil/tap
brew install gil
```

(아직 tap repo 등록 안 됨 — Phase 16 진행)

## 첫 실행

```bash
gil init               # XDG dirs 생성 + (대화형) auth login
gil doctor             # 환경 진단
```

`gil init` 끝나면:
- `$XDG_CONFIG_HOME/gil/` (config + auth.json 0600)
- `$XDG_DATA_HOME/gil/sessions/`
- `$XDG_STATE_HOME/gil/` (gild socket + logs)
- `$XDG_CACHE_HOME/gil/`

## XDG 위치

| 디렉토리 | Linux | macOS | 환경변수 |
|---|---|---|---|
| Config | `~/.config/gil` | `~/Library/Application Support/gil` | `XDG_CONFIG_HOME` |
| Data | `~/.local/share/gil` | `~/Library/Application Support/gil` | `XDG_DATA_HOME` |
| State | `~/.local/state/gil` | `~/Library/Application Support/gil` | `XDG_STATE_HOME` |
| Cache | `~/.cache/gil` | `~/Library/Caches/gil` | `XDG_CACHE_HOME` |

전체 한 디렉토리: `export GIL_HOME=/path/to/single-tree`

## 마이그레이션

기존 `~/.gil/` 사용자: `gil init` 1회 실행하면 자동 분산.
