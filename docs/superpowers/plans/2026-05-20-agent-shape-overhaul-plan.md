# Agent-Shape Overhaul — Master Plan (P68 series, 2026-05-20)

## Why this exists

Cold dogfood of `gil chat` on 2026-05-20 (user-driven) surfaced a class
of failures the existing eval-loop didn't catch:

1. Agent **describes** instead of **acts**. Prompt: "자율 실행은 어떻게
   하는건데?" → reply was a 3-step tutorial about freeze_spec / start_run
   / show_status, trailing "원하시나요?". Zero tool calls. No
   freeze_spec, no start_run. The agent proved by its behavior that
   it isn't autonomous.
2. Response text doesn't stream. `[think]` reasoning renders incrementally
   (its own channel), but assistant text arrives as one block — because
   `provider.Provider.Complete()` is synchronous-return.
3. Chat-app decorations leak through: `**bold**`, bulleted command
   lists, emojis, `>`-prefix for user input, code fences for "here's
   the command you'd run." The shape says chat, not agent.
4. Single surface, agent routes (user clarification): user wants ONE
   `gil` surface; the AGENT decides chat-vs-execute based on the input.
   Bifurcating into `gil chat` vs `gil exec` is wrong.
5. Adversary suggestions are emitted (P67j chess r2 turn 4) but the
   agent ignored them. Suggestion strength + non-actionability when
   the agent's next turn could still be silent.

This plan also folds in the harness comparison artifacts written by
Explore agents on 2026-05-20:
- `docs/research/2026-05-20-harness-comparison/codex.md`
- `docs/research/2026-05-20-harness-comparison/hermes.md`

`claude-code.md` (Claude Code introspection from inside the tool) is
written in P68g.

## GOAL alignment

Per [[gil_autonomy_arc_2026_05_17]] + recent feedback. Three axes:

- **정확도** — agent finishes the task correctly
- **완성도** — agent doesn't give up at ambiguity
- **context 유지도** — state persists across turns / daemon restarts

Each phase below tags which axis it moves. No-axis-move phases get
deprioritized.

## Method (per user directive, 2026-05-20)

- Heavy thinking, large + well-divided phases. Inside each phase,
  further sub-task breakdown.
- Each phase ends with a **dogfood gate**: I run `gil chat` (or
  equivalent surface) with concrete prompts, observe behavior, and
  only advance if the acceptance criteria pass. Self-audit per
  [[feedback_explicit_self_audit_gates]] applies at phase boundaries.
- Subagent fanout for phases that touch multiple file regions in
  parallel (P68c providers, P68e JSONL writer/reader). Inline for
  phases that are one focused edit (P68a, P68d).
- No "next phase" until the prior phase's dogfood gate is green.
- Acceptance criteria must be **observable in the chat surface** —
  not just "tests pass."

## Phase list (in execution order)

### P68a — Agent-shape system prompt overhaul

Move: 정확도 + 완성도.

The current default prompt (`session_prompt.go:264` —
`defaultChatSystemPrompt`) is dense (~160 lines) and buries the
behavioral mandate under tool docs. Worse, it literally says "Respond
conversationally" (line 267) which the model takes as license to chat.

Sub-tasks:
- a.1 Rewrite the leading mandate block: identity, ACT-first contract,
  anti-tutorial rules, no-markdown rule, first-message routing.
- a.2 Mirror to `exploreChatSystemPrompt` and `planChatSystemPrompt`
  (`agent.go:94, 113`) — they need the same anti-tutorial framing
  even though their tool set is narrower.
- a.3 Trim tool docs to one short sentence each — schemas already
  carry the full signature. The prompt is for *philosophy*, not
  reference.
- a.4 Add a "FAILURE MODES" section that explicitly names anti-patterns
  the model just exhibited (tutorial writing, enumerating tools,
  trailing "원하시나요?", emoji decoration). Negative-shaping helps
  with weaker models.
- a.5 Make sure the system context line (provider/model/session) is
  visible enough that "what model are you?" returns one line, not a
  capabilities tour.

Dogfood gate:
1. New session. Prompt: "자율 실행은 어떻게 하는건데?"
   - Expected: ONE of (a) agent inferred a task and called
     freeze_spec, (b) agent called request_user_input asking ONE
     concise "어떤 작업?" question, (c) a 1-2 sentence prose reply
     that prompts for a task with no tutorial / no markdown.
   - REJECT: any reply that enumerates freeze_spec / start_run /
     show_status step-by-step.
2. New session. Prompt: "/home/ubuntu/contest_data 에 dataset.zip
   풀고 첫 줄만 보여줘".
   - Expected: tool calls (run_bash unzip, head, etc). NO prose
     tutorial.
3. New session. Prompt: "어떤 모델이야?"
   - Expected: one short line naming the model. Not a tour.
