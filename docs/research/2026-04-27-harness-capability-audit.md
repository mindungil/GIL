# Harness Capability Audit (Post-Phase-17)

**Date**: 2026-04-27
**Author**: gil dev
**Scope**: Comparative capability audit — what can other harness agents DO that gil's agent CAN'T (or does poorly)?
**Companion to**: `2026-04-26-harness-ux-audit.md` (ENTRY/SETUP layer; closed in Phase 11/12/14/17)
**Out of scope**: ENTRY/SETUP UX (already audited), MCP server registry (already complete), provider matrix.

References cloned at `/home/ubuntu/research/{aider,cline,codex,goose,hermes-agent,opencode,openhands}/`.

---

## Gil baseline (commit 7a3e00e on develop)

| layer | gil today |
|---|---|
| tools | `bash`, `read_file`, `write_file`, `edit` (4-tier), `apply_patch` (Codex DSL), `memory_load`, `memory_update`, `repomap`, `compact_now`, `exec` (Recipe runner) |
| planning | none — single agent loop, PLAN_ONLY just blocks execution |
| stuck | 5 detectors (`core/stuck/detector.go`), 5 strategies (`core/stuck/recovery.go`) |
| verify | shell assertions (`core/verify/runner.go`) |
| checkpoint | Shadow Git per-step, `gil restore` (`core/checkpoint/shadow.go`) |
| memory | Cline 6-file markdown (`core/memory/`) |
| repomap | Aider PageRank (`core/repomap/`) |
| permission | OpenCode glob + always_allow/deny (`core/permission/`) |
| MCP | client + server (`gilmcp`) + registry (`core/mcpregistry/`) |
| sandbox | bwrap / seatbelt / docker / ssh / modal / daytona |
| slash | 9 commands: `/help /status /cost /clear /compact /model /agents /diff /quit` |
| sub-agent | only via `SubagentBranch` stuck-recovery strategy (1 path, no first-class tool) |

---

## A. Agent tools

### A.1 file ops

| harness | what they have | source |
|---|---|---|
| gil | bash, read_file, write_file, edit (4-tier), apply_patch | `core/tool/` |
| aider | `/add /drop /read-only /diff /undo /lint /ls /paste /commit` (slash, not tools) | `aider/aider/commands.py` (cmd_add, cmd_drop, cmd_diff, cmd_undo …) |
| cline | read_file, write_to_file, replace_in_file, list_files, search_files, list_code_definition_names, apply_patch | `cline/src/core/prompts/system-prompt/tools/` |
| codex | apply_patch, read_file, list_dir, view_image (multimodal), shell, unified_exec | `codex/codex-rs/core/src/tools/handlers/` |
| goose | text_editor (str_replace), shell, search via developer extension | `goose/crates/goose/src/agents/platform_extensions/developer/` |
| hermes | file_tools, file_operations, patch_parser, fuzzy_match | `hermes-agent/tools/file_tools.py`, `file_operations.py` |
| opencode | read, write, edit, glob, grep, codesearch, apply_patch | `opencode/packages/opencode/src/tool/` |
| openhands | str_replace_editor (Anthropic style), edit_file (LLM-based) | `openhands/openhands/llm/tool_names.py` (V0); V1 SDK |

**Verdict**: ─ no gap on file ops. **However**: gil lacks `glob` and `list_dir` as discrete tools (must shell-out), and `aider`'s `/add /drop /read-only` user-level context curation is missing (covered separately under D).

---

### A.2 shell

| harness | what they have | source |
|---|---|---|
| gil | `bash` — single foreground command, returns when done | `core/tool/bash.go` |
| codex | `unified_exec` (foreground) **+ background terminals** with `/ps /stop` slash, plus `shell` legacy | `codex-rs/core/src/tools/handlers/unified_exec.rs`, `slash_command.rs` (`Ps`, `Stop`) |
| cline | `execute_command` with auto-stream, can detach long-running | `cline/src/core/task/tools/handlers/ExecuteCommandToolHandler.ts` |
| openhands | `execute_bash` w/ persistent shell (tmux session per agent), background `&` semantics | V0 deprecated; V1 SDK has `BashTool` w/ session reuse |
| hermes | `terminal_tool` w/ `process_registry` for backgrounded processes | `hermes-agent/tools/terminal_tool.py`, `process_registry.py` |
| opencode | `bash` w/ pty subsystem | `opencode/packages/opencode/src/tool/bash.ts`, `pty/` |
| goose | shell via developer extension | `goose/crates/goose/src/agents/platform_extensions/developer/` |
| aider | `/run /test` (user-invoked, not agent-callable) | `aider/aider/commands.py` cmd_run, cmd_test |

