# Reference Lifts

gil 의 모든 핵심 메커니즘은 7개 reference harness 에서 lift. 각 commit message 에 정확한 source 파일/라인 명기.

| 컴포넌트 | 출처 | gil 위치 |
|---|---|---|
| Stuck detection (5 patterns) | OpenHands | `core/stuck/detector.go` |
| Stuck recovery (4 strategies) | OpenHands, Cline, Goose | `core/stuck/recovery.go` |
| **NoProgress 6th pattern** | gil 자체 (Phase 21.A) | `core/stuck/detector.go::checkNoProgress` |
| Cache-preserving compression | Hermes Agent (Nous Research) | `core/compact/` |
| Memory bank (6 markdown) | Cline | `core/memory/bank.go` |
| Repomap (PageRank) | Aider | `core/repomap/` |
| SEARCH/REPLACE 4-tier | Aider | `core/edit/` |
| apply_patch DSL | Codex (OpenAI) | `core/patch/` |
| Permission glob (last-wins) | OpenCode (sst) | `core/permission/evaluator.go` |
| **Bash chain split** | gil 자체 (Phase 22.A) | `core/runner/runner.go::splitBashChain` |
| Persistent always_allow/deny | Codex `ApprovedForSession` + Cline | `core/permission/store.go` |
| Shadow Git checkpoint | Cline + OpenCode | `core/checkpoint/shadow.go` |
| bwrap sandbox | Codex | `runtime/local/bwrap.go` |
| Seatbelt sandbox | Codex | `runtime/local/seatbelt.go` |
| Recipe / multi-step compression | Hermes Agent | `core/exec/` |
| MCP server/client | Goose (Block) | `mcp/`, `core/mcp/` |
| MCP registry (TOML) | codex `mcp_cmd.rs` + opencode | `core/mcpregistry/` |
| HTTP/JSON gateway | gRPC ecosystem (grpc-gateway) | `proto/gil/v1/*.proto` |
| VS Code extension scaffold | Cline (saoudrizwan) | `vscode/` |
| OIDC JWT verifier | Hermes auth.py + stdlib | `server/internal/auth/` |
| Atropos environment adapter | Hermes `HermesAgentBaseEnv` | `python/gil_atropos/` |
| Plan tool (TODO + status) | codex `plan.rs` + opencode `todo.ts` | `core/plan/`, `core/tool/plan.go` |
| AGENTS.md tree-walk | codex `agents_md_tests.rs` + opencode `instruction.ts` | `core/instructions/` |
| Slash command parser/registry | codex `slash_command.rs` + cline | `core/slash/` |
| Web fetch (URL→markdown) | opencode `webfetch.ts` + aider `cmd_web` | `core/web/`, `core/tool/webfetch.go` |
| LSP integration (9 ops) | opencode `lsp.ts` | `core/lsp/`, `core/tool/lsp.go` |
| Subagent (read-only research) | gil's own `RunSubagent` + OpenHands delegation + Cline use_subagents | `core/tool/subagent.go` |
| Clarify + outbound notify | Cline `AskFollowupQuestionToolHandler` | `core/tool/clarify.go`, `core/notify/` |
| First-run UX + XDG layout | goose `paths.rs` + opencode `global/index.ts` | `core/paths/`, `cli/internal/cmd/init.go` |
| Credstore (auth.json 0600) | opencode `auth/index.ts` | `core/credstore/file.go` |
| Typed UserError + Hint | opencode `error.ts` | `core/cliutil/error.go` |
| Self-correcting tool errors | Phase 20.A (gil 자체) | `core/edit/parser.go`, `core/patch/parser.go` |
| Per-provider system prompt diet | Phase 20.B (gil 자체) | `core/runner/system_prompt.go` |
| Verifier reserve + always-final-verify | Phase 19.A (gil 자체, dogfood) | `core/runner/runner.go` |
| Architect/coder split routing | aider architect_coder.py 패턴 + Phase 19.C | `core/runner/runner.go::classifyTurn` |

## Lift discipline

매 commit message 에 다음 형식으로 출처 명기:
```
feat(scope): brief

Reference lift: <repo>/<path/to/file>:<line> — <pattern name>
- adopted: <what we kept>
- adapted: <what changed for gil>
- not lifted: <what we explicitly skipped, why>
```

## gil 자체 발견 (lift 아닌 부분)

11 self-dogfood run 에서 발견된 gil 고유 메커니즘:
- Phase 19.A verifier reserve + always-final-verify
- Phase 20.A self-correcting tool errors
- Phase 20.B per-provider system prompt verbosity
- Phase 21.A NoProgress 6th stuck pattern
- Phase 22.A bash chain permission split
- Phase 22.B verify-independent NoProgress fallback
- Phase 23.B 24h soak harness pattern

이들은 다른 harness에서 lift 안 됨 — gil 의 dogfood loop 산출.

## 더 읽기

- `docs/research/2026-04-25-reference-harnesses-deep-dive.md` — 7 harness 라인 레벨 분석
- `docs/research/2026-04-26-harness-ux-audit.md` — UX 표면 비교
- `docs/research/2026-04-27-harness-capability-audit.md` — capability layer 비교
