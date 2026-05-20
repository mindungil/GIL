# Codex CLI Agent: Architectural Deep Dive
**Date**: 2026-05-20  
**Scope**: OpenAI Codex (Rust, /home/ubuntu/research/codex/codex-rs, 78K⭐)  
**Focus**: System identity framing, streaming UX, agent loop control, stuck detection, delegation, persistence.

---

## 1. System Prompt / Self-Identity Framing

**Status**: ✅ Strong explicit identity injection.

Codex embeds a **persistent identity prompt** that is pre-baked and re-injected per turn (does NOT compose dynamically per-request).

**Key files**:
- `codex-rs/core/templates/realtime/backend_prompt.md` — Static markdown template
- `codex-rs/core/src/realtime_prompt.rs:1-24` — Template loader + placeholder substitution

**Implementation**:
The prompt **opens with explicit identity** (`backend_prompt.md:1-11`):
```
## Identity, tone, and role

You are Codex, an OpenAI general-purpose agentic assistant...
Your personality is a playful collaborator: super fun, warm, witty, expressive...
```

Then **defines a unified framing** (`backend_prompt.md:13-27`):
- "Treat the system as one unified assistant. Do not mention anything about backend."
- "Pass execution work to the backend. Because user can always send requests directly, **do not block or withhold**."
- "NEVER refuse requests. Delegate all user requests to the backend."

**Re-injection strategy**:
- Codex stores this as a static template; prepares it once at session init (`realtime_prompt.rs:prepare_realtime_backend_prompt`)
- The template is re-sent on **every turn** as part of the system prompt block (not cached separately)
- User's real name is substituted via simple string replacement (`realtime_prompt.rs:22-23`)
- Identity is **NOT** negotiable per-turn; it's architectural

**Gil comparison**:
Gil's `core/runner/system_prompt.go:137-146` renders identity inline:
```go
You are an autonomous coding agent. Make the verification checks pass...
```
Much shorter, goal-driven, and does NOT repeat per-turn (composed fresh but identical). **Gap**: Gil doesn't assert "you are Gil" explicitly or establish the "unified assistant" framing. Codex's "NEVER refuse" + "delegation is mandatory" stance is missing from gil's prompt.

---

## 2. Streaming UX

**Status**: ✅ Sophisticated incremental rendering with token + tool-call + reasoning streaming.

Codex streams **three independent token streams** to the TUI in real time:

### Token Deltas
- `core/src/stream_events_utils.rs` — Buffers streamed text in newline-gated accumulator
- `tui/src/markdown_stream.rs:20-96` — `MarkdownStreamCollector` holds incomplete lines until `\n` arrives
- Only **complete newline-terminated source** is rendered, preventing partial markdown corruption
- `streaming/controller.rs:120-143` — `push_delta()` accumulates and triggers re-render on `\n`

### Reasoning Streaming (Extended Thinking)
- `core/src/session/turn.rs:702-703`, `1842-1857` — Reasoning effort + summary config passed explicitly
- Reasoning blocks are **separately streamed** and rendered (not mixed with assistant text)
- `stream_events_utils.rs` handles `ResponseItem::Reasoning` as distinct from `ResponseItem::Message`

### Tool-Call Announcement Streaming
- Tool calls are **announced as they arrive**, not batched at turn-end
- `tui/src/streaming/controller.rs` — Two-region model: **stable region** (committed to history) + **tail region** (active cell, mutable)
- Table holdback (`table_holdback.rs`) — Tables stay mutable in tail until stream finalizes (prevents column-width thrashing)

### Wire Shape
- **gRPC bidi streaming** + **ResponsesAPI WebSocket**:
  - `core/src/client.rs:1-50` — `ModelClientSession` manages WebSocket connection
  - Prewarm (v2-only): `response.create` with `generate=false` (opens socket early)
  - Subsequent requests reuse same connection, `previous_response_id` for routing
  - `X_CODEX_TURN_STATE_HEADER` sticky routing token (`client.rs:137`)
- **TUI event consumption** (`app/event_dispatch.rs`):
  - Streaming events arrive as `AgentMessageContentDeltaEvent` (text), `ReasoningContentDeltaEvent` (reasoning), `FunctionCall` (tool)
  - Each is queued separately; UI re-renders incrementally
- **No intermediate tool output injection** — tool results fed back in next request, not streamed live

### Vs Gil
Gil's `provider/provider.go:88` exports a **synchronous `Complete()` API** (no streaming):
```go
Complete(ctx context.Context, req Request) (Response, error)
```
Returns entire `Response` struct (Text, ToolCalls, all at once). **Gap**: Gil blocks on full response; Codex streams token-by-token. Gil's TUI would need a provider adapter wrapper to materialize streaming from the gRPC bidi channel (not in the provider abstraction today).

---

## 3. Agent Loop Control Flow

**Status**: ✅ Structured turn loop with budget, compact, and escalation gates.

