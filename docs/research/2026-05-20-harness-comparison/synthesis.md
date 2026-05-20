# Harness comparison synthesis (2026-05-20)

Read alongside:
- `codex.md` — OpenAI Codex (Rust)
- `hermes.md` — Hermes agent (Python)
- `claude-code.md` — Claude Code (closed, observed from inside)

GOAL axis tagging:
- **정확도** — agent finishes the task correctly
- **완성도** — agent doesn't give up at ambiguity
- **context 유지도** — state persists across turns / restarts

Each gap below is tagged with the axis it moves. No-axis gaps are
out of scope.

## Cross-table

| Axis | Codex | Hermes | Claude Code | gil (current) |
|---|---|---|---|---|
| Identity opener | "You are Codex … NEVER refuse, delegate" | SOUL.md + 7-layer | "You are Claude Code …" + structured sections | "You are gil …" + ACT-mandate (P68a) |
| Token streaming | SSE 3 channels | 3 channels (text/tool/reasoning) | Native, all channels | OpenAI SSE (P68c); Anthropic stub |
| In-loop stuck detection | None | None | None | Detector + AdversaryConsult + escalation (P67, P68f) |
| Subagent depth | Isolated rollouts | depth 1 (raise cap optional) | Agent tool, depth not exposed | depth 2 (P40) |
| Persistence | JSONL rollout | SQLite + system_prompt cache | Conversation only + MEMORY.md files | SQLite chat_messages (P34) + memory bank |
| Autonomous "walk away" | `codex exec` non-interactive | No background mode | No background mode | `start_run` detached |
| Markdown decorations in agent text | None (terminal verbatim) | Glamour-rendered | Glamour-rendered | Stripped at renderer (P68d) |
| System prompt cache | Per-session | Per-session SQLite | Re-sent per turn (Anthropic prefix cache makes it cheap) | Rebuilt per turn (gap) |
| "Skills" concept | None | None | Markdown skills loaded on demand | None |

## Gaps remaining for gil, ranked

### Tier 1 — directly moves GOAL axis

**G1. JSONL rollout journaling (context 유지도).** Codex pattern.
Per-session `rollout.jsonl` append on every turn, non-destructive
compaction, crash-resilient resume. Currently SQLite chat_messages
is mutated in place on compaction (P35) — a crash mid-compact would
corrupt history. This is P68e in the master plan. Touch:
`core/session/`, new `core/rollout/` package, `server/internal/service/session_prompt.go` for emit, hydrate path.

**G2. Persistent system prompt cache (context 유지도 + 정확도 via prefix
cache).** Hermes pattern. Assemble once per session, store in DB,
reload on subsequent Prompt() calls. Saves ~75% input cost on
multi-turn (Anthropic prefix cache hits a byte-identical prefix).
P68b in master plan. Touch: `core/session/sessions.go` schema,
`server/internal/service/session_prompt.go::buildSystemPrompt`.

**G3. P68f effectiveness tracking (완성도).** P68f shipped the
user-role injection — but if the agent still ignores the directive
in the NEXT 2 turns (0 tool calls), the dispatcher should escalate
to `SubagentBranchStrategy` (already exists in core/stuck/recovery.go).
That covers the case where even a stronger directive doesn't move
behavior. Currently the escalation chain only fires once per
pattern. Touch: `server/internal/service/chat_stuck.go::tick`
+ a small per-session counter in chatEventBuffer.

### Tier 2 — quality of life, indirect GOAL impact

**G4. Provider streaming for Anthropic.** P68c stubbed the Anthropic
path (Complete + single delta). When gil deploys against a Claude
model, users will want real token streaming. Touch:
`core/provider/anthropic.go::StreamComplete` — use the official Go
SDK's `messages.stream`.

**G5. System prompt section headers.** Claude Code pattern.
Adding `# PRIMARY CONTRACT`, `# Routing`, `# Tools`, `# Workflow`
visual breaks in `defaultChatSystemPrompt` may help the model
attend better. Low effort, low risk. Touch:
`server/internal/service/session_prompt.go:264`.

**G6. Eval-loop coverage of P68 changes.** P67j (chess) showed 2/3
PASS with adversary. After P68a + P68d + P68f, the same task
should be re-measured at N=5+ to validate the agent-shape
improvements actually move PASS rate, not just user impressions.
Also re-measure vm + spmc. Touch: `docs/eval/task-surface.md` +
sweep scripts.

### Tier 3 — informational / parking lot

**G7. Skills concept.** Claude Code-style on-demand procedural
guidance. Lightweight version: a `~/.config/gil/workflows/` dir of
markdown files the system prompt references. Not on autonomy
critical path; defer.

**G8. Provider tool-call streaming.** P68c streams text deltas
only. Tool-call announcements + argument streaming are an Anthropic
SDK feature gil doesn't use. Low-value for current model regime —
qwen doesn't usefully stream tool args. Park.

**G9. Bigger system prompt with explicit anti-pattern examples.**
Claude Code's prompt includes "Red Flags" tables with thoughts that
mean STOP. gil's P68a has a "FAILURE MODES" framing but no
example-table. Could help with weaker models. Worth experimenting
with after the Tier 1 work lands.

## What P68 shipped vs what was planned

Master plan (`docs/superpowers/plans/2026-05-20-agent-shape-overhaul-plan.md`):

| Phase | Status | Notes |
|---|---|---|
| P68a — System prompt overhaul | ✅ shipped (PR #7) | Dogfood: 5/5 prompts route correctly |
| P68b — Prompt cache | ⏳ pending | G2 above |
| P68c — Token-delta streaming | ✅ shipped (PR #9) | OpenAI SSE; Anthropic stubbed |
| P68d — Strip markdown at renderer | ✅ shipped (PR #8) | Belt-suspenders for P68a leakage |
| P68e — JSONL rollout | ⏳ pending | G1 above |
| P68f — Adversary suggestion strength | ✅ shipped (PR #10) | User-role injection of directive |
| P68g — Synthesis + claude-code.md | ✅ shipped (this doc) | Informational |

## Cross-cutting principles validated

- **Single user-facing surface, agent routes** (`[[feedback_natural_language_single_surface]]`):
  Claude Code matches; Codex bifurcates (`codex` vs `codex exec`).
  Hermes single-surface. gil should stay single-surface.
- **Trust the model where it deserves it, harness-compensate where
  it doesn't.** Claude Code trusts; gil at qwen-27B can't and
  shouldn't.
- **Persistence is a tier-1 concern.** Hermes + Codex both invest
  heavily in persistence (SQLite + system-prompt cache,
  JSONL rollout). Claude Code less so (session-scoped). gil sits
  middle and should pull toward the Hermes/Codex end.