4. New session. Prompt: "안녕".
   - Expected: short greeting + one prompt for a task. Not a markdown
     menu.
5. New session. Prompt: "뭔가 좀 고쳐줘".
   - Expected: `request_user_input` ONE focused question. NOT a
     multi-line "I can fix many things, would you like..."

Acceptance:
- 5/5 dogfood prompts pass.
- No `**`, no emoji, no trailing "would you like me to..." in any
  reply.
- Plus: `go test ./server/...` clean.

### P68b — Persistent identity & system prompt cache

Move: context 유지도 (+ 정확도 via prefix cache).

Hermes lift (`hermes.md` §7): per-session prompt cache makes the model
see byte-identical system prefixes across turns → Anthropic prefix
cache hit → ~75% input-cost reduction. Also means identity doesn't
*drift* if any per-turn assembly logic shifts.

Sub-tasks:
- b.1 Decide storage: new column on `sessions` table vs new
  `session_system_prompt` table. Likely just a column (one prompt
  per session).
- b.2 Compute prompt once, write on first Prompt() if absent, reload
  on subsequent Prompt() calls.
- b.3 Invalidate (rebuild) when: agent profile changes, model changes,
  or operator explicitly requests `reset_session`.
- b.4 Memory block (cross-session memory) — decide if it's part of the
  cached prompt or per-turn. Currently per-turn (`session_prompt.go:643`).
  Leave per-turn but document the carve-out.

Dogfood gate:
- New session, 3-turn conversation. Trace shows system prompt
  built ONCE (instrument with a debug log or count).
- Continue same session in a second Prompt() call — verify same prompt
  hash reused.

### P68c — Token-delta provider streaming

Move: 정확도 (real-time feedback) + 완성도 (user can interrupt early).

`provider.Provider.Complete(ctx, req) (Response, error)` is synchronous
return. The chat surface emits one `Part_Text` per turn with the entire
response text. Reasoning channel is separate (and DOES appear to stream),
which is the only thing keeping the experience from being totally
batch. Real streaming requires SSE/stream API at the provider layer
forwarded as per-delta `Part_Text` parts.

Sub-tasks:
- c.1 Add interface method `StreamComplete(ctx, req, onDelta func(Delta))
  (Response, error)`. Returns full response (same as Complete) AND
  fires the callback for each text/reasoning/tool-call announcement
  delta.
- c.2 Implement in `core/provider/anthropic.go` (use `messages.stream`).
- c.3 Implement in `core/provider/openai.go` (vLLM/qwen via OpenAI SSE).
- c.4 Implement in `core/provider/mock.go` (yield each turn's text in
  3-4 chunks for testability).
- c.5 Implement in `core/provider/retry.go` wrapper (forward callback).
- c.6 `session_prompt.go` lines 740-790: replace `prov.Complete(...)`
  with `prov.StreamComplete(..., onDelta=stream.Send(Part_Text{...}))`.
  Reasoning + tool-call announcement deltas also stream.
- c.7 Keep `Complete()` available — `core/stuck/recovery.go`'s adversary
  call wants a one-shot full response, not streaming. Define
  `Complete(req)` as `StreamComplete(req, func(d){})` (accumulator).

Subagent fanout: providers c.2 / c.3 / c.4 / c.5 are independent.
Spawn 4 parallel agents.

Dogfood gate:
- New session. Prompt: "list_sessions" intent (e.g. "최근 세션 보여줘").
- Expected: visible delta-by-delta rendering of the reply, not
  one blast at end.

### P68d — Client rendering: kill chat decorations

Move: 정확도 (output matches "agent acting" shape, not "LLM chatting").

The user's `> ▏ ‹ ... 🚀` rendering is happening in the chat client
(probably `cli/internal/chat/*` or `tui/*`). The system prompt now
tells the model to drop markdown emphasis, but the CLIENT also needs
to stop rendering markdown emphasis it might still receive.

Sub-tasks:
- d.1 Find the chat client renderer.
- d.2 For assistant replies: render as plain text. No `**bold**` → bold,
  no `1.` → automatic-numbering. Keep code fences (model may legitimately
  quote code).
- d.3 For tool calls: render as `→ tool_name(<short args>)` not a chat
  bubble. Result on the next line, indented.
- d.4 User input: keep readable but drop the chat-app `> ` prefix.
- d.5 Remove emoji rendering pass (if any).

Dogfood gate:
- Visual diff via the same 5 prompts in P68a. Side-by-side screenshots.

### P68e — Codex JSONL rollout journaling

Move: context 유지도 (crash recovery, audit, replay).

Codex lift (`codex.md` §7): write every turn to a JSONL rollout file.
Compaction is non-destructive (writes a summary entry). Resume reads
forward, rebuilding state. Gil's SQLite chat_messages is mutated in
place at compact-time.

