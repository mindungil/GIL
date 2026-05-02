# gil — chat surface gap log (vs reference harnesses)

Each entry: a concrete gap found by comparing gil's V1 chat surface against
one reference harness in `/home/ubuntu/research/` (aider, cline, codex,
goose, opencode, openhands).

Per-entry shape:
- **Reference**: which harness, file:line, what it does
- **gil current state**: what's there now
- **Gap (severity)**: what's missing, how bad
- **TODO**: hierarchical checklist of work needed

No phase/sprint/time language — just dependencies and severity. Triage
into phase plans is a separate step done by the human.

---

## 1. File-context management is invisible (no /add /drop)

**Reference**: aider — `research/aider/aider/commands.py` (cmd_add / cmd_drop / cmd_ls / cmd_read_only / cmd_paste / cmd_map). 6+ slashes for explicit chat-context control.

**gil current state**: 10 slash commands — `/help /sessions /switch /new /spec /status /diff /merge /run /quit`. None touch the file-context set. Context selection is implicit inside the interview engine via slot filling.

**Gap (high)**: User cannot steer the agent's working file set during interview *or* run. If the interview picks wrong files, only escape is `/quit`. aider/codex/claude-code all have explicit add/drop. Single biggest contributor to "feels like an LLM endpoint" — no felt agency over context.

**TODO**:
- [ ] design `WorkingSet` slot in the interview spec
- [ ] server-side mutator endpoint to add/drop entries
- [ ] chat REPL slashes
  - [ ] `/add <path>`
  - [ ] `/drop <path>`
  - [ ] `/ls`
  - [ ] `/read-only <path>` (later)
- [ ] coordinate with repomap design (phase-06) so `/map` reuses same data

---

## 2. Mid-run permission_ask events are dropped on the chat floor

**Reference**: codex — `research/codex/codex-rs/protocol/src/protocol.rs:939`. 5-level `AskForApproval` enum (UnlessTrusted / OnFailure / OnRequest / Granular / Never) + `GranularApprovalConfig` per-flow toggles. TUI surfaces approval requests as inline prompts.

**gil current state**: Protocol is fully wired on the server side:
- `proto/gil/v1/spec.proto:177-178` — `AutonomyDial.ASK_PER_ACTION`, `ASK_DESTRUCTIVE_ONLY`
- `proto/gil/v1/run.proto:39,137` — `AnswerPermission` RPC + `PermissionDecision` message
- server emits `permission_ask` events

**But chat REPL adapter never handles them** — `grep permission_ask cli/internal/chat/repl/grpc_client.go` → 0 matches. Status strip shows `ASK_DESTRUCTIVE` variant but the actual approval prompt never reaches `Renderer.Confirm()`. Agent waits forever or times out.

**Gap (high)**: Largest "feels like LLM endpoint, not agent" symptom — user can see `ASK_DESTRUCTIVE` in the strip but can't approve anything. **Wiring gap, not design gap.** All pieces exist; nothing bridges them.

**TODO**:
- [ ] extend `mapRunEventToTracker` (or add sibling) to recognize `permission_ask` event
- [ ] route into a new `Renderer.PermissionAsk(prompt, default)` call (could reuse `Confirm`)
- [ ] call `c.AnswerPermission(ctx, PermissionDecision{...})` with the response
- [ ] handle timeout / cancellation paths
- [ ] co-design with `/interrupt` (entry 13) — both use mid-run input

---

## 3. MCP client foundation exists but is not invoked; no plugin hooks

**Reference**: opencode — `research/opencode/packages/plugin/src/index.ts`. 14+ typed Hooks: `chat.message`, `chat.params`, `chat.headers`, `permission.ask`, `command.execute.before/after`, `tool.execute.before/after`, `shell.env`, `tool.definition`, plus `experimental.*`. Plus `Tool`, `WorkspaceAdaptor`, `AuthHook`, `ProviderHook` exports. Has working MCP client at `packages/opencode/src/mcp/`.

**gil current state**: Two half-built surfaces:
1. **MCP** — has `core/mcp/client.go` (`Launch`), `core/mcpregistry`, `mergeMCPServers` in `server/internal/service/run_mcp.go`. But `run.go:947-948` loads the registry only to emit an `mcp_registry_loaded` observability event — **no `mcp.Launch` call anywhere in `server/`**. Comment at line 947 admits: `specServers := map[string]mcpregistry.Server{} // future: derived from spec.MCP`.
2. **Plugins** — no hook system at all. Renderer is the only abstraction layer, UI-only.

**Gap (high for MCP wiring, medium for plugins)**: MCP foundation is ~70% built but the last 30% (launch + tool surfacing + return-value plumbing) is missing → user-visible benefit zero. opencode/codex/cline all ship MCP working.

**TODO**:
- [ ] MCP wiring (finishable):
  - [ ] derive `specServers` from spec.MCP (replace stub at run.go:947)
  - [ ] call `mcp.Launch` on merged registry inside agent loop
  - [ ] expose launched tools to the model via tool list
  - [ ] route tool-call results back through the runner
  - [ ] handle launch failures (emit event, fall back gracefully)
- [ ] plugin hooks (own larger effort):
  - [ ] design `Hooks` interface analogous to opencode's typed hooks
  - [ ] start with the most-used: `chat.params`, `permission.ask`, `tool.execute.before/after`
  - [ ] plugin loading + registration pipeline
  - [ ] sandbox/security model for plugin execution

---

## 4. Assistant output is undifferentiated — no reasoning vs answer split

**Reference**: codex — `research/codex/codex-rs/protocol/src/protocol.rs:1439-1459`. 4 typed event categories: `AgentMessage`/`AgentMessageDelta` (final answer), `AgentReasoning`/`AgentReasoningDelta` (reasoning summary, dim), `AgentReasoningRawContent[Delta]` (raw CoT, dev-mode hidden), `AgentReasoningSectionBreak`. Per-turn `effort: ReasoningEffort` and `summary: ReasoningSummary` config.

**gil current state**: One pipe — `Renderer.AssistantText(chunk string)` (`cli/internal/chat/render/renderer.go:58`). `NextAssistantChunk` returns `(string, bool, error)` (`grpc_client.go:248`). Proto stream collapses everything into AgentTurn deltas with no kind/role discriminator. No `Reasoning(string)`, no `ToolCallNarration(...)`, no per-chunk styling hint.

**Gap (medium-high)**: With Anthropic extended-thinking models (Opus 4.7), the agent will dump raw thinking deltas into the user surface unless gil filters them. claude/codex/opencode all separate so reasoning is dim-styled or collapsed by default. Symptom behind the user's "feels like an LLM endpoint" complaint and the CoT-leakage evidence.

**TODO**:
- [ ] proto: add `kind` enum on assistant chunk event
  - [ ] `message` | `reasoning_summary` | `reasoning_raw` | `tool_narration`
- [ ] provider adapters emit the right kind
  - [ ] anthropic.go: thinking blocks → `reasoning_raw` (or `reasoning_summary` per model)
  - [ ] openai.go: reasoning summary → `reasoning_summary`
- [ ] Renderer interface
  - [ ] new `AssistantReasoning(string)` with dim styling
  - [ ] `--show-thinking` flag to surface raw vs hide by default
- [ ] coordinate with entry 8 (tool narration) — both want typed pipeline; possibly fold into single `AssistantOutput{kind, ...}`

---

## 5. Provider retries are silent — no chat surface visibility

**Reference**: goose — `research/goose/crates/goose/src/providers/retry.rs`. `RetryConfig` with `max_retries`, `initial_interval_ms`, `backoff_multiplier`, `max_interval_ms`, `transient_only`. Per-error class (`should_retry`): RateLimitExceeded/ServerError/NetworkError → retry; RequestFailed 4xx → skipped if `transient_only`. Honors server-supplied `retry_delay`. 0.8–1.2x jitter. Emits `tracing::warn!("Request failed, retrying ({}/{})…")`.

**gil current state**: Has retry wrapper at `core/provider/retry.go` — `MaxAttempts` default 4, exponential doubling from 500ms, honors `retryAfterHint`, classifies retryable errors. Architecturally on par with goose. Two omissions: (a) no jitter, (b) no `transient_only` mode.

**But retry events never reach the chat surface** — `grep retry_attempt cli/internal/chat /home/ubuntu/gil/proto` → 0 matches. Server doesn't emit a retry event; chat user sees nothing during a 3-attempt retry sequence — strip stays unchanged, no system note, no `[retrying 2/4 · 30s]` indicator.

**Gap (medium)**: A 30s retry-after wait inside the provider call manifests to the chat user as "the agent froze". User assumes hang, hits ctrl-c, loses progress.

**TODO**:
- [ ] wrap retry callback to emit `provider.retry_attempt` event
  - [ ] payload: `{n, max, wait_ms, reason}`
- [ ] `mapRunEventToTracker` recognizes the new event
- [ ] Renderer shows transient `[retrying 2/4 · 30s]` strip variant or system note
- [ ] add jitter to retry.go (5 lines, matches goose retry.rs:76)
- [ ] add `transient_only` flag to RetryConfig

---

## 6. Token usage hidden — strip shows cost only, no /tokens breakdown

**Reference**: aider — `research/aider/aider/commands.py:445` `cmd_tokens`. Per-category context-window breakdown: system messages, chat history (with `/clear` tip), repository map (with `--map-tokens` tip), each file (with `/drop` tip), each read-only file. Sorts by size, totals at bottom. Plus token+cost continuously after every turn.

**gil current state**: Run-strip variant has `$X.XX` (cost) but no token count. `proto/gil/v1/event.proto:35-39` defines `EventMetrics { tokens, cost_usd, latency_ms }` and server populates all three on `run.iter`/`run.done` events. **Chat adapter at `cli/internal/chat/repl/grpc_client.go:379,386` reads only `CostUsd`, drops `tokens` and `latency_ms`.** No `/tokens` slash — `grep "case \"tokens\"" loop.go` → 0 matches. Phase 27 implemented per-role context budget so server already tracks per-category usage; nothing surfaces it.

**Gap (medium)**: Long-session users can't tell why iterations get slower or why cost climbs — no insight into "is context bloated, did system prompt grow, is repomap too big". Explainability gap that frustrates dogfood when iterations stretch past 20+.

**TODO**:
- [ ] adapter reads `Metrics.Tokens` + `Metrics.LatencyMs` (entry mapping at grpc_client.go:379,386)
- [ ] `TrackerInput` adds `Tokens` + `LatencyMs` fields
- [ ] strip variant format: `[run · iter N/M · X.Yk toks · $Z.ZZ]`
- [ ] new `/tokens` slash
  - [ ] new server RPC `GetContextBreakdown` returning per-role budget usage from P27 ContextBudget data
  - [ ] dispatchSlash entry that calls the RPC and renders the breakdown
- [ ] coordinate with P27.5 (tiktoken-go) and P28 (Anthropic count_tokens) for accurate counts

---

## 7. No @-mention parser — users can't tag files/URLs/diagnostics inline

**Reference**: cline — `research/cline/src/shared/context-mentions.ts:47`. Regex matches `@/path/to/file`, `@http://...`/`@https://...`, `@problems` (diagnostics), `@git-changes` (unstaged diff), `@terminal` (terminal output). Trailing punctuation excluded so `@/main.go.` parses as `@/main.go`. `parseMentions()` runs *before* sending to LLM and replaces each mention with actual content. opencode/codex have variants of the same idea.

**gil current state**: Zero `@`-parsing — `grep -rE "mention|@/|parseRefs" cli/internal/chat/repl core/interview` → 0 matches outside punctuation. Server's interview engine extracts context implicitly via slot filling across turns; user has no surface affordance to say "look at THIS specific file right now" within a single message.