**Verdict**: ★★ worthwhile gap. Gil's bash is foreground-only with no background terminal lifecycle. For multi-day autonomous runs (e.g., `bun build --watch`, `pytest --watch`, dev server tail), the agent currently has to either let the shell block, set its own timeout, or write polling scripts. Codex's `/ps /stop` model + a `bash_background` variant returning a handle the agent can poll with `bash_check`/`bash_kill` would close it.

**Proposal sketch**: Extend `core/tool/bash.go` with `background: true` arg returning a `pid` token; add `bash_status(pid)` and `bash_kill(pid)` tools (or one `bash` tool with mode). Persist process registry in session for crash-recovery (mirror hermes `process_registry.py`).

---

### A.3 web/browser

| harness | what they have | source |
|---|---|---|
| gil | none | — |
| aider | `/web URL` (scrape page → markdown) via playwright | `aider/aider/scrape.py`, cmd_web |
| cline | `browser_action` (CDP — launch, click, type, screenshot), `web_fetch`, `web_search` | `cline/src/core/prompts/system-prompt/tools/browser_action.ts`, `web_fetch.ts`, `web_search.ts`, `cline/src/services/browser/BrowserSession.ts` |
| openhands | `BrowseURLAction` + `BrowseInteractiveAction` (CDP-driven, Playwright runtime) | `openhands/openhands/events/action/browse.py` |
| hermes | `web_tools` (search + fetch) + full `browser_camofox` (anti-bot Camoufox) + `browser_cdp_tool` + `browser_dialog_tool` + `browser_supervisor` | `hermes-agent/tools/browser_*.py`, `web_tools.py` |
| opencode | `webfetch` (turndown HTML→markdown, 5MB cap, permission-gated), `websearch` | `opencode/packages/opencode/src/tool/webfetch.ts`, `websearch.ts` |
| codex | none built-in; extension-only via MCP | — |
| goose | none built-in; extension-only | — |

**Verdict**: ★★★ critical gap. Multi-day autonomous coding work routinely needs to read a library doc, an issue thread, a Stack Overflow answer, an API reference. Right now the agent has to ask the user or shell out to `curl`/`wget` and parse HTML manually. Every other coding-focused harness has at least `web_fetch + web_search`; cline/openhands/hermes have full browser automation.

**Proposal sketch**: Two-tier rollout —
- Tier 1 (Phase 18): native `web_fetch` tool (HTTP GET + readability/turndown to markdown, ~5MB cap, permission-gated by host glob like opencode), and `web_search` via a pluggable provider (SearxNG / Brave / Tavily). Both as MCP-able so they can be disabled in air-gapped runs.
- Tier 2 (later): browser automation behind a feature flag, probably as an MCP server (`gilmcp-browser`) so it stays out of the core binary.

---

### A.4 search (grep/ripgrep/semantic)

| harness | what they have | source |
|---|---|---|
| gil | shell out to `grep`/`rg` only | — |
| cline | `search_files` (ripgrep wrapper with context lines) | `cline/src/services/ripgrep/index.ts`, `cline/src/core/prompts/system-prompt/tools/search_files.ts` |
| opencode | `grep` (ripgrep), `glob`, `codesearch` (semantic) | `opencode/packages/opencode/src/tool/grep.ts`, `codesearch.ts` |
| codex | `tool_search` (built-in deferred-tool fetcher) | `codex-rs/core/src/tools/tool_search.rs` |
| hermes | `session_search_tool` (search prior session messages) | `hermes-agent/tools/session_search_tool.py` |

**Verdict**: ★ nice-to-have. Bash + `rg` covers search functionally, but a discrete `grep` tool gives the model a structured result schema (path, line, context lines) instead of stringly-typed output and avoids the agent having to remember `rg --json` flags. Low effort, modest payoff.

---

### A.5 code intel (LSP / AST)

| harness | what they have | source |
|---|---|---|
| gil | repomap (PageRank symbols, no LSP) | `core/repomap/` |
| opencode | **full LSP tool** w/ goToDefinition, findReferences, hover, documentSymbol, workspaceSymbol, goToImplementation, prepareCallHierarchy, incomingCalls, outgoingCalls + per-language LSP launchers (gopls, ts-language-server, pyright …) | `opencode/packages/opencode/src/tool/lsp.ts`, `lsp/launch.ts` |
| cline | `list_code_definition_names` (tree-sitter, no LSP) | `cline/src/services/tree-sitter/` (queries for 14 langs) |
| aider | repomap (tree-sitter + PageRank, the original) | `aider/aider/repomap.py`, `queries/` |
| openhands | none in built-in tools (V1 SDK may add) | — |

**Verdict**: ★★★ critical gap. Opencode is the only one with first-class LSP, and it's transformative — "find all callers of X", "rename Y across the workspace", "what's the type of this expression at line Z" turn from heuristic searches into deterministic operations. For multi-day refactors this is the difference between "I'll grep for `oldName`" (misses `oldName_v2`, hits comments, hallucinates) and "I'll ask gopls".