Sub-tasks:
- e.1 Spec the rollout JSONL schema (turn header, message-by-message,
  tool calls, tool results, system events, adversary events).
- e.2 Add per-session `~/.local/share/gil/rollouts/<sessionID>.jsonl`
  writer alongside the existing SQLite store. Best-effort, never
  blocks the chat turn.
- e.3 Resume path reads JSONL forward to rebuild in-memory state
  (parallel to current SQLite hydrate).
- e.4 Compaction: appends a `{"type":"compact","summary":"..."}` entry
  to the JSONL; doesn't truncate. SQLite still rewrites for query speed.
- e.5 `gil export` consumes the JSONL directly (no need to query SQLite).

Subagent fanout: writer / reader / spec definition are independent
once the schema is fixed. Spawn 3 agents after e.1.

Dogfood gate:
- 3-turn session. Kill daemon. Restart. New Prompt() to same session
  rebuilds history. Spot-check that JSONL is human-readable / has all
  turns.

### P68f — Adversary suggestion strength

Move: 완성도 (boundary tasks where adversary fires but agent ignores).

Chess r2 (2026-05-20 P67j sweep) showed adversary fired at turn 4 with
a specific suggestion ("stop diagnosing, inspect the code") — agent
ignored it. Two interventions:

Sub-tasks:
- f.1 When adversary fires, the suggestion text gets prepended to the
  next user turn as `[ADVERSARY: <text>] Continue.` instead of a
  `[system]` decoration the model may treat as ambient noise.
- f.2 Track per-session adversary effectiveness: count tool calls in
  the N=2 turns after each adversary fire. If zero in either turn,
  next adversary fire uses `SubagentBranchStrategy` instead of plain
  consult — fresh read-only sub-agent to look at the code.
- f.3 Decide: should adversary be allowed to *force* a tool call by
  emitting a synthetic tool-call directive? Probably no
  (system-imposed action violates
  [[feedback_agent_drives_system_safeguards]]). Document the decision.

Dogfood gate:
- Chess N=3 re-measurement. Expected delta vs P67j: in the case
  adversary fires, the next agent turn calls a tool ≥80% of the time.

### P68g — Comparison synthesis + claude-code introspection

Move: informational (no direct axis movement; informs future phases).

Sub-tasks:
- g.1 Read codex.md + hermes.md (already written by Explore agents).
- g.2 Write `claude-code.md` based on my own behavior as Claude Code
  (system prompt I see, tool surface, how I handle "describe a task"
  vs "answer a question" routing).
- g.3 Synthesis doc cross-tabbed with GOAL-axis tagging. Update the
  gap list / Followup TODOs.

Dogfood gate: artifacts exist; no behavioral validation needed.

## Cross-cutting concerns

- **Build/regression sweep** after each phase: `go test ./server/...
  ./core/...`. Pre-existing `TestDiscover_NoMarkersReturnsCwd` is
  the only known fail (unrelated /tmp symlink).
- **PR per phase**: separate branch + PR per phase so reviewers can
  see one concern at a time.
- **Memory updates**: each phase that changes a load-bearing pattern
  updates the corresponding memory file. New memories only when the
  pattern would be useful in future conversations.
- **No timeline guesses in this doc** per
  [[feedback_no_timeline_in_todo_docs]].

## End-of-day status (2026-05-20 evening)

| Phase | Status | PR / commit | Dogfood gate |
|---|---|---|---|
| P68a | ✅ shipped | PR #7 (merged 07bf819) | 5/5 prompts route correctly; markdown variance accepted |
| P68b | ✅ shipped | PR #11 (merged) | cached_system_prompt populated 10674b, key=`vllm:qwen3.6-27b:default` |
| P68c | ✅ shipped | PR #9 (merged f77a518) + fix e1f9013 | list_sessions tool call streams; no 400 |
| P68d | ✅ shipped | PR #8 (merged 3e02fdd) | bullets/bold/inline-backtick stripped at TUI+CLI |
| P68e | ⏳ parked | — | JSONL rollout; deferred per synthesis Tier-1 ranking |
| P68f | ✅ shipped | PR #10 (merged 92ad354) | chess N=3 sweep in flight on develop tip |
| P68g | ✅ shipped | within PR #10 | claude-code.md + synthesis.md |

P67l (telemetry from prior series) also shipped (PR #6).

## Out of scope for this plan

- Adding a *skills* system (Claude Code style markdown skill files).
  The user mentioned "skills에 대한 이해도 없고" but inspection shows
  the complaint was about the agent not knowing its capabilities, not
  about gil lacking skills as a concept. P68a's prompt overhaul
  addresses the self-awareness gap directly.
- Surface bifurcation (`gil chat` vs `gil exec`). User clarified the
  intent is ONE surface, agent routes internally.
- Provider-side tool-use training improvements. We can't retrain
  qwen3.6-27b; we can only prompt-engineer around its defaults.
