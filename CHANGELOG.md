# Changelog

All notable changes to gil are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it reaches v1.0.

## [Unreleased]

### Added

- `docs/releases/v0.1.0-alpha.md` — landing-page release notes for the
  v0.1.0-alpha tag (elevator pitch + highlights + install + quickstart
  + provider matrix + Phase 17 follow-ups + acknowledgments).
- `docs/dogfood/2026-04-27-second-run-end-to-end.md` — second dogfood
  run; full daemon loop end-to-end (gil new → frozen spec → gil run →
  verify → checkpoint), exercising the same code paths the live qwen
  run would. Surfaced a real bug in the milestone summarizer's
  failure-mode handling.
- `vscode/PACKAGING.md` — verified `.vsix` build smoke (118 KB, 0 TS
  errors) plus three Phase 17 follow-ups for marketplace readiness
  (missing `repository`, missing `LICENSE`, protos not bundled).

## [0.1.0-alpha] — 2026-04-27

First public alpha. Phases 1–13 complete: 14 e2e green, 4 binaries
(`gil` / `gild` / `giltui` / `gilmcp`), fresh-install onboarding,
in-session UX, distribution paths.

### Added

#### Core engine (Phases 1–9)

- Long-form interview engine (`core/interview`) with `SlotFiller` +
  `Adversary` + `SelfAuditGate`, saturation-based stop.
- Run engine (`core/runner.AgentLoop`) with Anthropic-native tool use
  and a multi-iteration auto-loop.
- Tool suite: `bash`, `read_file`, `write_file`, `edit` (Aider 4-tier
  SEARCH/REPLACE), `apply_patch` (Codex DSL), `memory_*` (Cline 6-file
  bank), `repomap` (Aider PageRank), `compact_now` (Hermes), `exec`
  (Hermes Recipe runner).
- Verifier (`core/verify`): shell-assertion runner with per-check
  timeout.
- Stuck detection (5 patterns: RepeatedAction, RepeatedObservation,
  RepeatedError, Monologue, PingPong, ContextWindow) plus 4 recovery
  strategies (`ModelEscalate`, `AltToolOrder`, `ResetSection`,
  `AdversaryConsult`, `SubagentBranch`).
- Cache-preserving compression (Hermes Head/Middle/Tail with
  anti-thrashing + Anthropic system-and-3 `cache_control`).
- Memory bank (Cline 6 markdown lift).
- Repomap (Aider PageRank, CGO-free Go: stdlib parser + regex).
- Permission glob evaluator (OpenCode last-wins).
- Shadow Git checkpoints (separate `.git` outside the workspace).
- Sandboxes: bwrap (Linux, Codex lift), Seatbelt (macOS, Codex lift),
  Docker per-command exec, SSH per-command exec + rsync sync.
- Cloud backends: Modal (CLI shell-out + manifest gen), Daytona (REST
  API + `RemoteExecutor` optional interface).
- MCP client (Goose subprocess pattern lift) for consuming external
  MCP servers.
- Async run with `--detach` + live event tail.
- HTTP/JSON gateway via grpc-gateway.
- Multi-user data isolation via `gild --user`.
- Prometheus metrics endpoint.
- Soak test harness (200+ iter mock soak).

#### Harness UX foundations (Phase 11)

- XDG-standard layout (`core/paths`): Config / Data / State / Cache.
  Auto-migrates `~/.gil/` on first run.
- Credential store (`core/credstore`): file-based JSON 0600, atomic
  write, opencode `auth.json` pattern lift.
- `gil auth login/list/logout/status` subcommand with interactive
  picker + key prompt.
- `gild` factory consults credstore before falling back to env vars.
- `gil init` first-run scaffolding (XDG dirs + `config.toml` stub +
  `auth login`).
- `gil doctor` 5-group environment diagnostic (Layout / Daemon /
  Credentials / Sandboxes / Tools).
- `gil completion <bash|zsh|fish|powershell>`.
- `cliutil.UserError` typed errors with `Hint` field; rewrote 17
  user-facing error sites.
- `gild` auto-spawn on first daemon-needing command (PID file + sock
  polling).

#### In-session UX (Phase 12)

- AGENTS.md / CLAUDE.md / .cursor/rules tree-walk discovery
  (`core/instructions`); injected into the AgentLoop system prompt.
- MCP server registry (`core/mcpregistry`) with global + project TOML;
  `gil mcp add/list/remove`; RunService merges with `spec.MCP`.
- TUI 9 slash commands (`/help /status /cost /clear /compact /model
  /agents /diff /quit`); observation-only ground rule (no
  mid-tool-call interrupts).
- `gil run --interactive` mode with the same slash dispatch.
- Project-local `.gil/config.toml` with layered defaults (CLI > env >
  project > global > defaults).
- Persistent permissions (`core/permission.PersistentStore`):
  `always_allow` / `always_deny` per-project; TUI modal 6 options
  (allow/deny × once/session/always); `AnswerPermission.decision`
  proto enum.
- Cost calculator (`core/cost`) with embedded model price catalog;
  `gil cost` + `gil stats` (USD est, `--json`, `--days`).
- `gil export` markdown / json / jsonl + `gil import` jsonl replay.
- Global `--output text|json` flag on events / status / mcp list /
  auth list / cost / stats / doctor.

#### Distribution (Phase 13)

- `scripts/install.sh` curl-pipe-able installer.
- `gil update` with installer-method-aware dispatch (`script` / `brew`
  / `manual`).
- `docs/distribution.md` channel comparison.
- `SECURITY.md`, `PRIVACY.md`, `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`.
- Version constants (`core/version`) wired via build-time `-ldflags`;
  `gil --version` / `gild --version` / `giltui --version` / `gilmcp
  --version`.
- GoReleaser config (16 binary matrix, deb + rpm + brew formula).
- VS Code extension scaffold (Cline pattern lift, slim TS-only).
- Python Atropos environment adapter.
- OIDC bearer-token middleware (UDS bypass option).

### Notes

- This release has all CODE PATHS for Modal / Daytona / OIDC / VS Code
  Marketplace / Atropos training, but real validation requires
  user-provided credentials. See README "외부 자원이 필요한 잔여 항목"
  for the explicit list.
- Pre-1.0: API and on-disk schemas may change between releases.
  Migrations will be best-effort.

[Unreleased]: https://github.com/mindungil/GIL/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/mindungil/GIL/releases/tag/v0.1.0-alpha