**Proposal sketch**: Native `lsp` tool that shells out to language servers already on PATH (gopls, pyright, ts-language-server, rust-analyzer). Single tool with `operation` enum (definition / references / hover / symbols / rename / call-hierarchy). Mirror opencode's API surface. CGO-free; just LSP-over-stdio. Fall back gracefully when no server available (current repomap suffices).

---

### A.6 multi-modal

| harness | what they have | source |
|---|---|---|
| gil | none — text only | — |
| codex | `view_image` tool (agent fetches local image file → vision model) | `codex-rs/core/src/tools/handlers/view_image.rs` |
| cline | image input via prompt (image_url in user msg), screenshot via browser_action | `cline/src/services/browser/` |
| openhands | base64 image in `MessageAction.image_urls`, screenshot via browser | `openhands/openhands/events/action/message.py` |
| hermes | full multi-modal: `image_generation_tool`, `vision_tools`, `transcription_tools`, `tts_tool`, `voice_mode`, `neutts_synth` | `hermes-agent/tools/image_generation_tool.py`, `vision_tools.py`, `transcription_tools.py`, `tts_tool.py`, `voice_mode.py` |
| aider | `/paste` accepts clipboard image, model w/ vision; `cmd_voice` for STT | `aider/aider/commands.py` cmd_paste, cmd_voice; `voice.py` |

**Verdict**: ★★ worthwhile gap. For "며칠 자율 실행", the dominant unmet need is **screenshot-to-context** (designer pastes a Figma export, error-message screenshot, dashboard screenshot) and **image-input as part of a turn**. Voice and image-gen are out of scope for a coding harness. Modern Anthropic / Qwen-VL / Gemini models all accept images in the same message format.

**Proposal sketch**: Add `view_image(path)` tool (codex-style: agent reads a file, server attaches as `image` content block in next request). Plumb image content blocks through `core/provider/` for anthropic + openai-compatible. Vision-capable detection per model in `core/cost/catalog`.

---

### A.7 planning (todo / task tracker)

| harness | what they have | source |
|---|---|---|
| gil | none — agent just decides | — |
| cline | **focus_chain** (per-task TODO that the agent maintains, surfaced in UI) | `cline/src/core/prompts/system-prompt/tools/focus_chain.ts` |
| codex | `plan` tool — agent writes structured plan items, displayed live; **Plan Mode** as a slash | `codex-rs/core/src/tools/handlers/plan.rs`, `slash_command.rs::Plan`, `protocol/src/plan_tool.rs` |
| opencode | `todo` tool + `plan-enter` / `plan-exit` mode tools (agent flips between plan and build) | `opencode/packages/opencode/src/tool/todo.ts`, `plan.ts` |
| openhands | `task_tracker` tool | `openhands/openhands/llm/tool_names.py::TASK_TRACKER_TOOL_NAME`; `events/action/task_tracking.py` |
| hermes | `todo_tool` | `hermes-agent/tools/todo_tool.py` |
| goose | `todo` extension | `goose/crates/goose/src/agents/todo.rs` |

**Verdict**: ★★★ critical gap. **Every other harness has a TODO/plan tool** — this is the single most universal pattern in the audit. A persisted task list serves three purposes: (1) the agent doesn't forget mid-step intentions across compaction, (2) the user sees real-time progress without reading the message stream, (3) "PLAN_ONLY autonomy" becomes meaningful (the agent emits a plan, user reviews, autonomy upgrades to execute).

**Proposal sketch**: `todo` tool with `add / update / complete / list` ops, persisted as a session artifact (sibling of memory bank). Surface in TUI as a sidebar / focus bar (cline focus_chain pattern). Tie PLAN_ONLY autonomy to "agent must populate todo before any execution tool is allowed" — this finally makes PLAN_ONLY a real plan-then-execute mode, not just a block.

---

### A.8 inter-agent (sub-agent / delegate)

| harness | what they have | source |
|---|---|---|
| gil | only via `SubagentBranch` stuck-recovery strategy — no agent-callable tool | `core/stuck/recovery.go::SubagentRunner` |
| codex | `agent_jobs` (batch sub-agent spawning, each with own thread) + `multi_agents` (collaborative spawn/wait/resume) — full first-class | `codex-rs/core/src/tools/handlers/agent_jobs.rs`, `multi_agents.rs` |
| cline | `subagent` tool + `SubagentRunner` infra (separate config, tool whitelist) | `cline/src/core/prompts/system-prompt/tools/subagent.ts`, `cline/src/core/task/tools/subagent/SubagentRunner.ts` |
| opencode | `task` tool — invoke a named sub-agent | `opencode/packages/opencode/src/tool/task.ts` |
| goose | `subagent_handler` + `subagent_execution_tool` + `summon`, sub-agent gets template-injected prompt + scoped tools | `goose/crates/goose/src/agents/subagent_handler.rs`, `subagent_execution_tool/`, `summon.rs` |
| hermes | `delegate_tool` + `mixture_of_agents_tool` (run N agents, aggregate) | `hermes-agent/tools/delegate_tool.py`, `mixture_of_agents_tool.py` |
| openhands | `delegate.py` action class | `openhands/openhands/events/action/delegate.py` |