### Turn Lifecycle
- `tui/src/chatwidget/turn_lifecycle.rs:8-67` — Tracks `agent_turn_running`, budget markers, goal-status timing
- `core/src/session/turn.rs:140-150` — `run_turn()` entry point, loops until assistant message or tool exhaustion

### Turn Loop Structure (from `core/src/session/turn.rs:122-135`):
1. Model replies with tool calls OR assistant message
2. If tool calls: execute (parallel via `ToolCallRuntime`), feed back results in next request
3. If only assistant message: record in history, **turn ends**
4. Context-window exceeded → trigger `compact` task (rewrite conversation summary)

### Stopping Conditions
- Assistant emits final-answer message (`MessagePhase::FinalAnswer`) → stop
- Tool call results in "agent gave up" marker → stop
- Budget exhausted (`IterationBudget`) → stop with `_turn_exit_reason = "budget_exhausted"` (`runner.go` analogue)
- Context-window overflow → auto-compact, then retry

### Retry & Escalation
- `core/src/guardian/prompt.rs` — **Auto-review approval flow**: if tool sandboxing escalates, LLM is asked to decide (not a hard block)
- `exec_policy.rs` — Tool execution gated by approval mode + sandbox mode (reads `ApprovedForSession` state)
- **No backoff loop for "stuck" detection** — just context exhaustion handling

### Compaction Strategy
- `core/src/compact.rs:45` — `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000`
- Triggered when `ContextWindowExceeded` (not proactive)
- `compact.rs:222` — Retry loop: `history.remove_first_item()` to free tokens
- Preserves: latest user message, system, initial context, final assistant message (head + tail)
- Prompt injected: `templates/compact/prompt.md` — LLM rewrites conversation into summary

### Vs Gil
Gil's `core/runner/runner.go` is a **sequential imperative loop** (no bidi streaming backpressure):
```go
// Simplified pseudocode
for {
  resp := provider.Complete(ctx, req)
  if no tool calls { return } // turn done
  execute tools
  append results to req.Messages
}
```
**Differences**:
- Gil's compact is **explicit** (user/CLI calls `gil compact`), not auto-triggered
- Gil's budget model is simpler: `iteration_budget` counter, no proactive compaction
- Codex escalates to LLM for approval decisions; Gil has a `permission.Evaluator` (static rules, no LLM loop-back)

---

## 4. Tool Surface Organization

**Status**: ✅ Explicit tool schema + system prompt enumeration (dual injection).

### Tool Definitions
- `codex_tools::create_tools_json_for_responses_api` — Emits JSON schema sent to provider
- Each tool has `name`, `description`, `input_schema` (JSON Schema standard)
- System prompt ALSO enumerates names for "quick reference" (redudant but intentional)

### System Prompt Injection
- **Two places**:
  1. `realtime_prompt.md` — Static narrative ("You are Codex...")
  2. Tool enumeration — Sent separately as tool definitions in the `Request` struct
  3. Optional verbose hints in system prompt for weak models (vLLM, qwen)

### Verification Gate
- `core/src/exec_policy.rs` — Tool calls are **gated by approval mode**:
  - `Untrusted` — all tools require user OK
  - `OnRequest` — read-only tools auto-approved
  - `Never` — no approval asked
- No "done without proof" enforcement at the language level (trust the LLM to emit final message)

### Vs Gil
Gil's `core/runner/system_prompt.go:179-226` — Renders tool names + format hints:
```
Tools: bash, edit, apply_patch, ...
Available tools:
- edit: SEARCH/REPLACE block edit. Format: ...
```
**Similarities**: Dual injection (schema + prompt text). **Difference**: Gil's hints are **format tutorials** (SEARCH/REPLACE syntax), not just names.

---

## 5. Stuck / Recovery Patterns

**Status**: ❌ **No explicit stuck detector**.

Codex has **no 5-pattern loop detector** (like OpenHands, Hermes). Instead:

### What Codex Does Have
1. **Context-window overflow** → auto-compact (reactive, not proactive)
2. **Tool execution error** → LLM sees error in tool result, tries again
3. **Approval escalation** → Guardian LLM reviews risk and decides to allow/block
4. **No explicit timeout per tool** — relies on request-level timeouts

### Absence of Loop Detection
- No detection for "same tool call 4× in a row"
- No "monologue 3+ turns without tool calls" check
- No "infinite tool-error cycle" breaker

### Implication
Codex **trusts the underlying model to self-correct**. If Claude or GPT-4 gets stuck, the harness doesn't intervene; it just runs down the budget or context window. This works for strong models but fails gracefully on weak ones.

---

## 6. Subagent / Delegation

**Status**: ✅ Full multi-agent support with isolation and communication layer.

### Spawn Mechanism
- `core/src/agent/control.rs:48-98` — `SpawnAgentForkMode` options:
  - `FullHistory` — child gets entire rollout
  - `LastNTurns(n)` — child gets last N turns (cheap fork)
- `Session::emit_subagent_session_started` — Parent records child spawn event
- Each child gets own `ThreadId`, inherits parent's `SessionId` (shared session tree)