**Gap (high — overlaps with #1)**: One of the most recognizable agent affordances in modern coding chat (cursor, claude-code, cline, opencode all support). Without it gil's chat input feels like a plain LLM prompt box.

**TODO**:
- [ ] add `parseMentions` step in chat REPL between `Scanner.Scan()` and `SendPrompt()`
  - [ ] regex matching `@/`, `@http`, `@problems`, `@git-changes`, `@terminal`
  - [ ] handle trailing punctuation
- [ ] new server RPC `LoadReference{type, path}` returning content bytes
- [ ] rewrite prompt: each `@path` → `<file path="X">…</file>` block
- [ ] coordinate with #1 — `@`-references and `/add` populate the same WorkingSet
- [ ] keep parsing **client-side** so providers/agents stay reference-agnostic

---

## 8. Tool execution invisible — no `Reading X` / `Running Y` narration

**Reference**: openhands — `research/openhands/openhands/events/action/`. 17+ typed Action subclasses: `FileReadAction`/`FileWriteAction`/`FileEditAction`, `CmdRunAction`/`IPythonRunCellAction`, `BrowseURLAction`/`BrowseInteractiveAction`, `AgentThinkAction`/`AgentDelegateAction`/`AgentFinishAction`, `RecallAction`/`CondensationAction`/`TaskTrackingAction`, `MessageAction`, `MCPAction`. Plus `ActionConfirmationStatus(CONFIRMED|REJECTED|AWAITING_CONFIRMATION)` and `ActionSecurityRisk` enums. Each typed action has its own field schema; frontend renders different action kinds differently.

**gil current state**: Server emits typed events for protocol concerns (`permission_ask`, `clarify_requested`, `mcp_registry_loaded`, `ssh_sync_pushed`, `plan_updated`, `compactor_setup_error`, `role_providers_error`) — see `server/internal/service/run.go` `Type: "..."` literals. **No typed `tool_call`/`tool_result`/`file_read`/`cmd_run` events.** `grep "tool_call|tool_result" server/` → only test fixtures (`core/stuck/detector_test.go`). Chat surface during run shows only strip ticking + occasional `AssistantText` if the model voluntarily narrates.

**Gap (high)**: Second-largest contributor to "feels like LLM endpoint" after #2. User starts 5-min run, sees `[run · iter 7 · $0.40]` for 3 minutes with no output, can't tell if stuck/looping/working. opencode/openhands/cline/codex all narrate tool calls inline. Anthropic returns `tool_use` blocks, OpenAI returns `function_call` deltas — gil just doesn't surface them.

**TODO**:
- [ ] `core/runner` emits per-tool-invocation events
  - [ ] `Event{Type: "tool_call", Data: {name, args, preview}}`
  - [ ] `Event{Type: "tool_result", Data: {ok, summary, truncated}}`
- [ ] `mapRunEventToTracker` (or sibling) recognizes them
- [ ] new `Renderer.ToolNarration(kind, summary)` method
- [ ] StdoutChatRenderer prints dim inline text
  - [ ] `· read src/main.go (1.2KB)`
  - [ ] `· ran "go test ./..." (exit 0, 12s)`
- [ ] consolidate with #4 — both want typed pipeline, possibly single `AssistantOutput{kind, ...}` redesign

---

## 9. Session model thin — no name, no fork, no Update RPC

**Reference**: goose — `research/goose/crates/goose/src/session/session_manager.rs:56`. Session has 22 fields + 7 SessionType variants (User/Scheduled/SubAgent/Hidden/Terminal/Gateway/Acp). Notable: `name` + `user_set_name` flag, `thread_id` (branching), `recipe` + `user_recipe_values` (parameterised templates), `accumulated_input_tokens`/`accumulated_output_tokens` separated. Fluent `SessionUpdateBuilder` for partial updates.

**gil current state**: Session at `proto/gil/v1/session.proto:10` has 15 fields. RPCs Create/Get/List/Delete (no Update). Missing vs goose:
- no `name` field (only `id` + free-form `goal_hint`)
- no SessionType discriminator
- no `thread_id`/branch/fork
- no `Update` RPC — rename requires delete+recreate (loses spec)
- input/output tokens not separated
- ListRequest filter is `status_filter` only (no name/working_dir search)
- chat surface: `/sessions /switch /new`, no `/rename`, `/fork`, `/delete`

**Gap (medium-high for power users)**: Long-running workflows need branching/naming. User investigating two parallel approaches needs to fork the interview at ready-to-freeze and run both — today impossible. Hex IDs alone can't differentiate sessions.

**TODO**:
- [ ] proto additions on Session
  - [ ] `name` + `name_user_set`
  - [ ] `parent_session_id` (for fork)
  - [ ] separate `input_tokens` / `output_tokens` accumulated
  - [ ] `SessionType` enum
- [ ] new RPCs
  - [ ] `SessionService.Update(id, patch)`
  - [ ] `SessionService.Fork(session_id, from_iteration)`
- [ ] chat slashes
  - [ ] `/rename <name>`
  - [ ] `/fork [<from-iter>]`
  - [ ] `/delete`
- [ ] List filter: by name, by working_dir
- [ ] hide scheduled / sub-agent sessions from default `/sessions` view

---

## 10. Edits write without per-edit approval; diff is summary-only

**Reference**: opencode — `research/opencode/packages/opencode/src/tool/edit.ts:58` `EditTool`. (1) **9 replacer strategies** (Simple/LineTrimmed/BlockAnchor/WhitespaceNormalized/IndentationFlexible/EscapeNormalized/MultiOccurrence/TrimmedBoundary/ContextAware) progressively loosen match. (2) **Pre-write approval** at lines 97-105: `yield* ctx.ask({permission: "edit", patterns, always, metadata: {filepath, diff}})` shows diff *before* touching file. (3) Per-file `Semaphore` lock. (4) LSP-aware auto-format. (5) `Bus.publish` events `File.Event.Edited`, `FileWatcher.Event.Updated` for plugin reaction.

**gil current state**:
- `core/tool/edit.go` has 2 tiers (Tier1, Tier2_PreservesIndent) vs opencode's 9 — single fuzzy axis (whitespace), no block-anchor or indentation-flexible fallback.
- **No permission gate inside edit/applypatch tools.** `grep "permission|Confirm|preview" core/tool/edit.go core/tool/applypatch.go` → 0 matches. Edit just writes.
- `Renderer.DiffHunk { Path, Added, Removed, Snippet }` (`renderer.go:45-50`) — counters + preview blob, no per-line array. /diff is summary, /merge is all-or-nothing (returns error today, gap noted in #2).
- No `Bus.publish` analog for future plugins.

**Gap (high)**: After /run finishes, user gets `[done]` strip + flat /diff. With no per-edit approval, edits already landed before user saw them. Summary-only DiffHunk → can't selectively accept hunks — all-or-`/quit`-and-lose. aider/claude-code/opencode all gate destructive edits per-call.

**TODO**:
- [ ] cheap part: extend `DiffHunk`
  - [ ] add `Lines []DiffLine{Kind: "+"|"-"|" ", Text}`
  - [ ] StdoutChatRenderer renders unified diff inline
- [ ] per-hunk apply
  - [ ] `/accept <hunk-id>` slash
  - [ ] new `ApplyHunk` server RPC
- [ ] per-edit permission_ask emission
  - [ ] inside `core/tool/edit.go` and `core/tool/applypatch.go`
  - [ ] gated by autonomy dial:
    - [ ] OFF/ASK_PER_ACTION → always ask
    - [ ] ASK_DESTRUCTIVE → ask for new files / large deletions
    - [ ] FULL → skip
- [ ] eventually port more replacer strategies (current 2-tier brittle on real codebases)
- [ ] (long-term) bus/event surface for plugin reaction (entry 3 hooks)

---

## 11. No CHAT_ONLY mode — every prompt funnelled through interview

**Reference**: goose — `research/goose/crates/goose/src/config/goose_mode.rs:24`. `GooseMode` has 4 variants: Auto (default), Approve, SmartApprove, **Chat** (chat only, no tool calls). At `agents/agent.rs:1399` Chat mode short-circuits the agent loop. `prompt_manager.rs:174` swaps system prompt to chat-style. aider has the same idea via `/ask` (chat-only), `/code` (default), `/architect` (design-only).

**gil current state**: `AutonomyDial` (`proto/gil/v1/spec.proto:174-180`) has 4 variants: PLAN_ONLY (≈ goose Approve in spirit), ASK_PER_ACTION (≈ Approve), ASK_DESTRUCTIVE_ONLY (≈ SmartApprove), FULL (≈ Auto). **No `CHAT_ONLY` variant.** PLAN_ONLY still flows through interview — model is system-prompted as interviewer. No path for "just chat with the LLM, no slot fill, no spec, no run". `grep "ChatMode|chat_mode|chat-only" cli/internal/chat server/internal/service core` → 0 matches. AutonomyDial is also per-spec (frozen at /run) rather than session-level switch.

**Gap (medium-high)**: Structural cause of the user's CoT-leakage / "feels like an LLM endpoint" complaint. Every prompt — even "what model are you?" — lands in the interview engine which is system-prompted to extract slots and emit `slot_filled`/`adversary`/`ready_to_freeze` notes. No escape hatch.

**TODO**:
- [ ] add session-level mode (separate from spec-level AutonomyDial)
  - [ ] new `SessionMode` enum: `code | chat | plan`
  - [ ] cleaner separation: AutonomyDial = tool authorization, SessionMode = whether interview fires at all
- [ ] `/chat-mode <code|chat|plan>` slash sets it
- [ ] server checks flag *before* entering interview engine
  - [ ] Chat mode → route prompt straight to provider with chat-style system prompt, no slot extraction
- [ ] coordinate with #4 (reasoning split)
  - [ ] Chat mode: reasoning visible by default
  - [ ] Code mode: reasoning collapsed

---

## 12. Chat input is bare bufio.Scanner — no history/completion/multiline

**Reference**: aider — `research/aider/aider/io.py`. Built on `prompt_toolkit`: `FileHistory` (persistent up-arrow), `ThreadedCompleter` (slash + file paths, fuzzy), multi-line mode toggle (`/multiline-mode`, Esc-Enter, paste detect), `/editor` opens `$EDITOR`, vi+emacs editing modes, Pygments syntax highlighting, placeholder text, dumb-terminal fallback.

**gil current state**: `cli/internal/chat/repl/loop.go:54-55` —
```go
scanner := bufio.NewScanner(cfg.In)
scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
```
Standard library Scanner, single-line per Scan(), 1 MiB max line. No history (up arrow does nothing), no autocomplete (typo `/sesions` wastes a turn), no multi-line, no `/editor`, no paste detection, no syntax highlighting.

**Gap (medium — daily friction)**: Not architectural, but constant. Every typo forces re-type, every multi-paragraph context dump forces copy-paste. Difference between feeling like a REPL vs a shell.

**TODO**:
- [ ] wrap input acquisition behind `inputReader` interface
  - [ ] dogfood/CI keep using `bufio.Scanner`
  - [ ] chat default uses readline-class library (chzyer/readline mature; peterh/liner lighter)
- [ ] features
  - [ ] persistent history at `~/.gil/history`
  - [ ] slash completion (reuse `IsKnownSlash` set)
  - [ ] file path completion (cwd + git ls-files prefix)
  - [ ] `/editor` slash invokes `$EDITOR`
  - [ ] multi-line via `\` continuation or Esc-Enter binding
- [ ] `--no-readline` flag for tests / piped input

---

## 13. No /interrupt — and the in-app hint promises a CLI verb that doesn't exist

**Reference**: codex — `research/codex/codex-rs/protocol/src/protocol.rs:405-412`. Two-level interrupt model: `Op::Interrupt` (abort current task, leave background shells alive) → server replies `EventMsg::TurnAborted`. `Op::CleanBackgroundTerminals` explicitly kills background processes. First-class `Interrupted` state (lines 1818, 3768) so user can resume rather than `/quit`. Per-process abort of just current LLM turn is normal across codex/claude-code/opencode/cline.

**gil current state**: Chat REPL hints at `loop.go:104` and `:168` say:
> `run-time prompts are V1.1; for now wait for done, or 'gil stop <id>' from another shell`

> `stuck after recovery — V1.1 will offer /interrupt; for now 'gil stop <id>' from another shell`

**Both hints are wrong** — `ls cli/internal/cmd/ | grep -iE "stop|abort|interrupt|pause"` returns nothing. **No `gil stop` verb exists.** `proto/gil/v1/run.proto rpc` listing has Start/Tail/Restore/AnswerPermission/AnswerClarification/RequestCompact/PostHint/Diff — no `Stop`/`Abort`/`Interrupt`/`Pause` RPC.

**Gap (high — dogfood blocker)**: User with runaway 50-iteration run has no recovery besides killing daemon process (loses session/events/budget). Worse: in-app hint promises an escape that doesn't exist; first-time users will type `gil stop <id>` and hit `unknown command "stop"`.

**TODO**:
- [ ] **immediate**: fix false hint copy in `loop.go:104` and `:168` so it doesn't promise a non-existent verb
- [ ] add Stop on protocol + CLI
  - [ ] `RunService.Stop(session_id, reason)` RPC
  - [ ] `gil stop <id>` CLI verb
  - [ ] `/interrupt` slash that calls Stop
- [ ] FSM
  - [ ] new `STOPPED_BY_USER` SessionStatus (or reuse existing `STOPPED`)
  - [ ] `Interrupted` distinguished from `Done` so resume works
- [ ] (later) codex-style two-level: Interrupt vs CleanBackgroundProcesses split for child shells

---

## 14. Rich per-role model architecture, zero runtime switch surface

**Reference**: aider — `research/aider/aider/commands.py` exposes 7 model slashes: `/model`, `/editor-model`, `/weak-model`, `/models`, `/chat-mode`, `/think-tokens`, `/reasoning-effort`. 3 roles. User can retune mid-session.

**gil current state**: `ModelConfig` (`proto/gil/v1/spec.proto:120-135`) is *richer* than aider's — **8 roles**: `main`, `weak`, `editor`, `adversary`, `interview`, `planner`, `slot`, `audit`. Each role can map to a different (provider, model_id) pair. More sophisticated than any reference harness.

**But runtime surface is empty.** `grep "case \"model" loop.go` → 0 matches. `grep "rpc.*Model|UpdateModel|SetModel" proto/gil/v1` → 0 matches. ModelConfig is set at spec-freeze time and never mutates. **Worse**: `grep "ReasoningEffort|ThinkTokens|reasoning_effort" proto/gil/v1/*.proto` → 0 matches. No proto field for Anthropic extended-thinking budget or OpenAI reasoning_effort. The capability isn't even representable on the wire.

**Gap (medium-high — wasted architecture)**: gil designed a more advanced model topology than the references but shipped no controls to use it. User wanting Opus for interview/planner and Haiku for slot/audit must edit the spec by hand. Mid-conversation tuning impossible. Lack of `reasoning_effort`/`think_tokens` fields means token-counting work (P27.5/P28) can't reason accurately about thinking-block costs.

**TODO**:
- [ ] proto additions on `ModelChoice`
  - [ ] `reasoning_effort` (`"low"|"medium"|"high"|int`)
  - [ ] `thinking_tokens` (int64, 0=disabled)
- [ ] new RPC `SessionService.UpdateModelConfig(role, provider, model_id, reasoning_effort?, thinking_tokens?)`
- [ ] chat slashes
  - [ ] `/model <role> <name>`
  - [ ] `/think <budget>`
  - [ ] `/reasoning <effort>`
- [ ] provider adapters flow new fields
  - [ ] `core/provider/anthropic.go` reads thinking_tokens
  - [ ] `core/provider/openai.go` reads reasoning_effort

---

## 15. No direct shell exec from chat — every test/lint round-trips the agent

**Reference**: aider — `research/aider/aider/commands.py`. Shell-exec slashes:
- `/run <cmd>` (alias `!<cmd>`) — line 1013
- `/test <cmd>` — output added to chat *only on non-zero exit* — line 993
- `/lint` — line 356
- `/commit [<msg>]` — line 337
- `/git <args>` — line 967
- `/web <url>` — fetch URL into chat
The `!` shorthand makes `/run` ergonomic — `!ls`, `!go test ./...` are one-key drops to shell.

**gil current state**: 10 slashes — `/help /sessions /switch /new /spec /status /diff /merge /run /quit`. **`/run` here means *start an agent run* after spec-freeze, not "execute shell command".** `grep "case \"(run|test|lint|git|commit|exec|sh)\"" loop.go` returns only `case "run"`. No shell-exec slashes, no `!` shortcut, no `/web`. User who wants quick test must drop to another terminal or type a long prompt that goes interview→freeze→run for a 1-second cmd.

**Gap (medium — daily friction)**: Same shape as #12 — not architectural, missing affordance per iteration. Painful for tight test/lint loops.

**TODO**:
- [ ] disambiguate naming
  - [ ] consider renaming agent-run slash to `/start` or keep `/run` and use a different slash for shell
- [ ] new shell-exec slashes (client-side, no daemon RPC)
  - [ ] `/sh <cmd>` or `/!`
  - [ ] `/test <cmd>`
  - [ ] `/lint`
  - [ ] `/git <args>`
  - [ ] `/web <url>`
- [ ] implement in `dispatchSlash` via `os/exec` + stream output to `Renderer.SystemNote`
- [ ] ergonomic case: detect bare `!cmd` at input parse, route to /sh
- [ ] coordinate
  - [ ] `/web <url>` adds fetched content to WorkingSet (#1)
  - [ ] `/test` failures auto-attach failing output to next prompt

---

## 16. Onboarding ends at first run — no `gil configure` umbrella

**Reference**: goose — `research/goose/crates/goose-cli/src/commands/configure.rs` (~1700 lines). Single `goose configure` command with 19+ interactive dialogs: telemetry consent, interactive_model_search, select_model_from_list, prompt_unlisted_model, try_store_secret (keyring), toggle_extensions_dialog, configure_builtin/stdio/streamable_http_extension, configure_extensions_dialog, remove_extension_dialog, prompt_extension_timeout/description/name, collect_env_vars, collect_headers, configure_goose_mode_dialog, configure_telemetry_dialog, configure_tool_output_dialog, configure_keyring_dialog, toggle_experiments_dialog. All reachable *after* first run.

**gil current state**: First-run onboarding solid:
- `cli/internal/cmd/chat_onboarding.go` (250 lines) — `runOnboardingNoInit`/`runOnboardingNoCreds` gates
- `auth_wizard.go` (616 lines) — provider login
- `init.go` (296 lines) — `gil init`

**No umbrella reconfigure command.** `ls cli/internal/cmd/ | grep -iE "config|setup|wizard|prefs"` → only `auth_wizard.go` (login flow, not generic configure). MCP servers added via `gil mcp add <args>` non-interactively. No interactive model search. No keyring dialog (creds via `gil auth` only).

**Gap (medium — discoverability)**: First-time users get good onboarding, but later when they want to "switch default model" or "add MCP server" they must read `gil --help` and parse advanced verb mode (now hidden behind `Advanced (headless / scripting)` group post-P26). goose makes both first-run and post-run config the same dialog — user learns one surface.

**TODO**:
- [ ] new `cli/internal/cmd/configure.go` in `Setup:` group (alongside init/auth/doctor)
- [ ] menu structure
  - [ ] provider creds (reuse auth_wizard flows)
  - [ ] model selection (interactive search)
  - [ ] MCP servers (add/remove/edit)
  - [ ] autonomy default
  - [ ] experimental flags
- [ ] coordinate
  - [ ] #14 — `/model` slash shares dialog code with `gil configure → models`
  - [ ] #3 — once MCP wired, `gil configure → MCP` is primary discovery path

---

## 17. Compaction is invisible to the chat user (no events, no /compact slash)

**Reference**: opencode — `research/opencode/packages/plugin/src/index.ts:303-324`. Two compaction hooks:
- `experimental.session.compacting(input{sessionID}, output{context, prompt?})` — fires on entry, lets plugins inject context strings or override the compaction prompt entirely
- `experimental.compaction.autocontinue(input{sessionID, agent, model, provider, message, overflow}, output{enabled})` — fires after compaction; `overflow` flag distinguishes forced vs voluntary, `enabled` defaults to true and lets plugins disable the synthetic auto-continue user turn

So opencode treats compaction as a first-class observable + interceptable event.

**gil current state**: P27 shipped a working compactor (`core/compact/`, server-wired at `core/runner/factory.go`). Server emits two compaction-adjacent events:
- `compactor_setup_error` (`server/internal/service/run.go:1198`) — error case only
- `compact_requested` (`server/internal/service/run_extras.go:64`) — when something *asks for* compaction

Plus budget signals (`budget_warning`, `budget_exceeded` at run.go:862,871). **No `compact_started` / `compact_completed` event** with before/after token counts. Chat surface knows zero — `grep "compact|Compact" cli/internal/chat` → 0 matches. Even though `RequestCompact` RPC exists (`proto/gil/v1/run.proto:53`), there's no `/compact` slash to manually trigger it — `grep "case \"compact\"" loop.go` → 0 matches.

**Gap (medium-high)**: User on a long session sees iter 30 suddenly take 8 seconds where iter 1-29 took 2 seconds, has no idea it's because the compactor just rewrote half their context. No control to compact preemptively, no insight into when overflow vs voluntary, no way to inspect what got summarized.

**TODO**:
- [ ] server emits compaction lifecycle events
  - [ ] `compact_started` with payload `{trigger: "overflow"|"manual"|"voluntary", before_tokens}`
  - [ ] `compact_completed` with payload `{after_tokens, dropped_msgs, kept_msgs, summary_chars}`
  - [ ] `compact_failed` with reason
- [ ] adapter `mapRunEventToTracker` recognizes the lifecycle events
- [ ] Renderer surface
  - [ ] system note: `· compacting context (80k → 30k tokens) …`
  - [ ] strip variant for in-progress compaction (or just dim the existing strip while compacting)
- [ ] new `/compact` slash dispatch
  - [ ] calls existing `RequestCompact` RPC
  - [ ] reports result via system note
- [ ] coordinate with #6 (token surface) — `/tokens` should show "next compact at X tokens"
- [ ] long-term: hook surface analogous to opencode's `experimental.session.compacting` for plugin interception (depends on #3 plugin system)

---

## 18. Chat REPL surfaces raw gRPC errors; the friendly-hint wrapper exists but isn't called there

**Reference**: codex tests at `research/codex/codex-rs/codex-client/tests/ca_env.rs:93-119`. `rejects_empty_pem_file_with_hint` and `rejects_malformed_pem_with_hint` assert that an error message contains:
- what went wrong (`"no certificates found in PEM file"`)
- which env var to check (`CODEX_CA_CERT_ENV`)
- alternative path (`SSL_CERT_FILE`)

The pattern: every user-facing error must carry (a) cause + (b) where to look + (c) suggested action.

**gil current state**: The pattern *is* implemented — `cli/internal/cmd/errwrap.go` has `wrapRPCError(err)` with 11+ branches, each producing a typed `cliutil.Wrap(err, summary, hint)` with the same shape codex tests for:
- `no credentials for anthropic` → hint `set ANTHROPIC_API_KEY, or run "gil auth login anthropic"`
- `must be frozen before run` → hint `run "gil interview <id>" to finish, then "gil spec freeze <id>"`
- `no active run for session` → hint `start one with "gil run <id>", then "gil events <id> --tail"`
- `cannot restore … while running` → hint `wait for the run to finish, then retry "gil restore"`
- ... etc.

**But `wrapRPCError` is called only from verb-mode commands.** `grep wrapRPCError cli/internal/` shows 11 call sites — `run.go`, `resume.go`, `events.go`, `status.go`, `session.go`, `watch.go`. **Zero calls from `cli/internal/chat/repl/`.** When a chat slash like `/run` returns the same gRPC error, the REPL formats it raw at `loop.go:98`:

```go
fmt.Sprintf("/%s failed: %v", cmd, err)
```

So the chat user gets `/run failed: rpc error: code = FailedPrecondition desc = session "abc123" must be frozen before run (current status: created)` — the verb-mode user gets the wrapped hint pointing them at `gil interview <id>`.

**Gap (medium — wiring gap, no design work needed)**: Friendly-hint translation table already exists; chat REPL just doesn't call it. Same severity-multiplier as #2 (permission_ask wiring), #5 (retry visibility), #6 (token surface): pieces all there, missing connector.

**TODO**:
- [ ] route chat REPL errors through `wrapRPCError` before rendering
  - [ ] `dispatchSlash` returns wrapped error
  - [ ] `loop.go:98` uses the wrapped message + hint instead of raw `%v`
  - [ ] same for `loop.go:109` (send failed) and `:117,137` (stream errors)
- [ ] decide rendering shape on Renderer side
  - [ ] new `Renderer.Error(summary, hint string)` for two-line output, OR
  - [ ] reuse `SystemNote` with a new `NoteError` kind
- [ ] add the chat-specific error sentences to `wrapRPCError` switch
  - [ ] `merge is not yet supported by the server` (currently raw at `grpc_client.go:336`)
  - [ ] `session not found: <id>` (at `grpc_client.go:94`) — hint `try /sessions to list`
- [ ] coordinate with #13 — when Stop RPC lands, its NotFound case wants a hint too

---

## 19. Stuck detector emits 6 typed patterns; chat adapter listens for the wrong event name

**Reference**: codex turn-state model has `Interrupted` as a first-class FSM state (cycle 13, codex-rs/protocol/src/protocol.rs:1818, 3768) so the user sees *why* a turn ended. The expectation across modern harnesses: when the agent detects it's looping, the UI shows what kind of loop it detected (repeated tool call, alternating tool ping-pong, no-file-progress, etc.) so the user can intervene with the right correction.

**gil current state**: gil has a sophisticated stuck detector at `core/stuck/detector.go` with **6 typed patterns**:
- `PatternRepeatedActionObservation` (same tool_call+tool_result 4+ times in window)
- `PatternRepeatedActionError` (same tool_call followed by error 3+ times)
- `PatternMonologue` (3+ consecutive provider_response with zero tool_calls)
- `PatternPingPong` (strict alternation between two tool signatures 6+ events)
- `PatternContextWindowError` (2+ run_error events mentioning context/token overflow)
- `PatternNoProgress` (K+ iters with verifier stalled and files empty/churning)

The detector is wired in production at `server/internal/service/run.go:1242`:
```go
loop.StuckDetector = &stuck.Detector{Window: 200}
```

And the runner emits typed events at `core/runner/runner.go:727,746`:
```go
a.emit(event.SourceSystem, event.KindNote, "stuck_detected", map[string]any{
    "pattern": sig.Pattern.String(),
    "detail":  sig.Detail,
    "count":   sig.Count,
})
// ... and on recovery:
a.emit(event.SourceSystem, event.KindNote, "stuck_recovered", map[string]any{
    "strategy":    a.StuckStrategy.Name(),
    "new_model":   dec.NewModel,
    "explanation": dec.Explanation,
})
```

**But the chat adapter listens for the wrong event name.** `cli/internal/chat/repl/grpc_client.go:381` and `loop.go:166` both branch on `"run.stuck"` — which is **not the type the runner emits**. The actual emitted Type is `"stuck_detected"`. So the chat surface never receives any stuck signal.

If it ever did receive them, it would still drop the rich payload — the current `mapRunEventToTracker` for `run.stuck` ignores `pattern`, `detail`, `count`, so the user could not see *which* of the 6 patterns triggered, only "stuck" generically.

**Gap (high — wiring gap with broken event name)**: This is the most expensive failure mode for an agent ("it's just spinning, I don't know why"). gil already has the diagnosis (6 patterns + count + detail), the recovery (`stuck_recovered` with model-switch explanation), and the placeholder strip variant (`PhaseStuck` at `loop.go:102`). All wires are cut at the adapter.

**TODO**:
- [ ] fix the event name mismatch
  - [ ] decide canonical name: keep `stuck_detected` server-side and update adapter, OR rename server event to `run.stuck` to match the existing chat code
  - [ ] same for `stuck_recovered` ↔ adapter (currently the adapter has no recovered handler at all)
  - [ ] add a regression test asserting the names match end-to-end
- [ ] surface the rich payload, not just the phase
  - [ ] adapter parses `pattern` / `detail` / `count` from `data_json`
  - [ ] `TrackerInput` extended with `StuckPattern`, `StuckDetail`, `StuckCount`
  - [ ] system note: `· stuck: ping-pong between Read+Edit (12 events) — auto-recovering with stronger model …`
- [ ] handle the recovery path
  - [ ] new `stuck_recovered` adapter case
  - [ ] system note: `· recovered: switched main model from haiku → sonnet (reason: ping-pong loop)`
- [ ] `PhaseStuck` strip variant carries pattern hint, e.g. `[stuck · ping-pong · auto-recovering]`
- [ ] `/why-stuck` slash to dump the recent stuck signals + recovery decisions for the current run (could reuse `Diff`-style block render)

---

## 20. Checkpoint/restore is a CLI verb only — chat has no /undo or /checkpoints

**Reference**: aider has `/undo` (`research/aider/aider/commands.py` cmd_undo) — undo the last commit (or chat edit) without leaving the chat. The mental model: each agent turn that modifies files leaves a commit; the user can always step back one without verb-mode plumbing. goose's session manager keeps an event log + recipe state so a session can be resumed from any prior point (cycle 9 SessionUpdateBuilder). claude-code has `/restore` listing snapshots inside the chat.

**gil current state**: gil has full checkpoint infrastructure:
- `core/checkpoint/shadow.go:130` — `ShadowGit.ListCommits(ctx)` returns `[]CommitInfo`
- `proto/gil/v1/run.proto:32` — `Restore(RestoreRequest) returns RestoreResponse` RPC
- `cli/internal/cmd/restore.go` — `gil restore <session-id> <step>` verb works (1-indexed, supports negative for "from latest")
- server uses ListCommits internally at `run.go:1382` to validate step bounds

**Two gaps:**
1. **ListCheckpoints RPC isn't exposed** — `grep ListCheckpoints proto/gil/v1` → 0 matches. Clients can't enumerate checkpoints; they must guess a step number and ask the server to validate. The verb command at `restore.go` outputs the total only after a successful Restore; there is no read-only "what checkpoints exist" call.
2. **Chat REPL has zero checkpoint slashes** — `grep "case \"(restore|undo|checkpoint|history)\"" loop.go` → 0 matches. To go back one step the chat user must `/quit`, run `gil restore <id> -1`, restart `gil chat`. Each step costs a session resume.

The verbose pattern at `restore.go:62-63` shows what user actually wants:
```
Restored session 01abc to step 5 / 12 (commit 7e3a2b1: post-iter-3)
```
That same line should be inline in chat after `/undo`.

**Gap (medium-high)**: gil already has the substrate. Restore-to-checkpoint is a critical agent-trust feature (the user knows they can't accidentally lose work, so they're more willing to grant FULL autonomy). Hiding it behind verb-mode means the chat user defaults to lower autonomy out of caution.

**TODO**:
- [ ] expose `RunService.ListCheckpoints(session_id)` RPC
  - [ ] returns `[]Checkpoint{step, commit_sha, commit_message, timestamp, files_changed}`
  - [ ] use existing `ShadowGit.ListCommits` under the hood
- [ ] chat slashes
  - [ ] `/checkpoints` — render the list (Renderer.Diff-style block could reuse format)
  - [ ] `/undo` (alias `/restore -1`) — go back one step, render confirm Y/N first
  - [ ] `/restore <step>` — explicit step jump
- [ ] handle restore-while-running gracefully
  - [ ] `wrapRPCError` already has the "cannot restore … while running" branch — reuse via #18
- [ ] coordinate
  - [ ] #9 (session fork) — `/fork` could default to forking from the current checkpoint
  - [ ] #10 (per-hunk diff) — `/undo` after a multi-hunk edit could prompt "undo all? or pick hunks to keep?"

---

## 21. Assistant output is raw bytes — no markdown rendering, no code-block awareness

**Reference**: codex TUI ships a complete markdown pipeline (`research/codex/codex-rs/tui/src/markdown.rs:8` `append_markdown`, plus `markdown_render.rs` and `markdown_render_tests.rs`). Each agent turn is captured as raw markdown (`AgentTurnMarkdown.markdown` at `chatwidget.rs:495`) and re-rendered into ratatui `Line`s with styling — bullets, headers, fenced code blocks with syntax highlighting, file-link rewriting relative to cwd. The TUI also retains `last_agent_markdown` and `latest_proposed_plan_markdown` so users can copy the raw source. opencode/claude-code/openhands all do equivalent work.

**gil current state**: `cli/internal/chat/render/stdout.go:44`:
```go
func (r *StdoutChatRenderer) AssistantText(chunk string) {
    fmt.Fprint(r.out, chunk)
}
```
Raw bytes to stdout. No detection of:
- fenced code blocks (`` ```go ... ``` `` rendered as literal backticks + uncolored code)
- bullet lists (`- foo` shown as the literal hyphen)
- headers (`# Plan` rendered with the `#`)
- inline code (`` `name` `` shown with the backticks)
- file-link rewriting (`[main.go](main.go)` shown as the literal markdown)

The export verb at `cli/internal/cmd/export.go` renders sessions *to* markdown format, but in live chat rendering the term is unused — `grep markdown cli/internal/chat` → 0 matches.

Modern LLMs emit markdown by training default. The user gets the worst of both worlds: the model formats with markdown intent, the terminal shows the syntax characters as garbage.

**Gap (medium-high)**: Single most visible quality difference between gil's chat and codex/claude-code/opencode. Lists, headers, code blocks all degrade to monospace plain text. Code blocks especially — when the model says "here's how to fix it" with a `go` block, gil renders the body uncolored and indistinguishable from prose. Hard to read, hard to trust, hard to copy-extract.

**TODO**:
- [ ] decide rendering approach
  - [ ] inline streaming vs buffer-and-render (codex buffers per turn at `chatwidget.rs` `record_agent_markdown`)
  - [ ] streaming pro: lower latency feel; streaming con: list/code-block boundaries unknown until close — partial render flickers
  - [ ] buffer pro: cleaner output; buffer con: "wall of text appears at end" feel
  - [ ] reasonable default: stream raw chunks while turn is open (existing behavior), then re-render with markdown styling at turn-end
- [ ] markdown library
  - [ ] Go options: `gomarkdown/markdown`, `yuin/goldmark` (most active), `quicklobster/glamour` (charm.sh — already designed for terminal)
  - [ ] glamour is closest to what's wanted: produces styled terminal output directly
- [ ] code-block syntax highlighting
  - [ ] `alecthomas/chroma` is the standard Go highlighter (used by Hugo, glamour wraps it)
- [ ] Renderer interface
  - [ ] consider new `AssistantTurnEnd()` callback so renderer knows when to flush+rerender
  - [ ] OR change `AssistantText(chunk)` signature to track turn boundaries explicitly
- [ ] honor existing flags
  - [ ] `--ascii` falls back to plain text (no ANSI styling)
  - [ ] `NO_COLOR` env honored (already wired for status strip — extend)
- [ ] coordinate
  - [ ] #4 (reasoning split) — reasoning chunks render dim, never styled, never highlighted
  - [ ] #15 (shell-exec slashes) — detected ` ```bash ` blocks could surface "press ! to run this" affordance
  - [ ] #1 (file-context) — detected file paths in markdown links could surface "press /add to include"

---

## 22. Project instructions (AGENTS.md / CLAUDE.md) load silently — chat user can't see what's loaded

**Reference**: codex `research/codex/codex-rs/config/src/config_toml.rs:202-205` — explicit `project_doc_max_bytes` + `project_doc_fallback_filenames` config, plus end-to-end tests at `app-server/tests/suite/v2/thread_start.rs:209-212` for global vs project AGENTS.md precedence. opencode/codex/cline/goose/hermes-agent all have AGENTS.md at the repo root. The user-visible contract: every harness loads project + global instructions, the user can inspect what merged.

**gil current state**: gil already implements discovery. `core/instructions/discover.go` defines:
- `agentsMDFilename = "AGENTS.md"` (line 93) — cross-harness consensus
- `claudeMDFilename = "CLAUDE.md"` (line 94) — Anthropic flavour
- `DisableClaudeMD` opt-out (line 53)
- $GlobalConfigDir / $HomeDir / $project — full layered walk

`Discover` is called in two places:
- `core/runner/runner.go:1533` — at run start, merges into the system prompt
- `core/slash/handlers.go:352` — used by some slash handler

**Two gaps:**
1. **Chat surface zero awareness** — `grep "AGENTS.md|instructions" cli/internal/chat` → 0 matches. The user typing in `gil chat` cannot see (a) which AGENTS.md files were discovered for the current cwd, (b) their precedence order, (c) the merged system-prompt contribution. They must trust `discover.go` silently.
2. **No `/instructions` or `/agents` slash** to print the resolved chain.

This makes debugging "agent isn't following my coding standards" awful — the user has no way to confirm AGENTS.md was even read, much less which one took precedence.

**Gap (medium)**: gil has the harder half (discovery + precedence + opt-outs) but the cheap reveal layer is missing.

**TODO**:
- [ ] new server RPC `GetInstructions(working_dir)` returning list of `{path, source: "global"|"home"|"project", bytes_loaded, sha}` for the resolved chain
- [ ] chat slash
  - [ ] `/instructions` (alias `/agents`) — render the chain with origin labels
- [ ] one-line system note at session start
  - [ ] `· loaded 2 AGENTS.md files (global, project) — /instructions to inspect`
  - [ ] suppressed when no files found (don't bloat zero-config sessions)
- [ ] coordinate
  - [ ] #16 (`gil configure`) — `gil configure → instructions` could show the same data + offer to open a file in $EDITOR
  - [ ] #1 (file-context) — AGENTS.md is implicitly part of the WorkingSet conceptually; `/ls` could show it under a separate "system" header

---

## 23. Subagent runs are invisible to the chat parent

**Reference**: openhands has `AgentDelegateAction` (`research/openhands/openhands/events/action/agent.py:77`) and `AgentFinishAction` so subagent boundaries are first-class events the UI renders. codex has thread spawning. The pattern: parent surface shows subagent start/end inline + the subagent's own progress in a collapsible block.

**gil current state**: gil ships a subagent tool at `core/tool/subagent.go` (the agent can spawn a child agent via tool call) and the runner emits typed lifecycle events at `core/runner/runner.go:1343,1392`:
```go
a.emit(event.SourceSystem, event.KindNote, "subagent_started", map[string]any{...})
// ... and later:
a.emit(event.SourceSystem, event.KindNote, "subagent_done", map[string]any{...})
```
The comment at runner.go:1276 confirms: "inherits the parent's stream so subagent_started / subagent_done / [other subagent events] flow through".

**Chat surface zero awareness** — `grep subagent cli/internal/chat` → 0 matches. The chat adapter doesn't recognize either event. So when the parent agent runs a subagent (which can take 30+ seconds and consume meaningful budget), the chat user sees nothing — the strip continues showing the parent iter, and when the subagent completes its events get dropped.

This is the same shape as #19 (stuck detector wiring) and #17 (compaction events): server emits, adapter ignores.

**Gap (medium-high — overlaps with #8 tool narration)**: Subagent calls are the heaviest tool calls gil supports — surfacing them inline is the difference between "trust the agent's plan" and "what is it doing for 30 seconds".

**TODO**:
- [ ] adapter recognizes subagent lifecycle events
  - [ ] `subagent_started` → system note `· subagent: <task summary>`
  - [ ] `subagent_done` → system note `· subagent done (<summary>, $X.XX, Yk toks)`
  - [ ] (optional) `subagent_iter` for live ticking
- [ ] strip variant during active subagent
  - [ ] `[run · iter 7 · subagent active · $X.XX]` so the user knows it's nested work
- [ ] coordinate
  - [ ] #8 (tool narration) — subagent IS a tool call, so this entry collapses into #8's typed action pipeline once that exists
  - [ ] #6 (token surface) — subagent token cost should attribute correctly in `/tokens` breakdown
  - [ ] #13 (/interrupt) — interrupting the parent must propagate cancellation into the active subagent

---

## 24. Budget warnings emitted on the wire, never surface to the chat user

**Reference**: aider has `--cost-cap` flag + per-turn cost printing so the user sees cost trajectory in real time. The pattern across harnesses: warn before exhaustion (75%, 90% of cap), block at 100%, give the user an upgrade-budget escape hatch. The point of the warning is *agency* — let the user intervene before the agent halts.

**gil current state**: Server already emits both events:
- `budget_warning` — emitted at `core/runner/runner.go:248,658` when crossing 75% (one-shot per crossing, not per iter)
- `budget_exceeded` — emitted when total or cost cap breached

Server even handles them specially at `server/internal/service/run.go:860-871`:
```go
if evt.Type == "budget_warning" || evt.Type == "budget_exceeded" {
    // ... updates session.budget_exceeded sticky bit
    if evt.Type == "budget_exceeded" { ... }
}
```

The session proto already exposes `budget_exceeded` and `budget_reason` fields (`session.proto:35-36`).

**Chat surface zero handling** — `grep -E "budget_warning|budget_exceeded|BudgetExceeded" cli/internal/chat/repl/grpc_client.go` → 0 matches. The adapter doesn't map either event. The strip variant has `$X.XX` cost but no budget cap reference, no warning glyph at 75%, no halt at 100%.

User on a $5 cap session sees `$3.40` ticking up, has no signal that the agent is approaching the cap until the run unceremoniously stops with a generic error.

**Gap (medium-high — same wiring shape as #2/#5/#17/#19)**: 5th instance of the same pattern: server emits typed event, chat REPL ignores it, all wiring evidence is in commit history not in code.

**TODO**:
- [ ] adapter maps `budget_warning` + `budget_exceeded`
  - [ ] payload reads percent_consumed, dimension ("tokens" | "cost"), absolute remaining
- [ ] strip variant updates
  - [ ] near cap: `[run · iter 7 · $3.50/$5.00 ⚠]` (warning glyph, dim if not yet at 75%)
  - [ ] over cap: `[stopped · budget exceeded · $5.10/$5.00]`
- [ ] system note on first warning crossing per session
  - [ ] `· budget warning: 75% of $5.00 cost cap consumed — /budget to inspect or raise`
- [ ] new `/budget` slash
  - [ ] shows current consumption per dimension
  - [ ] could call existing session row data plus optional UpdateSpec RPC to raise the cap mid-run
- [ ] coordinate
  - [ ] #6 (`/tokens`) — combine into `/budget` view (token + cost + iter together)
  - [ ] #13 (Stop) — exceeded budget should stop with a typed reason, not generic error

---

## 25. No transcript save / share — exporting requires `/quit` to verb mode

**Reference**: aider keeps two persistent files (`research/aider/aider/io.py:242,258,317`):
- `chat_history_file` (markdown of the conversation)
- `llm_history_file` (raw model in/out, redacted)
Both append continuously while the chat is open. plus `/copy` (cmd_copy at `commands.py`) copies the last assistant message to clipboard. The user can open the markdown file in another window mid-session for context, or share it after.

**gil current state**: gil has a robust `gil export` verb — `cli/internal/cmd/export.go` 600+ lines, three formats (markdown / json / jsonl), MaskSecrets pass for safety, smart truncation. **But it's verb-only.** Chat REPL has no `/save`, `/export`, `/share`, `/copy` slash — `grep "case \"(save|export|share|transcript|history|copy)\"" loop.go` → 0 matches. Nothing gets persisted live. To share or save mid-conversation, the user must `/quit`, run `gil export <session> --format markdown > out.md`, restart `gil chat`.

aider's auto-save model also covers a use case gil misses: ad-hoc resume by re-reading the markdown file outside the harness. With gil, the user has zero artifact between session ID and a /run-only output — a fact about the session that exists outside the daemon.

**Gap (medium)**:
- mid-session sharing painful
- no live artifact (file the user can `tail -f` in another window)
- `/copy` ergonomics absent (aider users copy code blocks 30+ times a day)

**TODO**:
- [ ] new chat slashes (client-side, daemon already has the data via Tail)
  - [ ] `/save [<path>]` — dumps markdown to file (default `~/.gil/sessions/<id>/transcript.md`)
  - [ ] `/export <markdown|json|jsonl>` — same as verb-mode export, runs in place
  - [ ] `/copy` — copy last assistant turn to clipboard (`atotto/clipboard` Go lib, OS-aware)
  - [ ] `/copy <hunk-id>` — copy a specific code block from the last turn (depends on #21 markdown rendering for block detection)
- [ ] live transcript option
  - [ ] `gil chat --transcript <path>` flag opens append mode at session start
  - [ ] StdoutChatRenderer also writes to the file (mirror of stdout, with markdown + ANSI stripped)
- [ ] reuse export.go's MaskSecrets pass — chat saves should redact too
- [ ] coordinate
  - [ ] #16 (`gil configure`) — default transcript path / format under user prefs
  - [ ] #21 (markdown rendering) — `/copy <block>` parses the rendered markdown to find code blocks

---

## 26. No /clear or /reset — chat history accumulates until /quit

**Reference**: aider (`research/aider/aider/commands.py`):
- `cmd_clear` (line 411) — clears the chat history but keeps in-context files and the repo map. Used when the conversation has drifted but the working set is still right.
- `cmd_reset` (line 439) — wipes everything (history + files + state). Fresh start without `/quit`+restart.
The pattern: two distinct depths of "forget". claude-code, opencode, openhands all expose at least one form of in-session reset.

**gil current state**: 10 slashes, none for context wipe — `grep -E 'case "(clear|reset)"' loop.go` → 0 matches. The only way to start over is `/quit` + `gil chat` (and even then the session row persists with all its events; you'd need `/new` after restart to truly fresh-start). There's no path to say "the answers so far were wrong, keep my session but throw out the history" — interview slot data carries forward whether or not the user wants it to.

**Gap (medium)**: When the interview misreads intent and the agent fills slots wrong, the user wants to back out without losing the session row's metadata (working_dir, name once #9 lands, configured models). gil forces them to `/new`, which throws everything including the configuration.

**TODO**:
- [ ] decide semantics
  - [ ] `/clear` — wipe message history, keep session row + WorkingSet + spec progress
  - [ ] `/reset` — same as /clear plus drop spec progress (back to fresh interview)
  - [ ] confirm both before executing (Renderer.Confirm)
- [ ] server RPC
  - [ ] `SessionService.Clear(session_id, scope: "history"|"all-but-config")` — drops events with scope filter
  - [ ] reuses existing event store; just truncates the session's stream past a marker
- [ ] interaction with checkpoints (#20)
  - [ ] `/clear` should NOT drop checkpoint history — those are file-state, not chat-state
  - [ ] confirm dialog mentions: "files unchanged; checkpoints kept; / restore N still works"
- [ ] coordinate with #9 (session model) — `/clear` is a simpler-shaped sibling of `/fork` (both create a usable surface from a drifted session)

---

## 27. Web search and web fetch tools exist; chat shows nothing when they fire

**Reference**: aider has `cmd_web` to add a URL's content to chat. opencode/cline parse @http URLs as inline mentions (cycle 7 — entry 7). All harnesses surface "fetched X (1.2KB)" when an external resource is pulled, both for trust (the user sees what hit the network) and for context (large fetches eat tokens visibly).

**gil current state**: gil has both tools as first-class implementations:
- `core/tool/websearch.go` — search tool the agent can call
- `core/tool/webfetch.go` — URL fetch tool

Both are in the model's tool palette. **But the chat surface gets nothing when they fire.** No `web_search`/`web_fetch` events emitted by `core/runner` (search of `Type:\s*"web_` in server+core finds 0 matches outside test fixtures), so even if a chat adapter wanted to render them it has no signal. The user prompts "find recent docs on X", the agent calls `web_search`, the model gets the result, the assistant text mentions the search, but the chat shows only `· iter 3` ticking — no indication a network call happened. Same blind spot for `web_fetch`.

This is a particularly bad blind spot because:
- web tools have privacy implications (the user might want to know "we just hit duckduckgo.com")
- they're slow (search 1-3s, fetch up to 10s) so the silence feels like a hang
- they consume tokens disproportionately (a fetched page can be 5-50k tokens)

**Gap (medium-high — instance of the #8 tool-narration pattern)**: This is a special case of #8 (tool execution is invisible) — once #8's typed `tool_call`/`tool_result` events exist, web tools fall out for free. Listed separately because they have *additional* requirements: URL/query visibility for trust, byte-count for token attribution, error states (404, blocked, captcha) that other tools don't have.

**TODO**:
- [ ] depends on #8 typed action events
  - [ ] `tool_call{name: "web_search"}` includes the query in the args field
  - [ ] `tool_call{name: "web_fetch"}` includes the URL
  - [ ] `tool_result` for both includes byte-count + truncated flag
- [ ] Renderer shape
  - [ ] `· searched: "recent claude opus changes" (3 results, 4.2KB)`
  - [ ] `· fetched: https://example.com/api (12.1KB, 3.8k tokens)`
  - [ ] `· fetch failed: 403 Forbidden — not retried (rate limited?)`
- [ ] privacy / consent
  - [ ] coordinate with #2 (permission_ask) — fetches under ASK_DESTRUCTIVE_ONLY autonomy could ask first, especially for non-public URLs
- [ ] cap & truncate
  - [ ] surface in #6 (`/tokens`) breakdown so the user sees web_fetch contributions to context

---

## 28. latency_ms is plumbed end-to-end and never displayed

**Reference**: codex/aider both surface per-turn latency informally (some via `tracing` spans, some inline like `(2.3s)`). claude-code shows time-to-first-token. The point: latency is a leading indicator of provider/network/budget problems — when it spikes 5x, the user wants to know before iter 3 turns into a 90-second pause.

**gil current state**: gil already has the field on the wire. `proto/gil/v1/event.proto:35-39`:
```proto
message EventMetrics {
  int64 tokens = 1;
  double cost_usd = 2;
  int64 latency_ms = 3;
}
```
Server populates it. `cli/internal/chat/repl/grpc_client.go:379,386` reads from `Metrics` but only pulls `CostUsd` (cycle 6 entry 6 noted `Tokens` is dropped too). **`LatencyMs` is dropped at the same site.** `grep -E "LatencyMs|latency_ms" cli/internal/chat` → 0 matches.

**Gap (low-medium — same wiring shape as #6, sub-line of the same fix)**: This is essentially a sub-bullet of #6 (token surface). Listing separately so it isn't lost when #6 is implemented and someone reads only that entry's TODO. The proto field is right there and gets thrown away.

**TODO**:
- [ ] add latency to the same adapter pass as #6
  - [ ] `TrackerInput.LatencyMs int64`
  - [ ] adapter pulls `Metrics.LatencyMs` alongside `Metrics.Tokens` and `Metrics.CostUsd`
- [ ] Renderer surface (incremental on top of #6's strip format)
  - [ ] last-iter latency in dim suffix: `[run · iter 7/100 · 2.3k toks · $0.40 · 1.8s]`
  - [ ] OR: only show when anomalous (>2x median of previous 5 iters), reduces noise
- [ ] system note when latency degrades sharply
  - [ ] `· latency spike: 8.2s vs 1.5s avg — provider may be rate-limiting`
- [ ] coordinate with #5 (retry visibility) — sustained high latency without retry events suggests a different cause (provider slow vs retrying); make sure the user can tell

---

## 29. /sessions output is opaque — hex-prefix + name + phase, no filter, no detail

**Reference**: aider keeps full chat history in a single markdown file the user can grep externally — implicit "filter" via shell tools. opencode/openhands maintain rich session metadata (cycle 9 entry 9: name, working_dir, recipe, type, tokens). The user-visible expectation: "show me my open sessions in this repo, sort by recency, hide the scheduled background ones".

**gil current state**: chat `/sessions` at `loop.go:192-198` renders one line per session:
```go
fmt.Sprintf("%d. %s  %s  [%s]", i+1, short, s.Name, s.Phase)
```
i.e. `1. 01abcd  my-session  [INTERVIEWING]`. No flags, no filter, no working-dir tag, no last-active timestamp, no token/cost. With more than ~10 sessions the list becomes a wall of hex prefixes. (Compounded by #9: `s.Name` is empty until that work lands, so today the column is just blank for older sessions.)

`/sessions` calls `c.ListSessions(ctx)` which maps to `SessionService.List(ListRequest)`. ListRequest has `limit` and `status_filter` only (cycle 9 entry 9 noted) — no working-dir filter, no name search, no recency sort.

**Gap (medium — discoverability)**: For a single repo this is fine. For a user juggling sessions across 5 repos it's painful — they have to remember 5 hex prefixes, or `/quit` and `gil sessions list --filter ...` from verb mode (which also doesn't have a filter today).

**TODO**:
- [ ] enrich the chat slash output
  - [ ] columns: `#`, `id-short`, `name` (from #9), `phase`, `working-dir basename`, `last-active relative` (`2m ago` / `3h ago`), `cost so far`
  - [ ] keep one-line so it stays scan-able; truncate basename to 16 chars
  - [ ] sort by last-active descending
- [ ] argument parsing on `/sessions [filter]`
  - [ ] `/sessions running` — phase filter
  - [ ] `/sessions @cwd` — only sessions whose `working_dir` matches current cwd
  - [ ] `/sessions <substr>` — name + working-dir substring match (client-side filter, no proto change needed for v1)
- [ ] (later) extend ListRequest proto with `working_dir_filter`, `name_substring`, `since_timestamp` so server-side filtering scales to 1000+ sessions
- [ ] coordinate
  - [ ] #9 (session naming) — names appear here once Update RPC lands
  - [ ] #16 (`gil configure`) — same ListRequest filters power any TUI session picker

---

## 30. drainInterviewStream silently drops every gRPC stream error (DOGFOOD-CONFIRMED bug)

**Reproducer (literally just ran)**: `gil` config has `provider = "anthropic"` (default) but auth.json has only a vllm credential. Type a prompt:
```
$ printf "hello\n/quit\n" | /tmp/gil chat
[gil]
[idle · type a prompt to start, or /sessions to resume]
>
[idle · type a prompt to start, or /sessions to resume]
>
```
The prompt is consumed → silence → idle. No "send failed", no "no credentials", no system note, nothing. The user has zero signal anything went wrong.

**Root cause**: `cli/internal/chat/repl/grpc_client.go:160-173`:
```go
func (g *GRPCClient) drainInterviewStream(stream ..., chunkCh chan<- string, done chan struct{}, sessionID string) {
    defer close(done)
    for {
        ev, err := stream.Recv()
        if err != nil {
            // EOF or transport error — turn is over.
            return
        }
        ...
```
Server returns the gRPC error `codes.FailedPrecondition desc = "no credentials for anthropic"` on the first `Recv()`. This goroutine catches it, comments away the choice, and `return`s. `defer close(done)` fires, which means `NextAssistantChunk` returns `("", false, nil)` (note: *nil error*, not the original `err`). loop.go:113-126 sees `more=false` immediately, prints `\n`, loops back to idle. The error is **lost three layers deep** — first when goroutine returns without forwarding it, then when chunkDone closes, then when NextAssistantChunk doesn't synthesize a wrapper.

**Same shape behind**: long-line dogfood (entered 10KB line — same silent loop), and likely behind any provider/model/network/server-panic failure mid-interview.

**Gap (high — single most-broken UX in current chat)**: This isn't an architectural gap; it's a real swallowed-exception bug. Every "what model are you" type prompt the user typed in the previous conversation went through this hole — the user saw "agent did nothing" and we logged it as "feels like an LLM endpoint". Half the symptom was that chat couldn't even *report* it failed.

**TODO**:
- [ ] propagate stream errors out of `drainInterviewStream`
  - [ ] new field `streamErr error` on GRPCClient (mutex-guarded)
  - [ ] goroutine sets it before `defer close(done)` fires
  - [ ] `NextAssistantChunk` reads it after chunkDone closes; returns `("", false, streamErr)` on error path
- [ ] loop.go:113-126 handles err properly
  - [ ] on err, print system note via `wrapRPCError(err)` — coordinate with #18
  - [ ] reset `inInterview` flag on stream error so next prompt creates a fresh stream
- [ ] regression test
  - [ ] fake gRPC stream that returns FailedPrecondition on first Recv
  - [ ] assert chat surface emits a system note containing the wrapped hint
- [ ] same fix probably needed in any other `Recv() ... return` site
  - [ ] grep `cli/internal/chat/repl/*.go` for `stream.Recv` patterns

---

## 31. shortID(id)[:6] collides for ULIDs created within ~30s (DOGFOOD-CONFIRMED bug)

**Reproducer (literally just ran)**: created 2 sessions ~1 minute apart. `gil session list --output json` returned:
```
"01KQEP2BZEP2XKGZZ0W2ZVX9ZA"
"01KQEP0FXVGA3VG1E5AYKZQRJX"
```
Then `/sessions` inside chat printed:
```
[system] 1. 01KQEP  01KQEP  [INTERVIEWING]
[system] 2. 01KQEP  01KQEP  [INTERVIEWING]
```
Two indistinguishable rows. `/switch 01kqep` would be ambiguous.

**Root cause**: ULID structure is 10 chars timestamp (~1 ms granularity, base32) + 16 chars random. Two ULIDs created within roughly 30 seconds of each other share their first 6+ characters. `cli/internal/chat/repl/grpc_client.go:393-398`:
```go
func shortID(id string) string {
    if len(id) >= 6 {
        return id[:6]
    }
    return id
}
```
Same hardcoded 6 in `loop.go:194` and `cli/internal/cmd/summary.go:399`. Plus a second-order bug at grpc_client.go:107:
```go
out = append(out, SessionSummary{
    ID:    s.ID,
    Name:  shortID(s.ID),     // <- fake-name workaround for #9
    Phase: s.Status,
})
```
`SessionSummary.Name` is set to the shortID since proto Session has no `name` field (#9). When prefixes collide, the chat formatter at loop.go:198 prints the short ID *twice* in a row, doubling the confusion.

**Gap (high)**: Confirmed by dogfood, not theoretical. A user who creates more than one session per minute (which is normal during interview iteration) loses the ability to distinguish them in the chat list.

**TODO**:
- [ ] adapt the truncation length to ULID realities
  - [ ] minimum 8 chars, ideally 10 (covers timestamp portion fully)
  - [ ] OR: git-style adaptive — show enough chars to uniquely disambiguate the current list (compute prefix length per render)
- [ ] consolidate the three `shortID` definitions into one place
  - [ ] currently at `cli/internal/cmd/summary.go:399`, `cli/internal/chat/repl/grpc_client.go:393`, plus inline at `loop.go:194-195`
  - [ ] put in `core/cliutil` or similar
- [ ] depends on #9 — once Session.Name field exists, drop the `Name: shortID(s.ID)` workaround at grpc_client.go:107 and use the real name
- [ ] regression test for both behaviors
  - [ ] two ULIDs sharing 6-char prefix → must render distinguishably
  - [ ] real names override short ID display

---

## 32. `gil chat` accepts --provider, --model, --working-dir flags that do nothing (DOGFOOD-CONFIRMED)

**Reproducer**: `gil chat --help` lists `--provider`, `--model`, `--socket` flags. `--working-dir` is not in the help. Reality:
- `--provider <X>` and `--model <X>` are bound to local vars in `cli/internal/cmd/chat.go:39,53,57-58` and **never used inside `runChat`**. The body's deepest reference to them is a comment at line 67: *"providerName and model parameters are accepted from the cobra command but currently unused — repl.Run drives prompts through the daemon's configured provider."*
- `--working-dir` is read at chat.go:102 (`cmd.Flags().GetString("working-dir")`) but **never registered with `c.Flags().StringVar(...)`**. So `GetString` returns `""` for every invocation, falls through to `os.Getwd()`. The flag effectively doesn't exist; user can't override.
- `--provider` help string says: *"LLM provider for intent classification + interview (anthropic|openai|openrouter|vllm|mock)"* — the intent classifier was removed in P26 T12. Stale copy.

**Gap (medium-high — silent flag = trust killer)**: User types `gil chat --provider openai` to A/B against another provider, gets back the daemon-configured anthropic flow, has no signal it was ignored. Same for `--model`. This is the most insidious kind of bug: the program proceeds successfully with the wrong settings.

**TODO**:
- [ ] decide each flag's fate
  - [ ] `--provider`, `--model`: either wire them through to override the daemon's per-session provider config, or remove them with a deprecation message
  - [ ] `--working-dir`: register with `c.Flags().StringVar` so `--help` shows it; or remove the dead read at chat.go:102 if working-dir-override isn't a feature we want
- [ ] update help copy
  - [ ] drop "intent classification" from `--provider` help (intent classifier removed in T12)
  - [ ] reflect the actual semantics
- [ ] add a test that asserts behavior matches the help
  - [ ] e.g. `gil chat --provider openai` + assert grpc client received `provider=openai` config OR test that command rejects unknown-flag if removed
- [ ] coordinate with #14 (model switching mid-session) — startup-time `--provider`/`--model` flags should land in the same `UpdateModelConfig` plumbing once that exists

---

## 33. `--ascii` flag does not strip `·` middle-dots from status-strip (DOGFOOD-CONFIRMED)

**Reproducer**:
```
$ printf "/help\n/quit\n" | /tmp/gil chat --ascii 2>&1 | cat -A
[idle M-BM-7 type a prompt to start, or /sessions to resume]$
```
`M-BM-7` is `cat -A`'s representation of the UTF-8 byte sequence `0xC2 0xB7` = U+00B7 MIDDLE DOT. With `--ascii` engaged, this glyph should fall back to `-` or `|` or ` ` per spec. It doesn't.

**Root cause**: `cli/internal/chat/render/stdout.go` lines 52, 56, 58, 60, 70, 76, 78, 94 all hard-code `·` literals in the strip body string:
```go
body = "idle · type a prompt to start, or /sessions to resume"
body = "interview · ready to freeze · /run to start, prompt to keep iterating"
body = fmt.Sprintf("run · iter %d/%d · $%.2f · %s", ...)
// etc.
```
The Glyphs struct (cycle 0/3 covers ✓ → OK and ✗ → FAIL) doesn't include `·`. The `r.ascii` flag controls only the done-strip glyphs (`OK`/`FAIL` swap) but every other strip variant ignores it.

**Gap (medium — flag promised, not delivered)**: User on a strictly-ASCII terminal (Windows cmd.exe legacy, some CI log capture, certain SSH chains) sees `·` rendered as `\xC2\xB7` two-byte garbage or replacement glyphs. The whole point of `--ascii` is to support those terminals.

**TODO**:
- [ ] extend Glyphs to include `Sep` (or `MiddleDot`) field — `·` for unicode mode, `|` (or `-`) for ascii
- [ ] update stdout.go strip builders
  - [ ] replace literal `·` with `g.Sep`
  - [ ] also covers stdout.go:52,56,58,60,70,76,78,94 + same pattern in `formatInterviewStrip` and `formatDoneStrip`
- [ ] regression test
  - [ ] ascii: true → strip body has no UTF-8 multi-byte sequences (assert via `len(s) == utf8.RuneCountInString(s)`)
- [ ] (lower priority) audit other unicode glyphs in the codebase — `▱`, `○`, `▏` etc. visible in `gil session list` output are also unicode and may need ASCII fallback

---

## 34. Banner is `gil <DisplayName>` only — no working-dir, model, autonomy, session count

**Reproducer**:
```
$ /tmp/gil chat
[gil]
[idle · type a prompt to start, or /sessions to resume]
>
```
Two lines and a prompt cue. That's the entire pre-prompt context.

**Reference**: claude-code, codex, opencode all show 4-7 lines on entry: working directory, model + provider, autonomy mode, available slashes, session count, sometimes git branch. The banner is the user's "where am I?" anchor.

**Source**: `cli/internal/chat/render/stdout.go:36-38`:
```go
func (r *StdoutChatRenderer) Banner(s SessionState) {
    fmt.Fprintf(r.out, "%s %s\n", r.p.Primary("gil"), r.p.Dim(s.DisplayName))
}
```
SessionState has 9+ relevant fields (Iter, MaxIter, CostUSD, Autonomy, ChecksPassed, ChecksTotal, etc.) but Banner uses only DisplayName, which on first launch is empty.

**Gap (medium)**: User who maintains 5+ sessions across 3 repos cannot tell which working dir / which provider / what budget they're entering on. Encourages /quit-and-recheck-via-verb-mode behavior, undermining the chat-only-surface goal of P26.

**TODO**:
- [ ] enrich the banner with current context (only the bits known pre-daemon-fetch)
  - [ ] working dir basename (full path on second line, dim)
  - [ ] daemon socket if non-default
  - [ ] config-default provider + model
  - [ ] config-default autonomy
- [ ] post-banner one-shot system note after dialing daemon
  - [ ] `· N sessions in this dir, M total — /sessions to inspect`
  - [ ] only if N > 0; suppress for fresh installs
- [ ] AGENTS.md hint (depends on #22)
  - [ ] `· loaded 2 instructions files (project, global) — /instructions`
- [ ] coordinate
  - [ ] #29 (`/sessions` enrichment) — reuse same metadata + filtering
  - [ ] #14 (model switching) — banner reflects current ModelConfig if it diverges from defaults

---

## 35. Interview service ignores `[defaults] provider` from config.toml; run service honors it (DOGFOOD-CONFIRMED asymmetry)

**Reproducer (literally just ran)**:
1. `auth.json` configured with vllm (qwen3.6-27b) — only provider with creds
2. Edit `~/.config/gil/config.toml` → `provider = "vllm"`, `model = "qwen3.6-27b"`
3. `gil chat`, type "hello, write a one-line python hello world"
4. **Silent fail**, idle prompt returns
5. Repeat in verb mode: `gil interview <id>` with the same input
6. Verb mode prints: `Error: no credentials for anthropic` / `Hint: set ANTHROPIC_API_KEY, or run "gil auth login anthropic"`

The hint comes from `wrapRPCError` (#18) translating server error. The server is *still requesting anthropic* even though config.toml says vllm.

**Root cause (smoking gun)**: two compounding bugs.

(a) **Chat ignores `--provider`** (already entry #32) → passes `""` to daemon at `cli/internal/chat/repl/grpc_client.go:136`:
```go
stream, err := g.sdk.StartInterview(ctx, sessID, prompt, "", "", sdk.InterviewModels{})
```

(b) **Interview service has no config-defaults pass.** `server/internal/service/interview.go:109`:
```go
prov, defaultModel, err := s.providerFactory(req.Provider)
```
`req.Provider == ""`, so `providerFactory("")` falls through to `server/cmd/gild/main.go:451`:
```go
case "anthropic", "":
    // ... build anthropic provider
```
The empty string maps to anthropic. **Hardcoded** at the factory layer, before any config.toml is consulted.

(c) Compare to **run service** (`server/internal/service/run.go:296`) which DOES call:
```go
spec = workspace.ApplyDefaults(spec, wsCfg)
```
…before `s.providerFactory(req.Provider)`. Run picks up `[defaults] provider` from config.toml; interview does not.

So the same daemon, same session, behaves differently:
- During interview: hardcoded anthropic
- During run: respects vllm
A user can complete an interview only by passing `--provider vllm` explicitly to `gil interview` — which means the chat path can NEVER reach vllm because chat doesn't expose that override (#32).

**Plus #30 (silent stream errors)** — even when (a)+(b) produce the wrong provider, the error never surfaces because drainInterviewStream swallows it. From the user's perspective: change config.toml, restart shell, type prompt, watch nothing happen.

**Gap (high — first-run blocker for non-anthropic users)**: Anyone who configured a non-anthropic provider and runs `gil chat` hits silence on the first prompt. The default-config bug (#unmaked) writes `provider = "anthropic"` even when auth.json clearly knows the user only has vllm. Even fixing the config doesn't help because the interview ignores it.

**TODO**:
- [ ] **interview service must apply workspace defaults** before resolving provider
  - [ ] mirror run.go:283-296 in interview.go around line 109
  - [ ] need to know the workspace dir; right now interview just gets `req.SessionId` and looks up `sess` — `sess.WorkingDir` is the right anchor
- [ ] split this from the chat-side fix
  - [ ] daemon-side: interview reads config defaults (this entry)
  - [ ] chat-side: chat REPL surfaces stream errors (#30)
  - [ ] chat-side: chat respects --provider/--model flags (#32)
  - [ ] init-side: `gil init` writes a default that matches the user's actual creds (next bullet)
- [ ] **`gil init` should not hard-code `provider = "anthropic"`** when `auth.json` has only one non-anthropic provider configured
  - [ ] inspect existing creds, pick the most-recent or only-one provider as the default
  - [ ] currently `gil init` even prints `Auth: already configured (1 provider)` — it can see which one — but writes anthropic anyway
- [ ] regression test
  - [ ] config.toml provider=vllm, no anthropic creds → `gil chat` typing a prompt → daemon spawns vllm engine, NOT anthropic
  - [ ] same scenario with `gil interview <id>` (verb mode) — already works thanks to wrapRPCError, but assert it stays working
- [ ] coordinate
  - [ ] #14 (model switching mid-session) — once `UpdateModelConfig` RPC exists, the chat surface should pre-populate it from config.toml so users can switch live
  - [ ] #16 (`gil configure` umbrella) — should be the canonical way to update the default provider; right now editing config.toml by hand is the only path

---

## 36. Interview sensing engine breaks on models that emit reasoning/thinking preambles (DOGFOOD-CONFIRMED — actual parse failure)

**Reproducer (literally just ran)**:
```
$ echo "hello, write a one-line python hello world" | \
    /tmp/gil interview 01KQEPJF4W8KHN5484YED8SCA8 --provider vllm --model qwen3.6-27b
First message: Error: recv event: rpc error: code = Internal desc =
sensing: interview.RunSensing parse "Thinking Process:\n\n1. **Analyze
the Request:** ... ": invalid character 'T' looking for beginning of value
```

**Root cause**: The qwen3.6-27b model (vllm-served) emits its chain-of-thought as plain prose — `"Thinking Process:\n\n1. ..."` — *before* the JSON object the interview's sensing stage requires. The sensing stage's JSON parser reads from the start of the response, hits `T`, fails. The whole interview never starts.

This is the **concrete manifestation of #4 (no reasoning vs answer split)**: previously listed as a visual / CoT-leakage gap. In practice it's a hard parse failure. Any model trained with a "thinking preamble" convention (DeepSeek-R1, recent Qwen, OpenAI o1-style) will break the sensing engine.

The interview's `provider.Generate` returns the full response and the sensing stage at `core/interview/...` does direct `json.Unmarshal` on it (the error message says `interview.RunSensing parse`). No detection of `<thinking>...</thinking>` blocks, no JSON extraction fallback (e.g. find the first `{...}` substring), no provider-aware response cleaning.

**Gap (high — entire model families locked out)**:
- DeepSeek-R1, Qwen3 thinking variants, Anthropic extended-thinking-on, OpenAI o1/o3 → all unusable for interview today
- Verb mode at least surfaces the error; chat (#30) silently swallows it, so users hit "agent does nothing" with no diagnostic

**TODO**:
- [ ] response cleaning before JSON parse
  - [ ] strip `<thinking>...</thinking>` blocks (Anthropic-style)
  - [ ] strip `Thinking Process:\n...\n\n` preambles (Qwen-style)
  - [ ] regex fallback: find the first `{` and parse from there
  - [ ] last resort: ask the model to "respond with strict JSON, no preamble" via a system-prompt suffix
- [ ] provider-aware adapters
  - [ ] anthropic provider already returns thinking blocks separately when extended-thinking is on — strip those before passing to interview
  - [ ] openai reasoning models — strip the reasoning-summary blocks
  - [ ] vllm/openrouter generic — heuristic strip
- [ ] better error wrapping
  - [ ] sensing-parse-failed should yield a typed error with the offending bytes (truncated) + a hint like "model emitted non-JSON; try `--model <known-good>` or check provider settings"
- [ ] coordinate with #4 (reasoning split) — once typed AssistantOutput separates `reasoning` from `message`, interview stages consume `message` only and this bug evaporates
- [ ] coordinate with #30 — until chat surfaces stream errors, this bug is invisible to chat users

---

## 37. `gil status <id>` accepts an argument that it silently ignores

**Reproducer**:
```
$ gil status 01KQEPJF4W8KHN5484YED8SCA8
   ○  01kqep   started 2m ago
   ○  01kqep   started 11m ago
   ○  01kqep   started 12m ago
   ...
$ gil status --help
List sessions
Usage:
  gil status [flags]
```
The CLI accepts `gil status <some-id>` and prints a list of all sessions. The argument is silently dropped by cobra (Args validation isn't restrictive). User who types `gil status 01abc` to ask "what's the status of session 01abc" gets the entire session list instead, and may not even notice their session is in there.

**Naming clash compounds it**: chat's `/status` slash returns a 1-line summary of the active session (`grpc_client.go:303-310` `Status() returns "ID · Status · iter · cost"`). Verb `gil status` is a list. **Same name, different semantics.**

**Gap (low-medium — naming + ergonomics)**: Users learn "/status" inside chat and assume `gil status` does the same. They don't.

**TODO**:
- [ ] decide naming
  - [ ] option A: rename `gil status` → `gil sessions` (alias-only would also work) since the body is "list sessions"
  - [ ] option B: make `gil status [<id>]` actually do per-session detail when an id is given, list when not
  - [ ] option B is what users will expect — same shape as `git status`, `kubectl status`
- [ ] reject unknown positional args by default
  - [ ] `cobra.NoArgs` or `cobra.MaximumNArgs(1)` instead of accepting whatever
- [ ] coordinate with #29 (enriched `/sessions` output) — both verb and slash should share the formatter

---

## 38. ⚠️ ROOT-CAUSE: `gil init` writes config.toml with `[defaults]` section, but Config struct expects top-level fields → every user's config silently ignored (DOGFOOD-CONFIRMED)

**This is the single most damaging pre-existing bug found this round.** Affects everyone who has run `gil init` since the schema diverged.

**Reproducer (literally just ran)**:
1. `~/.config/gil/config.toml` (written by `gil init`):
   ```toml
   [defaults]
   provider = "mock"
   model = ""
   workspace_backend = "LOCAL_NATIVE"
   autonomy = "ASK_DESTRUCTIVE_ONLY"
   ```
2. Test program calling `workspace.Resolve(globalPath, "")`:
   ```
   Resolve() = Provider="" Model="" err=<nil>
   ```
3. Same file with `[defaults]` header REMOVED (fields at top level):
   ```
   Resolve() = Provider="mock" Model="" err=<nil>
   ```

**Root cause**:
- `cli/internal/cmd/init.go:29-37` writes the toml stub with a `[defaults]` section header.
- `core/workspace/config.go:25-51` defines `Config` with fields tagged `toml:"provider"`, `toml:"model"`, etc. — at the **top level**.

`go-toml` (or whatever toml decoder) parses the file: `[defaults]` table becomes a sub-map, top-level Provider/Model stay zero. Resolve returns the empty Config, then ApplyDefaults at run.go:296 has nothing to apply.

So:
- `[defaults] provider = "anthropic"` (init's hardcoded default) → ignored
- User edits to `provider = "vllm"` → ignored
- User edits to `provider = "mock"` → ignored

**Every user gets the providerFactory's hardcoded fallback** (`case "anthropic", "":`) regardless of their config.toml.

This is **why entry #35 patch (apply config in interview service) appeared to do nothing initially** — the config layer it reads from was empty for unrelated reasons. After removing `[defaults]` from the file by hand, my interview.go patch worked correctly and chat reached the qwen / mock provider.

**Gap (highest severity — config has been silently broken since the schema diverged)**:
- Run service: appears to "work" only because both the config and the spec default to anthropic; if a user diverged the spec, run still ignored their config
- Interview service: was never reading config (fixed by my #35 patch, but still useless until #38 lands)
- All `[defaults] workspace_backend` and `[defaults] autonomy` settings are likewise no-ops

**TODO**:
- [ ] decide which side to align (one of):
  - [ ] **option A** (smaller change): drop `[defaults]` section header in `init.go:32` so the stub matches the loader
  - [ ] **option B** (better DX): change `Config` struct to embed `Defaults Config` and the toml tag on the outer wrapper to `[defaults]` — makes config more readable for non-default sections (`[notify]`, `[workspace]`, etc. already use sections)
  - [ ] given that `mcp.toml` and the run config also want sectioned layout, **option B is the more future-proof fix**
- [ ] migration: existing config.toml files in the wild
  - [ ] one-shot `gil doctor --fix-config` that detects the format and rewrites
  - [ ] OR backward-compat: loader tries top-level first, falls back to `[defaults]`
- [ ] regression test
  - [ ] `Resolve(<file with [defaults] section>)` → returns populated Config
  - [ ] `Resolve(<top-level file>)` → also returns populated (whichever side we pick)
- [ ] coordinate with #35 — once #38 lands, my interview.go patch becomes useful
- [ ] coordinate with init.go — its `Auth: already configured (1 provider)` print proves init *can* see the user's actual creds; the hardcoded `provider = "anthropic"` in the stub should pick whatever the user has, not stamp anthropic blindly

---

## 39. Chat drops Stage events for sensing→conversation; no "interview started" feedback (DOGFOOD-CONFIRMED)

**Reproducer (literally just observed after fixing #38)**: typed "build a hello world python script" in chat. Mock daemon ran sensing, sent `Stage{From: "sensing", To: "conversation"}` then `AgentTurn{"What's your project goal?"}`. Chat displayed only the question. No system note like `· interview started (domain=unknown, confidence=0.50)`.

**Root cause**: `cli/internal/chat/repl/grpc_client.go:198-204`:
```go
case *gilv1.InterviewEvent_Stage:
    if v.Stage != nil && v.Stage.To == "ready_to_freeze" {
        g.eventCh <- TrackerInput{
            Kind:      "interview.ready_to_freeze",
            SessionID: sessionID,
        }
    }
```
The adapter only forwards Stage events whose `To == "ready_to_freeze"`. Every other transition (sensing→conversation, conversation→adversary, conversation→audit, etc.) is silently dropped. The user gets no indication the interview engine is making progress until it reaches the very end.

This is why the chat strip stayed at `[idle ...]` after the first prompt instead of switching to `[interview · 0/N slots · sat 0%]`.

**Gap (medium-high)**: Interviews can have 5-10 stage transitions before ready_to_freeze. The user sees zero of them. Combined with #19 (stuck detector silent) and #17 (compaction silent), the chat surface only ever speaks at the END of phases, never DURING. This is the rendering corollary of "feels like an LLM endpoint".

**TODO**:
- [ ] forward all Stage transitions, not just ready_to_freeze
  - [ ] new TrackerInput Kind: `interview.stage_transition` with `From`, `To`, `Reason` fields
  - [ ] Renderer prints a system note: `· interview: sensing → conversation (domain=unknown, confidence=0.50)`
- [ ] coordinate with #29 (enriched /sessions output) — same Phase string should match between strip and stage events
- [ ] regression test — assert each emitted Stage event produces a visible system note

---

## 40. Strip + prompt cue redraw timing puts assistant text after the next strip line (DOGFOOD-CONFIRMED visual artifact)

**Reproducer**:
```
> What's your project goal?
[idle · type a prompt to start, or /sessions to resume]
>
```
The model's response (`What's your project goal?`) appears AFTER the next strip redraw, not under the prompt that the user just sent. Reads "out of order" — visually it looks like the strip happened first, then the response, then another strip.

**Root cause**: `cli/internal/chat/repl/loop.go:62-65,113-127`:
```go
for {
    drainEvents(ctx, cfg, tr)
    cfg.Renderer.StatusStrip(tr.State())  // line 64
    cfg.Renderer.PromptCue()               // line 65 (writes "> ")
    
    if !scanner.Scan() { ... }
    line := scanner.Text()
    
    // ... after handling slash/blank ...
    case InputPrompt:
        cfg.Client.SendPrompt(...)
        for {
            chunk, more, err := cfg.Client.NextAssistantChunk(ctx)
            if chunk != "" { cfg.Renderer.AssistantText(chunk) }
            ...
        }
        cfg.Renderer.AssistantText("\n")
    }
    // ← back to top of for-loop, redraws strip + cue BEFORE next assistant chunk arrives
}
```

The for-loop's top draws strip+cue BEFORE Scan(). After a prompt, AssistantText fires AND THEN we loop, which redraws strip+cue. So the order on screen is:
1. `[idle...]` strip + `> ` cue (drawn at loop start)
2. user types prompt (displayed inline by terminal, not by code)
3. AssistantText: `What's your project goal?` (drawn between the two strips)
4. `[idle...]` strip + `> ` cue again (loop top)

In a real terminal with cursor positioning the strip might be in-place updated, but stdout-pipe and most simple terminals show every line in order, so the response gets sandwiched.

**Gap (medium — purely visual but read-blocking)**: The chat reads less like a conversation and more like a log dump. Especially confusing for the AGENT-FIRST event: user types prompt, sees strip, sees their own scroll-back, sees response after another strip. Compare to claude-code/codex/aider which all clear+repaint in-place.

**TODO**:
- [ ] decide redraw model
  - [ ] option A (cleaner): only draw strip when phase actually changes, not every loop iteration
  - [ ] option B (current model preserved): use cursor-up + clear-line ANSI to redraw in place; falls back to plain print under NO_COLOR / dumb terminal
  - [ ] option C: TUI mode with bubbletea/lipgloss (longer-term, separate refactor)
- [ ] suppress strip while assistant is mid-stream (don't repaint until turn ends)
- [ ] coordinate
  - [ ] #21 (markdown rendering) — turn-end re-render is the natural point to also flush markdown styling
  - [ ] #4 (reasoning split) — reasoning chunks wouldn't trigger a strip repaint either

---

## 41. Mock provider's NewMock has only 2 fixed responses; reply turn exhausts it (DOGFOOD-CONFIRMED)

**Reproducer**:
```
> build a hello world python script
What's your project goal?
> small CLI
[silent — actual error: "RunReplyTurn slotfill: slotfill provider: mock provider responses exhausted"]
```

**Root cause**: `server/cmd/gild/main.go:444-449`:
```go
default:
    // Text-only Mock for InterviewService scenarios
    return provider.NewMock([]string{
        `{"domain":"unknown","domain_confidence":0.5,"tech_hints":[],"scale_hint":"unknown","ambiguity":"none"}`,
        "What's your project goal?",
    }), "mock-model", nil
```
2 hardcoded responses: sensing JSON + first question. Reply turn (slotfill, audit, conversation) needs at least 4-6 more responses. The third call hits "responses exhausted".

This is fine for unit tests (mock is supposed to be deterministic) but means **mock provider cannot dogfood the multi-turn interview cycle end-to-end**. Real LLM (anthropic, openai) is required to walk through interview → freeze.

**Gap (medium — dev-loop friction, not user-facing)**: Every dogfood cycle needs a real LLM, costs real money. CI tests can't reach interview-end behaviour with the current mock.

**TODO**:
- [ ] enrich the text-only mock with a longer canned script
  - [ ] enough responses for one full interview cycle: sensing JSON, conversation question, slot-fill JSON × N slots, audit JSON, ready_to_freeze stage emit
  - [ ] make the script easy to edit / introspect
- [ ] better: scriptable mock via env var
  - [ ] `GIL_MOCK_INTERVIEW_SCRIPT=path/to/file.json` — load responses from file
  - [ ] enables CI tests for the full cycle
- [ ] error path: when mock is exhausted, return a typed error with hint "extend GIL_MOCK_INTERVIEW_SCRIPT"
  - [ ] right now it bubbles up as generic "provider responses exhausted" → wrapped via #18 helps but not specific enough

---

## 42. Bare `gil` branches on TTY (runChat) vs non-TTY (runSummary) — two surfaces, not one (§2.6 violation)

**Reproducer**: `cli/internal/cmd/root.go:124-128`:
```go
RunE: func(cmd *cobra.Command, _ []string) error {
    if !noChat && stdoutIsTTY() {
        return runChat(cmd, defaultSocket(), "", "")
    }
    return runSummary(cmd.OutOrStdout(), defaultSocket(), defaultBase(), asciiMode)
},
```

**Root cause**: `gil` has two essentially different entry surfaces — interactive REPL on a TTY, one-shot mission-control summary on a pipe. design.md §2.6 mandates a single chat surface regardless of TTY-ness. The `--no-chat` flag is itself an escape hatch elevated to a mode switch; per §2.6 mode-switch flags as defaults are forbidden.

**Gap (high — primary surface principle)**: User cannot rely on a single mental model of "what gil shows me when I type its name." Scripts that pipe `gil` see one thing; humans see another; flag toggles change semantics. Violates §2.6 "단일 채팅 surface… mode 분기 금지."

**TODO**:
- [ ] unify entry through chat surface even on non-TTY
  - [ ] non-TTY consumes stdin as one-shot prompt or EOF, renders the same disclosure + status strip via plain (non-ANSI) renderer
  - [ ] `runSummary`'s output (session list, next-action hints) becomes part of the chat self-disclosure on entry (already partly done — Step (a))
- [ ] demote `--no-chat` flag
  - [ ] hidden / deprecated in `--help`
  - [ ] eventually removed once unified surface settles
- [ ] kill `stdoutIsTTY()` as a routing primitive in root.go
  - [ ] still useful inside the renderer for color/glyph decisions, not for routing
- [ ] depends on (b)/(c) intent router landing first so chat surface can handle whatever the user types without slash-table reliance

---

## 43. Static slash table is the default surface; intent classifier was deleted in P26 (§2.6 violation)

**Reproducer**: `/help` lists `/sessions /switch /new /spec /status /diff /merge /run /quit`. User who does not memorize the table cannot resume a session, view spec, or apply diff except by typing `/`. P26 explicitly removed the prior intent classifier (commit history: T12 "chat.go integration (replace intent classifier)"). design.md §2.6 mandates the opposite direction: natural language as primary, slash as hidden fallback.

**Root cause**: P26's design treated the slash table as the canonical input surface. §2.6 (added 2026-05-02) inverts that: every action must be reachable in natural language, slashes serve as deterministic escape hatch only.

**Gap (high — primary surface principle)**: Users typing "show me the spec" or "save it" or "switch to the dark-mode session" get nothing — the loop only routes `InputSlash` and `InputPrompt` to the LLM, with no natural-language → action classifier in between. This is the §2.6 violation that motivated the principle's codification.

**TODO**:
- [ ] re-introduce intent router (LLM-based, not the deleted regex one)
  - [ ] `spec.models.intent` slot — small fast model
  - [ ] classify each `InputPrompt` into either {forward to interview/run service} or {dispatch a service call} or {ambiguous → ask}
  - [ ] surface routing decision as a system note ("→ resuming session 01kqep…") so user can correct
- [ ] keep slash table as hidden fallback
  - [ ] `/help` deemphasized; not advertised on idle strip
  - [ ] still parseable for scripts and accessibility
- [ ] entry self-disclosure (Step (a), DONE) is the precondition: user must see what's available before the router can map their words to it

---

## 44. `gil chat` subcommand still exists alongside bare `gil` (§2.6 violation, related to #42)

**Reproducer**: `gil --help` lists both `gil` (bare) and `gil chat` as ways to enter the chat surface. They are nearly identical except `gil chat` accepts dead flags (#32). Two paths to the same surface duplicates the user's mental model and contradicts §2.6's "단일 채팅 surface."

**Root cause**: P26 added `gil chat` as the canonical verb before bare `gil` was wired up to chat. After bare `gil` started routing to chat (TTY path), `gil chat` became redundant. The `--no-chat` flag still depends on `chat` existing as an explicit verb the user can opt out of.

**Gap (medium)**: Power users may keep using `gil chat` for the explicit-verb form even when it's vestigial. New users see two ways to do the same thing in `--help`.

**TODO**:
- [ ] decide: keep `gil chat` as alias-only (hidden) or remove
  - [ ] alias-only: keeps muscle-memory working without polluting `--help`
  - [ ] remove: cleanest, requires deprecation cycle
- [ ] depends on #42 (bare gil unification) — once bare `gil` is the universal surface, `gil chat` has no separate role
- [ ] dead flags on `gil chat` (#32) should be removed regardless

---

## End of round 4 (44 entries total) — §2.6 surface-unity violations added

Entries 30–41 are dogfood-discovered (built `/tmp/gil` + `/tmp/gild`, ran chat, observed). Entries 42–44 are §2.6-rooted: design.md §2.6 (자연어 단일 surface, 내부 에이전트 라우팅, added 2026-05-02) was implicit before P26 and got actively violated when P26 removed the intent classifier. The structural fixes (#42, #43, #44) trace back to one principle now codified.

The most-critical finding is **#38**: every user's config.toml has been silently ignored since `gil init`'s template diverged from the loader's expectations. This is the structural cause of the user's "I configured vllm but chat keeps asking for anthropic" complaint that motivated this round.

Patches I applied during dogfood (NOT part of the gap log; these were enabling-test patches):
- `server/internal/service/interview.go` — apply workspace defaults so `req.Provider == ""` falls back to config.toml (#35 fix). **Conditionally useful** — only kicks in once #38 (config schema mismatch) is fixed.
- `~/.config/gil/config.toml` — manually removed `[defaults]` header to confirm #38 is the real bug.

The patches let chat actually reach end-to-end through mock provider for one turn. Multi-turn requires a real LLM (#41) or a richer mock script.

Rounds 1+2 (entries 1–29) were grep-and-compare against reference harnesses. **Round 3 (entries 30–34) is the first set of dogfood-discovered findings — actually built `/tmp/gil` + `/tmp/gild` and exercised the chat surface end-to-end.** Five concrete bugs (silent stream errors, ULID prefix collision, dead flags, `--ascii` incomplete, sparse banner) showed up in the first ~10 minutes of real use. This validates the round 1 thesis (Group A wiring gaps dominate "feels like LLM endpoint") and adds concrete reproducers for the most user-visible issues.

---

## Priority groupings (severity + dependency, NOT time)

### Group A — Pure wiring (pieces all exist; missing connection)
- #2 permission_ask wiring — server emits, adapter drops
- #5 retry visibility — wrapper has events, surface doesn't see them
- #6 token surface — adapter reads `CostUsd`, ignores `Tokens`
- #13 false /interrupt hint copy — at minimum stop lying, ideally add Stop RPC
- #17 compaction events not emitted (RequestCompact RPC exists, no /compact slash)
- #18 chat REPL doesn't call existing `wrapRPCError` translation table
- #19 stuck detector event name mismatch — server emits `stuck_detected`, adapter listens for `run.stuck`
- #23 subagent_started/done events ignored by chat adapter
- #24 budget_warning/budget_exceeded events ignored by chat adapter
- #28 `Metrics.LatencyMs` dropped by adapter (sub-line of #6)
- #30 drainInterviewStream swallows gRPC stream errors silently (DOGFOOD-CONFIRMED)
- #31 shortID 6-char truncation collides for ULIDs created within ~30s (DOGFOOD-CONFIRMED)
- #32 `gil chat` flags `--provider`/`--model` ignored, `--working-dir` not registered (DOGFOOD-CONFIRMED)
- #33 `--ascii` doesn't strip `·` middle-dots from status strip (DOGFOOD-CONFIRMED)
- #35 interview service ignores config.toml `[defaults] provider`, run service honors it — asymmetry causes first-run silent fail for non-anthropic users (DOGFOOD-CONFIRMED)
- #36 sensing engine breaks on models that emit "Thinking Process:" / `<thinking>` preambles — qwen3.6-27b unusable today (DOGFOOD-CONFIRMED, manifestation of #4)
- #37 `gil status <id>` silently ignores positional arg, naming clashes with chat `/status`
- #38 ⚠️ **`gil init` writes `[defaults]` section but Config struct expects top-level → all user configs silently ignored** (DOGFOOD-CONFIRMED, root cause)
- #39 chat drops Stage events except ready_to_freeze; sensing→conversation invisible (DOGFOOD-CONFIRMED)
- #40 strip+prompt-cue redraw timing places assistant text after next strip — visual flow broken (DOGFOOD-CONFIRMED)
- #41 mock provider's NewMock has 2 fixed responses, exhausted on reply turn (DOGFOOD-CONFIRMED)

### Group B — New surface/proto, single subsystem
- #1 file-context (WorkingSet slot + slashes)
- #7 @-mentions (client-side parser + LoadReference RPC)
- #9 session naming + fork (proto fields + Update/Fork RPCs + slashes)
- #10 per-hunk diff apply + per-edit approval
- #14 model switching (proto fields + UpdateModelConfig RPC + slashes)
- #15 shell-exec slashes (client-side)
- #16 `gil configure` umbrella
- #20 ListCheckpoints RPC + `/undo` `/checkpoints` `/restore` slashes
- #22 GetInstructions RPC + `/instructions` slash (discovery wired, surface absent)
- #25 chat-time `/save /export /copy` + optional `--transcript` flag
- #26 `/clear /reset` for in-session context wipe
- #29 enriched `/sessions` output + filter args
- #34 enrich banner with cwd/model/autonomy/session-count

### Group C — Cross-cutting design (touches multiple subsystems)
- #4 reasoning split (typed AssistantOutput proto + Renderer + provider adapters)
- #8 tool narration (typed action events through runner + Renderer)
- #11 CHAT_ONLY mode (new SessionMode enum + interview bypass + slash)
- #12 readline input layer (input abstraction + library wiring)
- #21 markdown rendering (Renderer turn-end + glamour/chroma integration)

### Group D — Long-horizon ecosystem
- #3 MCP wiring (finishable, but pulls in plugin hook design naturally)
- #27 web search/fetch surfacing (rolls into #8 typed action pipeline)

### Cross-cutting dependencies (rough)
- #4 + #8 likely share a typed `AssistantOutput`/event design
- #1 + #7 share WorkingSet
- #2 + #10 + #13 share permission/approval flow
- #14 share dialog code with #16
