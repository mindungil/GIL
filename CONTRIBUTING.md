# Contributing

Thanks for considering a contribution to gil.

## Quick links

- [README.md](README.md) — project overview
- [docs/design.md](docs/design.md) — architectural narrative
- [docs/progress.md](docs/progress.md) — phase history
- [docs/plans/](docs/plans/) — current/future implementation plans
- [SECURITY.md](SECURITY.md), [PRIVACY.md](PRIVACY.md) — stance documents

## Development setup

```bash
git clone https://github.com/mindungil/GIL.git
cd gil
make build             # produces bin/{gil,gild,giltui,gilmcp}
make test              # all unit tests
make e2e-all           # 14 e2e phases
```

Requirements:
- Go 1.25+
- git, make
- Optional: bwrap (Linux sandbox), docker, ssh, rsync

## Branching strategy — gitflow

| Branch | Purpose |
|---|---|
| `main` | Production. Only release commits + hotfixes. Tagged `vX.Y.Z`. |
| `develop` | Integration. All feature work merges here. |
| `feature/*` | New feature off `develop`. Merges back to `develop` via PR. |
| `release/vX.Y.Z` | Release prep off `develop`. Merges to `main` (with tag) AND `develop`. |
| `hotfix/vX.Y.Z` | Urgent fix off `main`. Merges to `main` (with tag) AND `develop`. |

Only `main` and `develop` are long-lived. Everything else is deleted after merge.

## Making a change

1. **Open an issue first** for non-trivial changes — get alignment before writing code.
2. **Branch off `develop`**: `git checkout develop && git pull && git checkout -b feature/your-thing`.
3. **Follow the plan structure**: significant changes get a `docs/plans/phase-NN-feature.md` document with tracks + tasks (see existing plans for examples).
4. **Reference lift discipline**: when porting patterns from other harnesses (aider/cline/codex/goose/hermes-agent/opencode/openhands), cite source file + line in the commit message.
5. **Tests required**: every public API change needs a unit test. e2e for end-user-visible flows.
6. **Commits**: small, logical, with `feat(scope): ...` / `fix(scope): ...` / `docs(scope): ...` / `test(scope): ...` / `chore(scope): ...` prefixes.
7. **PR target**: `feature/*` → `develop`. Never directly to `main`.
8. **PR checklist**:
   - `make test` green
   - `make e2e-all` green
   - Plan/progress docs updated if relevant
   - Reference lifts cited

## Releasing

1. From `develop`: `git checkout -b release/vX.Y.Z`
2. Bump CHANGELOG.md (move `[Unreleased]` items into a new `[X.Y.Z]` section + date)
3. Open PR to `main`. After merge:
   ```
   git checkout main && git pull
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin main vX.Y.Z
   ```
4. GoReleaser workflow auto-builds the binary matrix on tag push.
5. Merge `release/vX.Y.Z` back to `develop` so the version bump + CHANGELOG land there too.
6. Delete the release branch.

## Hotfix

1. From `main`: `git checkout -b hotfix/vX.Y.Z+1`
2. Fix + test
3. PR to `main` → tag → push (same as release)
4. Merge back to `develop`

## Code style

- gofmt + goimports
- Errors: wrap with `cliutil.New(msg, hint)` for user-facing paths
- No global mutable state in `core/`

## Module layout

| Module | Purpose |
|---|---|
| `core/` | Pure logic, no I/O outside dedicated subpackages |
| `runtime/` | OS / cloud sandbox adapters |
| `proto/` | gRPC types |
| `server/` | gild daemon |
| `cli/` | gil CLI |
| `tui/` | giltui Bubbletea TUI |
| `sdk/` | gRPC client wrapper |
| `mcp/` | gilmcp MCP server adapter |

## Reporting bugs

GitHub issues: https://github.com/mindungil/GIL/issues

Include:
- gil version (`gil --version`)
- OS + arch
- `gil doctor` output
- Minimal reproducer

## Reporting security issues

See [SECURITY.md](SECURITY.md). Do not open public issues for security bugs.

## Releases

Releases are tagged `v0.X.Y`. GoReleaser auto-builds the matrix on tag push.