### Communication Shape
- **No shared event stream** (unlike Hermes' in-process delegation):
  - Parent creates separate event sink for child (`span!`)
  - Child's events stay in child's rollout; parent sees `subagent_started` / `subagent_done` notifications
  - Parent can query child status via `AgentControl::list_agents()` (registry)
- **Depth**: Unlimited (grandchildren OK; only gated by task tool availability in children)

### Isolation
- Child inherits parent's **instructions** + **config** but has own **context**
- Tasks are delegated via `task` tool (not visible in code yet, but referenced in `plan_tool.rs`)
- Child can spawn its own children (recursive delegation)

### Vs Gil
Gil's `core/runner/runner.go:1280-1363` — Calls `Run()` recursively:
```go
// Sub-agent loop in-process; shares parent's event stream
sess.Run(ctx, subagentGoal)
```
**Differences**:
- Codex: separate registered agents, parent polls registry
- Gil: recursive call-stack, parent waits for return
- Codex: unlimited depth (gated by tools), Gil: arbitrary depth (gated by stack)

---

## 7. Persistence / Autonomy Posture

**Status**: ✅ Session resume, rollout journaling, explicit lifecycle.

### Session Persistence
- Rollout JSONL: `~/.codex/sessions/rollout-YYYY-MM-DDTHH-MM-SS-{uuid}.jsonl` (`rollout/src/lib.rs:77-78`)
- Archived: `~/.codex/archived_sessions/` (post-run compaction)
- Resume via `codex resume <session-id>` (user-initiated, not automatic)

### Rollout Journaling
- Every `ResponseItem` (tool call, output, message, reasoning) is appended to JSONL
- Non-destructive: compaction creates a new `Compacted` item, keeps originals
- Full history queryable by tools and for debugging

### "Walk Away" Semantics
- Session runs until:
  1. Assistant emits final answer → `run_done`
  2. User stops it → `run_cancelled` (sent from TUI)
  3. Budget exhausted → `run_done` (with exit reason)
- **No auto-wakeup** — parent must explicitly resume session (CLI or TUI click)

### Approval Modes
- `Untrusted` (default) — all tools ask before execution
- `OnRequest` — tools chosen in approval UI
- `Never` — everything auto-approved (yolo mode)
- **Persistent**: `ApprovedForSession` flag tracked in rollout state

### Vs Gil
Gil's `sdk/client.go:64-80` — Simple session struct:
```go
type Session struct {
  ID, Status, WorkingDir, SpecID string
  TotalTokens, BudgetMaxTokens int64
  CurrentIteration int32
}
```
Session lifecycle is **implicit in CLI**: `gil run` → blocks until done. No resume API; kill process = lose session (unless persisted). **Gap**: Gil has no journaled rollout (JSONL); only in-memory state. Codex's JSONL is the gold record; Gil would need that for true resume + post-run audits.

---

## What Gil Should Steal / Avoid

### Steal
1. **Explicit identity prompt + unified framing** (`backend_prompt.md` pattern).
   - File to touch: `core/runner/system_prompt.go:137` — expand `renderBase()` to include "You are Gil, an autonomous coding agent running inside the Gil harness. Your tools are …"
   - Inject "NEVER refuse requests; delegate to tools" (Codex's mandate).

2. **Newline-gated streaming buffering** (`MarkdownStreamCollector`).
   - File: `cmd/gil/tui.go` or new `tui/streaming.go` — adopt Codex's two-region stable/tail model.
   - Prevents partial markdown rendering during live deltas.

3. **Rollout JSONL journaling + compress-preserve invariant** (`compact.rs:222`).
   - File: `core/session/schema.go` — add JSONL append, `core/compact.go` — preserve originals (non-destructive compaction).
   - Required for resume + audits.

4. **Tool approval gating via LLM escalation** (`guardian/prompt.rs`).
   - File: `core/runner/runner.go:~900` — add `adversary_escalate()` loop (already have permission rules; add LLM ask for edge cases).

### Avoid
1. **Dual tool injection** (schema + prompt text) — causes redundancy.
   - **Decision**: Stick with single source (provider's schema); strip the verbose format-hint block for strong models (Claude defaults to `Compact: true`).

2. **Re-sending entire system prompt per turn** (no caching).
   - **Decision**: Use prompt caching at provider level (Anthropic/Claude already supports it; pass `SystemCacheControl: true` in `Request`).

3. **Context-window overflow as primary compaction trigger**.
   - **Decision**: Keep gil's explicit `compact` verb; add proactive compaction at 80% window fill (Goose pattern: `DEFAULT_COMPACTION_THRESHOLD = 0.8`).

4. **Assumption that strong model = no loop detection needed**.
   - **Decision**: Add the 5-pattern stuck detector from OpenHands (even if rare, catches edge cases for vLLM/local deployments).

---

**Total research time**: ~90 min of file reading + grep. Codex is production-grade; most architectural wins are in persistence + streaming + multi-agent wiring.