**Verdict**: ★★ worthwhile gap. Gil already has `SubagentRunner` infra but only the stuck-recovery strategy uses it. Promoting it to an agent-callable tool unlocks: (1) "go research how lib X does Y, summarise back, no edit perms", (2) parallel branch exploration ("try refactor approach A and B in parallel, I'll pick"), (3) cheap-model triage delegating to expensive-model for hard sub-tasks. Hermes's `mixture_of_agents` is the loudest case for parallel branches, but cline/opencode/codex all have at least the basic delegate.

**Proposal sketch**: Expose `delegate` tool: `delegate(subgoal, allowed_tools, max_iters, model?)` returning summary string. Reuse existing `SubagentRunner` interface. Phase 19+: add `delegate_parallel(branches[])` for mixture-of-agents pattern, returning all summaries for the parent to pick.

---

### A.9 communication (mid-run notify / ask user)

| harness | what they have | source |
|---|---|---|
| gil | none — agent runs to completion, no "ask user mid-run" tool | — |
| cline | `ask_followup_question` — agent halts, asks user, resumes with answer | `cline/src/core/prompts/system-prompt/tools/ask_followup_question.ts` |
| codex | `request_user_input` + `request_permissions` | `codex-rs/core/src/tools/handlers/request_user_input.rs`, `request_permissions.rs` |
| opencode | `question` tool (ask + permission flow) | `opencode/packages/opencode/src/tool/question.ts`, `question/index.ts` |
| hermes | `clarify_tool` + `send_message_tool` (cross-channel — Telegram/Discord/Slack mid-run) | `hermes-agent/tools/clarify_tool.py`, `send_message_tool.py` |

**Verdict**: ★★★ critical gap **specifically for the 며칠 자율 실행 mode**. The whole gil thesis is "interview exhaustively up front so agent never has to ask again". But edge cases happen — file conflict, ambiguous intent, missing credential. Today gil's only options are: stop the run (loses context) or guess (loses correctness). A `clarify` tool that posts a question to the session and parks the run until answered is the third leg. Hermes's "send_message via Slack/Telegram" is the multi-day-autonomy form factor — agent finds an ambiguity at 3am, pings user via push notification, parks, resumes when answered.

**Proposal sketch**: Add `clarify` tool: emits a `ClarifyEvent` over gRPC stream, run state transitions to `WAITING_FOR_USER`, TUI shows banner. Optional: add a notify-out plugin (matrix / slack / telegram / desktop notification) so multi-day runs can ping outside the TUI. Self-audit gate: agent should only use `clarify` if interview slot was missing, never as a substitute for thinking — count usage, escalate if abused.

---

### A.10 memory (beyond Cline 6-file)

| harness | what they have | source |
|---|---|---|
| gil | Cline 6-file markdown bank (`memory_load`, `memory_update`) | `core/memory/` |
| cline | original 6-file pattern | `cline/.clinerules/`, `cline/src/core/storage/` |
| hermes | `memory_tool` (vector-backed, queryable) + `plugins/memory/` engine | `hermes-agent/tools/memory_tool.py`, `plugins/memory/` |
| codex | `memories` slash command, separate "memory" generation/use config | `codex-rs/tui/src/slash_command.rs::Memories` |
| openhands | recall events + microagents (per-tag knowledge) | `openhands/openhands/events/recall_type.py`, `openhands/openhands/microagent/` |

**Verdict**: ─ no gap on basic memory. ★ for vector-memory; gil's Cline-style flat-file bank is sufficient for mid-size projects. Hermes's vector store starts to matter at >100k LoC repos with months of session history; not Phase-18 priority.

---

### A.11 skills (Anthropic-style file-based agent extensions)

| harness | what they have | source |
|---|---|---|
| gil | none | — |
| opencode | **first-class `skill` tool + skill discovery** — scans `**/SKILL.md`, `.claude/skills/`, `.agents/skills/`; agent invokes via `use_skill` | `opencode/packages/opencode/src/skill/index.ts`, `tool/skill.ts` |
| hermes | massive skill library — `skills/` (built-in) + `optional-skills/` + `skill_manager_tool`, `skills_hub`, `skills_sync`, `skills_guard`, `skills_tool`, `skill_preprocessing`, `skill_commands` | `hermes-agent/skills/`, `hermes-agent/optional-skills/`, `hermes-agent/tools/skill*.py`, `agent/skill_*.py` |
| cline | `use_skill` tool | `cline/src/core/prompts/system-prompt/tools/use_skill.ts` |
| codex | `skills` slash + `Skills` enum | `codex-rs/tui/src/slash_command.rs::Skills` |

