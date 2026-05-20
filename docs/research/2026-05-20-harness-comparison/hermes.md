# Hermes — harness architecture comparison (2026-05-20)

Source: `/home/ubuntu/research/hermes-agent/`. Agent-collected, file:line cited.

## 1. System prompt / self-identity framing

7-layer assembly in `run_agent.py:4357-4506` `_build_system_prompt()`:
1. Identity — `~/.hermes/SOUL.md` if present else `DEFAULT_AGENT_IDENTITY`
   (`prompt_builder.py:134-142`)
2. User-provided system message
3. Persistent memory snapshot
4. Tool-aware guidance (memory/skills/session_search) — only when tools present
5. Context files (AGENTS.md/CLAUDE.md/.cursorrules walked cwd→git root)
6. Timestamp + model/provider metadata
7. Platform hints

System prompt is **cached per session in SQLite**
(`session_db.update_system_prompt()`, line 4492). Rebuilt only after
compression events. Identity is stable mid-session.

Gap signal: identity and user instructions are merged into the same
`prompt_parts` list with no structural boundary — the model can't
distinguish "you ARE the harness" from "this is task context."

## 2. Streaming UX

Three streaming channels (`run_agent.py:5872-5974`):

1. **Token deltas** — `_fire_stream_delta(text)` (5945, 6106). Suppressed
   on tool-call turns (5890-5892).
2. **Tool announcement** — `_fire_tool_gen_started(name)` (5950) fires
   when model starts a tool call, before args complete.
3. **Reasoning** — `on_reasoning_delta` (5960) for extended thinking
   models.

Per-provider streaming: chat_completions `stream=True`,
anthropic_messages `client.messages.stream()`, codex_responses delegated,
bedrock_converse `converse_stream()`.

Gateway routes through events not raw text: `tui_gateway/server.py:115-181`
emits `message.stream_delta` events to the TUI. TUI re-renders per
frame. Wire shape is structured (not pipe-of-tokens).

## 3. Agent loop control flow

Main loop `run_agent.py:9652-9677`:
- `while (api_call_count < max_iterations and budget.remaining > 0) or _budget_grace_call`
- One grace iteration after budget exhausted (9668-9677)
- Compression triggered on `ContextWindowExceeded` via
  `parse_context_limit_from_error()` (9518-9540). Up to 3 passes (9533).
- Anti-thrash: skip compression if last 2 passes saved <10%.

Provider errors classified via `classify_api_error()` (84) →
retryable/fatal.

**No in-loop stuck detector** — gateway-level only
(`tests/gateway/test_stuck_loop.py:25-66`): 3 same-session restarts →
auto-suspend, counter cleared on success.

## 4. Tool surface organization

Toolsets in `toolsets.py`; loaded in `run_agent.py:1451-1472`.

- OpenAI-style function schemas passed to LLM in `tools=` parameter
  (not enumerated in system prompt)
- `enabled_toolsets` / `disabled_toolsets` CLI flags filter
- Delegate tool strips specific blocked tools via `DELEGATE_BLOCKED_TOOLS`
  (`delegate_tool.py:41-49`)
- Skills injected as system-prompt **guidance text** (4460-4476), not
  as tool definitions
- No verify-gate analog — tools register and run directly; approval is
  resolved at runtime via `register_gateway_notify()` callbacks (1331)

## 5. Stuck / recovery patterns

- **Compression** is the main recovery (proactive preflight + reactive)
- **Grace call** after budget exhaust (one final attempt)
- **Adversary consult**: NOT implemented (this is the Goose/Codex
  pattern gil already imported as P67)
- **Subagent escape**: delegate_task hands off to a child with a
  cleaner context

User escape: Ctrl+C → `_interrupt_requested` (9657), or
`steer()` non-blocking inject (1106).

## 6. Subagent / delegation

`tools/delegate_tool.py`:
- `MAX_DEPTH = 1` (line 129): parent (0) → child (1); grandchildren
  rejected unless `_MAX_SPAWN_DEPTH_CAP` raised
- Each child: fresh conversation, own task_id, restricted toolset, focused
  system prompt from delegated goal + context
- Blocked at delegate level: `delegate_task`, `clarify`, `memory`,
  `send_message`, `execute_code` (41-49) — no recursion, no user
  interaction, no cross-platform writes
- Run in `ThreadPoolExecutor`; subagents don't inherit CLI approval
  callbacks
- Parent blocks until children done (`as_completed`); only summary
  returned, child tool calls don't bubble up

## 7. Persistence / autonomy posture

- Session DB at `~/.hermes/sessions.db` (SQLite); messages +
  system_prompt persisted
- System prompt **reloaded from DB on continuation** (9458-9470) to
  preserve prefix cache
- Compression creates a **new session** (not mutation), keeping
  audit-style trail
- No scheduled resume — explicit user/gateway resume only
- Auto-suspend after 3 restarts (gateway safety net)

## What gil should steal / avoid

1. **Steal — per-session system prompt cache.** Hermes stores the
   assembled prompt in the session DB and never rebuilds mid-session.
   Saves ~75% input cost via Anthropic prefix cache hits. Gil rebuilds
   on every turn — `session_prompt.go:636` constructs it fresh each
   Prompt(). Touch: persist the assembled prompt on first build,
   reload on subsequent turns until model/agent profile changes.

2. **Avoid — merged identity+instructions in one flat block.** Hermes
   conflates harness identity with task context in `prompt_parts`.
   That's likely why a "you are the harness" framing doesn't land
   robustly. Keep gil's identity assertion as a distinct **leading
   block** with explicit boundary text before any user/context content.

3. **Steal — token-delta callback chain to TUI.** Hermes wires
   `stream_delta_callback` from provider → agent loop → TUI gateway
   → render. Each layer fires immediately. Gil's `prov.Complete()`
   returns once with full text, then we Send a single Part_Text.
   Need: real streaming provider call (Anthropic SDK supports
   `messages.stream`; Qwen via vLLM supports SSE) + per-token gRPC
   Part_Text emit.

4. **Steal — gateway-level stuck counter.** Hermes's 3-restart →
   auto-suspend is orthogonal to in-loop detection and protects against
   "session keeps crashing on the same turn." Gil P36/P38 reapers
   handle orphan/heartbeat staleness but not "this session crashes 3
   times in a row." Touch: add a per-session crash counter in the
   session table.

5. **Avoid — hardcoded subagent depth in code.** Hermes's
   `_MAX_SPAWN_DEPTH_CAP` is buried at module scope. Gil already lifted
   to depth 2 (P40), but the cap should be config-driven going forward
   so eval sweeps can probe depth=3 without code change. Touch:
   `subagentMaxDepth` constant in `agent_tools_subagent.go`.
