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
git clone https://github.com/jedutools/gil.git
cd gil
make build             # produces bin/{gil,gild,giltui,gilmcp}
make test              # all unit tests
make e2e-all           # 14 e2e phases
```

Requirements:
- Go 1.25+
- git, make
- Optional: bwrap (Linux sandbox), docker, ssh, rsync

## Making a change

1. **Open an issue first** for non-trivial changes — get alignment before writing code.
2. **Branch off main**: `git checkout -b feat/your-thing`.
3. **Follow the plan structure**: significant changes get a `docs/plans/phase-NN-feature.md` document with tracks + tasks (see existing plans for examples).
4. **Reference lift discipline**: when porting patterns from other harnesses (aider/cline/codex/goose/hermes-agent/opencode/openhands), cite source file + line in the commit message.
5. **Tests required**: every public API change needs a unit test. e2e for end-user-visible flows.
6. **Commits**: small, logical, with `feat(scope): ...` / `fix(scope): ...` / `docs(scope): ...` / `test(scope): ...` / `chore(scope): ...` prefixes.
7. **PR checklist**:
   - `make test` green
   - `make e2e-all` green
   - Plan/progress docs updated if relevant
   - Reference lifts cited

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

GitHub issues: https://github.com/jedutools/gil/issues

Include:
- gil version (`gil --version`)
- OS + arch
- `gil doctor` output
- Minimal reproducer

## Reporting security issues

See [SECURITY.md](SECURITY.md). Do not open public issues for security bugs.

## Releases

Releases are tagged `v0.X.Y`. GoReleaser auto-builds the matrix on tag push.