**Verdict**: ★★ worthwhile gap. Skills are emerging as the cross-harness pattern (Anthropic published the format, opencode/hermes/cline/codex all consume them). For gil's "며칠 자율 실행" they're a major lever: a skill is just a markdown file with `description` frontmatter; agent picks it up when relevant. This is how a user packages "deploy procedure", "run benchmarks", "write release notes" without a hook system. Gil already supports `AGENTS.md` discovery (Phase 12), so the skill loader is a similar shape — just multi-file with frontmatter dispatch.

**Proposal sketch**: Add `core/skill/` discoverer that scans `${repo}/skills/*/SKILL.md`, `${repo}/.claude/skills/`, `${XDG_CONFIG_HOME}/gil/skills/`. Match by description against current task. Two-stage: (1) inject skill metadata as a deferred-tool list (model picks one), (2) when agent calls `use_skill(name)`, inject full body as system prompt addendum + return list of associated tool names. Mirror opencode `skill/` package shape.

---

### A.12 fancy (codebase-wide rename / refactor)

| harness | what they have |
|---|---|
| gil | none — must be done by repeated edits |
| opencode | LSP `rename` operation (workspace-wide, type-safe) |
| others | none distinct from edit + LSP |

**Verdict**: subsumed by A.5 LSP gap.

---

## B. Reasoning patterns

### B.1 Plan mode (explicit plan-then-execute)

| harness | what they have | source |
|---|---|---|
| gil | none — PLAN_ONLY autonomy just blocks all execution, no plan output | `core/runner/` |
| codex | **`/plan` slash + Plan Mode** — agent emits plan, user reviews, then runs | `codex-rs/tui/src/chatwidget/plan_mode.rs`, `tools/handlers/plan.rs` |
| opencode | **`plan-enter` / `plan-exit` tools** — agent flips itself between plan agent and build agent | `opencode/packages/opencode/src/tool/plan.ts` (PlanExitTool asks "switch to build agent?") |
| cline | Plan Mode separate from Act Mode, dedicated prompts | `cline/src/core/prompts/system-prompt/tools/plan_mode_respond.ts`, `act_mode_respond.ts` |
| aider | `/architect` mode (architect model plans, coder model executes) | `aider/aider/coders/architect_coder.py`, `architect_prompts.py` |

**Verdict**: ★★★ critical gap, paired with A.7 todo. Opencode's pattern (separate plan-agent → exit hands off to build-agent) is closest to what gil's PLAN_ONLY autonomy *should* mean. Right now PLAN_ONLY blocks tools but the agent has nowhere to write the plan, so it's effectively useless.

**Proposal sketch**: Promote PLAN_ONLY to "plan mode": (1) add `todo` tool (A.7), (2) when autonomy = PLAN_ONLY, only `todo`, `read_file`, `repomap`, `bash` (read-only via permission) are available, (3) on exit (agent calls `plan_complete` or user upgrades autonomy), build-agent inherits the todo. Plan stage = aider architect, build stage = aider coder.

---

### B.2 Architect/Coder split

| harness | what they have | source |
|---|---|---|
| gil | none | — |
| aider | architect_coder + coder_coder — two models, two roles | `aider/aider/coders/architect_coder.py` |
| codex | sub-agents have role config (manager / worker patterns possible) | `codex-rs/core/src/tools/handlers/multi_agents.rs` |

**Verdict**: ★ nice-to-have. Plan mode (B.1) covers 80% of this benefit. A separate "architect model = expensive, coder model = cheap" split is a future optimization once gil supports model-per-stage routing. Not Phase-18 priority.

---

### B.3 Reasoning effort dial

| harness | what they have | source |
|---|---|---|
| gil | none beyond model choice | — |
| aider | `/reasoning-effort` and `/think-tokens` slash | `aider/aider/commands.py` cmd_reasoning_effort, cmd_think_tokens |
| codex | `Fast` slash + per-call effort in `Model` slash | `codex-rs/tui/src/slash_command.rs::Fast`, `Model` |

**Verdict**: ★ nice-to-have. Anthropic extended-thinking + OpenAI o-series both support effort knobs. Wiring this through to a `/effort` slash + per-step config is small. Probably bundled into `/model` flow.

---

### B.4 Critic / reviewer pass

| harness | what they have | source |
|---|---|---|
| gil | none — verifier is shell assertions only | — |
| codex | `/review` slash — review my changes | `codex-rs/tui/src/slash_command.rs::Review` |
| opencode | code-review skill, can be invoked as a sub-agent | `opencode/.../tool/skill.ts` |

**Verdict**: ★★ worthwhile. A "review my diff against the goal" pass before declaring done is exactly what `gil`'s "stop = artifacts complete" boundary needs. Falls out of A.8 (delegate) + A.11 (skill) — write a `code-review` skill, agent calls it as final step.

---

## C. Conversation control surfaces

| feature | gil | codex | aider | cline | opencode |
|---|---|---|---|---|---|
| Compact at user request | `/compact` | `/compact` | `/clear` (full reset) | yes | yes |
| Resume / continue | `gil restore` (checkpoint) | `/resume` (saved chats) | session save/load | session.json | session/ |
| **Fork (branch a chat)** | none | `/fork` (full) + `/side` (ephemeral) | none | none | none |
| **New chat mid-conv** | none | `/new` | `/clear /reset` | new task | new session |
| Edit prior message | none | none | none | yes (UI) | yes |
| Skip turn / let continue | none | continue inferred | none | none | none |
| Pause vs cancel | cancel only | cancel + `/stop` for bg terms | cancel | pause + cancel | cancel |
| Rename thread | none | `/rename` | none | yes | yes |
| Goal pinning | spec is goal | `/goal` (set/view) | none | task field | none |
| Copy last response | none | `/copy` | `/copy` | yes | yes |
| Mention file | yes (interview) | `/mention` | `/add` | @ syntax | @ syntax |

**Verdict on the row that matters**: ★★ worthwhile gap on **`/fork` + `/side`**. Codex is the only one with proper conversation forking — fork the current chat, try alternative direction, original untouched. For multi-day runs this is the safe-experiment escape hatch ("what if I did X instead — fork and try, abandon if bad"). Gil's Shadow Git per-step gives file-level forking but not conversation-level.

**Proposal sketch**: `gil session fork <id>` that copies session storage + Shadow Git ref, returns new session id. TUI `/fork` slash. Cheaper than `/side` (ephemeral in-memory) for first cut.

★ for rename + goal-pin (low effort, modest UX).

---

## D. Context management beyond compaction

| harness | what they have | source |
|---|---|---|
| gil | compact + memory bank; **no selective add/drop** | `core/compact/` |
| aider | `/add` `/drop` `/read-only` `/ls` `/tokens` `/map` `/context` — fine-grained file context curation | `aider/aider/commands.py` |
| cline | mention files via `@` | `cline/src/core/mentions/` |
| opencode | mention files; auto-context via tool calls | `opencode/.../mentions` |

**Verdict**: ★★ worthwhile gap. Aider's `/add /drop` is the most-praised UX in any coding-focused harness. Today gil's compaction is automatic and opaque — the user can't say "drop those 3 PDFs you read 2 hours ago, keep the schema doc forever". The Hermes/Anthropic Head/Middle/Tail compression decides for the user. A `/pin <file>` and `/drop <file>` slash would let users curate without disabling auto-compact.

**Proposal sketch**: Two slashes — `/pin <path>` (mark file as "always include in next prompt", survives compaction), `/drop <path>` (purge from context window, do not re-include unless agent re-reads). Persist pins in session state. Display pin count in `/status`.

---

## E. Tool invention / extension

| feature | who has it | source |
|---|---|---|
| MCP client + server + registry | gil, codex, cline, opencode, openhands, goose, hermes | (parity) |
| Inline tool def mid-run | none | — |
| User-config "always-context file" | aider `--read FILE`; opencode AGENTS.md | `aider/aider/main.py`, `opencode/.../config/` |
| **Plugin system beyond MCP** | opencode `plugin/`, goose `extensions/` (full Rust plugin), hermes `plugins/` (Python) | `opencode/packages/opencode/src/plugin/`, `goose/extensions/`, `hermes-agent/plugins/` |
| Skills (file-based) | A.11 covered | — |

**Verdict on plugins**: ─ no immediate gap if MCP suffices. ★ if we want to support agent-local plugins (no separate process). Goose's in-process plugin system (Rust trait impls) is the most performant model; opencode's TS plugin system is the most ergonomic. Gil could load `.so`/`plugin` Go plugins, but Go's plugin system is brittle. **Recommendation**: stay with MCP for extensibility, add the skill loader (A.11) for declarative extensions.

---

## F. Multi-modal

Covered under A.6. Summary:
- ★★ image input (`view_image`)
- ─ image output (not for a coding harness)
- ─ voice (aider/hermes have it; not gil's audience)
- subsumed by A.3 web fetch

---

## G. Failure recovery beyond stuck

| feature | gil | others |
|---|---|---|
| Stuck detect | 5 patterns | hermes has loop_recovery (`openhands/events/observation/loop_recovery.py`) |
| Stuck recover | 5 strategies | similar |
| Hallucination detection | none | none have explicit (verifier substitutes) |
| Drift correction | self-audit @ interview transitions only | codex has `/goal` reminder |
| Pre-commit self-test | shell assertions in verify | aider `/lint` + `/test`, openhands sandbox tests |
| Rollback granularity | per-step Shadow Git | openhands per-action sandbox snapshot; opencode `snapshot/` per-message |
| Conflict resolution | none | cline has reject + retry |

**Verdict**: ─ gil is roughly at parity. ★ for "post-edit auto-lint" (aider's `/lint` runs after each edit) — could be folded into the verifier loop. Skip dedicated conflict resolution for now.

---

## H. Code intelligence

Covered under A.5. **★★★ LSP** is the headline gap.

Sub-points:
- Test discovery / coverage: ★ — opencode/cline don't have it either; would be a differentiator if added but not a reference-driven gap.

---

## I. Observability + telemetry

| harness | what they have | source |
|---|---|---|
| gil | Prometheus metrics in server, Cost USD, no tracing | `server/internal/metrics/` |
| openhands | OpenTelemetry spans, full trace | `openhands/openhands/utils/` (V1) |
| codex | analytics extension, rollout file (`/rollout`) | `codex-rs/agent-identity/`, `analytics/` |
| hermes | `analytics`, `insights.py`, `trajectory.py` (full trajectory log) | `hermes-agent/agent/insights.py`, `trajectory.py`, `analytics/` |
| cline | usage / cost analytics | `cline/src/services/account/` |
| openhands | eval harness — SWE-bench, GAIA | `openhands/evaluation/` (sub-repo) |

**Verdict**: ★ nice-to-have. Trajectory dump (agent's full action sequence, replayable) is the best-in-class hermes feature; it's how you build evals later. Gil's session storage is the seed; a "export trajectory as JSONL" command would close it. Eval harness (SWE-bench integration) is a separate project, not Phase-18.

**Proposal sketch**: `gil session export <id> --format=trajectory` emits jsonl of (action, observation) tuples. Use it later for offline eval / replay.

---

## J. Multi-agent / collab

Covered under A.8. Codex's `multi_agents` is the most sophisticated (spawn / wait / resume / interaction events). Hermes's `mixture_of_agents_tool` is the most parallel. **★★ worthwhile**.

---

## K. UX-but-deeper

| feature | gil | others |
|---|---|---|
| Notification on run complete | none | hermes sends to Telegram/Discord/Slack via `send_message_tool` |
| GitHub PR open/comment | none | openhands has `integrations/` (gitlab, github, bitbucket) for full PR lifecycle |
| Diff preview before apply | edit just writes; user sees post-hoc via `/diff` | cline shows diff, asks before apply (configurable) |
| Interactive resolution | none | covered under A.9 |

**Verdict**: ★★ for **GitHub integration** + **notify-on-complete**. Together these are what "며칠 자율 실행" needs to actually be a workflow vs a parlor trick — agent finishes feature, opens draft PR, posts message to Slack with link. Openhands's `integrations/github/` is the reference (auth flow, PR open, comment, review reply).

**Proposal sketch**: Two pieces — (a) `gh` CLI wrapper as a Recipe step (low effort, no new abstraction); (b) outbound notify plugin: `gil notify` MCP server with `notify_complete(channel, message)`. Reuse permission glob model. Falls in same bucket as A.9 clarify (the inbound side).

★ for diff-preview: trivial to add a `--dry-run` flag to `edit` tool that returns the proposed diff, agent then re-calls without `--dry-run`. But low value because verifier + Shadow Git rollback already covers the safety property.

---

## L. Distribution / ecosystem

Skip — gil already has `gil update`, releases pipeline, vsce package.

---

# Summary tables

## Parity vs gap by dimension

| dim | feature | verdict |
|---|---|---|
| A.1 | file ops | ─ parity |
| A.2 | shell (background) | ★★ |
| A.3 | web/browser | ★★★ |
| A.4 | search (rg structured) | ★ |
| A.5 | code intel (LSP) | ★★★ |
| A.6 | multi-modal (image input) | ★★ |
| A.7 | planning (todo) | ★★★ |
| A.8 | sub-agent / delegate | ★★ |
| A.9 | clarify / mid-run notify | ★★★ |
| A.10 | memory (vector) | ─ |
| A.11 | skills | ★★ |
| A.12 | refactor (subsumed by A.5) | — |
| B.1 | plan mode | ★★★ (paired with A.7) |
| B.2 | architect/coder | ★ |
| B.3 | reasoning dial | ★ |
| B.4 | critic pass | ★★ (subsumed by A.8 + A.11) |
| C | fork/side | ★★ |
| D | pin/drop context | ★★ |
| E | plugin (beyond MCP) | ─ |
| F | covered in A.3/A.6 | — |
| G | failure recovery | ─ parity (★ for auto-lint) |
| H | covered in A.5 | — |
| I | trajectory export | ★ |
| J | covered in A.8 | — |
| K | GitHub + notify | ★★ |
| L | distribution | ─ skip |

**Counts**: parity 5, ★ 5, ★★ 8, ★★★ 5.

---

## Top 5 gaps to consider closing in Phase 18+

Ordered by impact on the "며칠 자율 실행, 다시 묻지 않음" north star.

### 1. Planning / TODO tool + real Plan Mode (A.7 + B.1) — ★★★
**Why first**: Universal across every reference (codex, cline, opencode, openhands, hermes, goose). PLAN_ONLY autonomy gate exists today but is dead weight without a plan artifact. Pairing the two converts a block-everything mode into a true plan-then-execute pipeline. Side benefit: the agent stops forgetting mid-step intentions across compaction.
**Sketch**: `todo` tool (add/update/complete/list, persisted as session artifact); when autonomy = PLAN_ONLY only `{todo, read_file, repomap, bash-readonly}` allowed; on `plan_complete` or autonomy upgrade, build-agent inherits.

### 2. Web fetch + search (A.3) — ★★★
**Why second**: Multi-day autonomous work needs to read library docs, GitHub issues, RFCs, Stack Overflow. Today the agent has to ask the user or struggle with `curl | sed`. Single biggest "agent quality of life" item; cheap to ship as native tool, can be MCP-gated for air-gapped runs.
**Sketch**: native `web_fetch(url, format=markdown)` with 5MB cap and host-glob permission; `web_search` via pluggable provider (SearxNG / Brave / Tavily); both surfaceable as MCP for opt-out.

### 3. Clarify / mid-run user prompt + outbound notify (A.9 + K) — ★★★
**Why third**: The interview-exhaustively philosophy is right but not absolute. Edge cases (missing credential, ambiguous intent surfaced only at runtime) need a third option besides "stop the run" and "guess wrong". Pair inbound `clarify` with outbound `notify_complete` (Slack/Telegram/desktop) and "며칠 자율 실행" becomes survivable — agent parks, pings user, resumes when answered. Hermes's `send_message_tool` is the form factor.
**Sketch**: `clarify` tool emits `ClarifyEvent`, run state → `WAITING_FOR_USER`, TUI banner; outbound notify as MCP server (`gilmcp-notify`) with adapters per channel; permission glob model.

### 4. LSP integration (A.5) — ★★★
**Why fourth**: Highest *quality* lift but biggest implementation surface. "Find all callers", "rename across workspace", "type at line N" turn from heuristic into deterministic. Opencode is the only reference with this; it's a real moat. Worth doing despite implementation cost because hallucinated grep refactors are the #1 multi-day-run failure mode after context exhaustion.
**Sketch**: `lsp` tool (single tool, `operation` enum: definition/references/hover/symbols/rename/call-hierarchy); shell out to gopls/pyright/ts-language-server/rust-analyzer if on PATH; stdio LSP, no CGO; graceful no-op when no server (current repomap suffices).

### 5. Sub-agent delegate as first-class tool (A.8) — ★★
**Why fifth**: Infrastructure already exists (`SubagentRunner` interface), only the stuck-recovery strategy uses it. Promotion to agent-callable tool unlocks: research delegation ("go figure out how X does Y, no edit perms, summarise back"), parallel branch exploration, cheap-model triage delegating hard sub-tasks to expensive model. Naturally extends to the critic/reviewer pass (B.4) — final step delegates to a "code-review" skill on a fresh context.
**Sketch**: `delegate(subgoal, allowed_tools, max_iters, model?) → summary` reusing existing `SubagentRunner`. Phase 19+: `delegate_parallel(branches[])` returning all summaries (mixture-of-agents).

---

## Most surprising finding

**Every single reference harness has a TODO/plan tool. Gil is the only one without one.**

This wasn't on my radar going in — I expected planning to be one of those "some have it, some don't" features. But codex (`plan`), cline (`focus_chain`), opencode (`todo` + `plan-enter`/`plan-exit`), openhands (`task_tracker`), hermes (`todo_tool`), goose (`todo` extension), and aider (`/architect`) all have some form of agent-maintained, user-visible task list. The convergence suggests this isn't bikeshedding — it solves three real problems (cross-compaction memory, user progress visibility, meaningful plan-mode) that gil is currently papering over with self-audit gates and Shadow Git diffs. Pairing this with the existing-but-inert PLAN_ONLY autonomy is the cheapest, highest-leverage Phase-18 item.

Honorable mention: the **A.5 LSP gap**. Opencode is the *only* reference with proper LSP integration (`opencode/packages/opencode/src/tool/lsp.ts` — 9 operations, per-language launchers). Aider, cline, and gil all use tree-sitter + heuristics. Opencode's lead here is real and deserves a directed catch-up.
