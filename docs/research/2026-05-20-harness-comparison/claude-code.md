# Claude Code — harness architecture observation (2026-05-20)

Source: introspection from inside the running tool (the agent
writing this doc IS Claude Code). Closed source, so file:line
citations are not possible; instead this records observed behavior
and surface contracts I can confirm from my own runtime.

## 1. System prompt / self-identity framing

The system prompt is large (several kilobytes), structured into
distinct sections with explicit headings:
- "You are Claude Code, Anthropic's official CLI for Claude" —
  identity at the very top.
- A "Doing tasks" section with a behavioral mandate.
- A "Tone and style" section ("Your responses should be short and
  concise").
- An "auto memory" section (specific to this deployment).
- A "Tools" reference enumerating each tool with one-line shape.
- Section markers like "# System", "# Doing tasks", "# Using your
  tools" so the prompt reads as headed paragraphs, not a flat wall.

Identity is **persistent for the session**. The same opener appears
on every turn. Unlike gil, Claude Code does NOT inject the
session-id / model-id into the prompt as a line — those are inferred
from context (system reminders, message metadata).

Critically, the prompt repeatedly reinforces "act, don't describe":
- "When given an unclear or generic instruction, consider it in the
  context of these software engineering tasks…"
- "For exploratory questions … respond in 2-3 sentences with a
  recommendation and the main tradeoff."
- "Don't add features … beyond what the task requires."

This is the framing gil's P68a now mirrors.

## 2. Streaming UX

Token-level streaming is the default. Reasoning, text, and tool
calls each have their own stream channel:
- Text deltas arrive incrementally; the terminal client renders
  them as they arrive.
- Tool-call announcements are atomic — the call name + args appear
  as a single unit, not character by character.
- Tool results come back as a separate event after the tool runs.

The wire shape is not exposed (closed source) but the user-visible
behavior is unambiguously real streaming.

## 3. Agent loop control flow

Turn-based. Each user message kicks an agent loop:
1. Assistant produces text + zero-or-more tool calls
2. Tool calls execute; results return as tool_result blocks
3. Assistant produces next response based on results
4. Loop until assistant produces no tool calls AND has answered

Stop conditions I can confirm:
- Assistant decides the task is done (no tool calls + summary text).
- User interrupts (Ctrl+C in the CLI).
- "Compact context" command triggered (manual user signal).
- Hard token cap (5h limit on session length advertised in CLI).

Notably ABSENT in Claude Code:
- In-loop stuck detection (no PatternMonologue / PatternNoProgress
  equivalent).
- Automatic adversary consult.
- Hard agent-turn budget cap that injects synthetic "you've used N
  turns" messages.

The design philosophy: Anthropic's models (Opus 4.7, Sonnet 4.6) are
strong enough at tool use that stuck detection isn't needed at the
harness layer. gil targets the weaker-model regime (qwen3.6-27B) and
takes the inverse stance.

## 4. Tool surface organization

Tools are presented in the system prompt as a flat reference list
with one-line descriptions, grouped by purpose:
- File: Read, Edit, Write
- Search: Grep, Glob
- Execution: Bash
- Notebooks: NotebookEdit
- Web: WebFetch, WebSearch
- Tasks: TaskCreate, TaskUpdate, TaskList
- Background: Bash with run_in_background
- Agents: Agent (sub-agent dispatch)
- Skills: Skill (load a markdown skill file)
- IDE: not always available

Schemas are passed to the model via Anthropic's native tool-use
format (JSON schemas). The system prompt enumeration is for
philosophy / when-to-use guidance, not signature.

**Verify gate equivalent**: not explicit, but the prompt strongly
nudges the model to test its changes ("For UI or frontend changes,
start the dev server and use the feature in a browser before
reporting the task as complete.").

**Skills system** — markdown files (`skills/*.md`) loaded on demand
via the Skill tool. Each skill is a short procedural guide
("test-driven-development", "debugging", etc.). The system prompt
explicitly tells the model: "Invoke relevant or requested skills
BEFORE any response or action. … 1% chance a skill might apply means
that you should invoke the skill to check." This is the closest
analog to gil's `freeze_spec` mandate, but for behavioral patterns
not for goal capture.

gil DOES NOT have a skills concept. The 2026-05-20 user feedback
"skills에 대한 이해도 없고" reflected this — but on inspection, what
the user wanted was the agent to be self-aware of its EXISTING
capabilities (i.e., to act on them, not enumerate them), which P68a
addresses without needing to add a skills system.

## 5. Stuck / recovery patterns

Effectively none at the harness level. Recovery is the user's job
— if the agent gets stuck, the user types a new prompt to steer.

This is the inverse stance from gil. gil's P67 detector + adversary
consult + escalation makes sense BECAUSE gil targets the weaker-
model regime where stuck-detection IS needed. Claude Code's bet is
that Opus / Sonnet won't get stuck the same way; gil's bet is that
qwen-27B will, and the harness must compensate.

## 6. Subagent / delegation

Agent tool can spawn sub-agents with isolated context. Sub-agent
types:
- `general-purpose` — full tool access, used for open-ended research
- `Explore` — read-only search agent (subset of tools)
- `Plan` — architect agent that returns a plan
- Various task-specific agents

Sub-agents return a single message back to the parent. Parent
synthesizes; sub-agent's internal tool calls aren't visible to the
parent's context. Tool description warns: "Trust but verify."

Depth cap not visible to the introspecting agent (me). I'm told
"do not call this tool to chat with itself" — implying recursion is
controlled.

gil's `spawn_agent` is analogous; gil currently caps at depth 2
(P40). Claude Code likely caps similarly but exact policy isn't
exposed.

## 7. Persistence / autonomy posture

Conversation context survives within a session (one tab / one CLI
run). Across sessions, no automatic resume — each new invocation is
a fresh agent.

Persistent memory ("auto memory") is a file-based system: the agent
writes to `~/.claude/projects/...memory/MEMORY.md` and per-fact
markdown files. Future sessions in the same project see the
MEMORY.md index loaded into the system prompt.

gil's analog is the `remember` tool + session memory bank
(`session_memories` table). Both shapes do the same thing — preserve
non-derivable context across runs.

"Walk away" semantics: Claude Code doesn't have an autonomous
background-run mode like gil's `start_run`. If the user closes the
CLI, the session ends. gil's `freeze_spec + start_run + walk away +
come back to a result` is a distinct shape.

## What gil should steal / avoid

1. **Steal — explicit section headings in the system prompt.**
   Claude Code's prompt has `# Doing tasks`, `# Tone and style`,
   `# Tools`, etc. — visible structure helps the model attend to
   the right block. gil's P68a system prompt is one long block;
   could be improved with `# PRIMARY CONTRACT`, `# Routing`,
   `# Tools`, `# Workflow` headers. Touch:
   `server/internal/service/session_prompt.go::defaultChatSystemPrompt`.

2. **Steal — the "skills" pattern, but lightweight.** Don't add a
   full skills system (the user feedback was actually about
   capability self-awareness, addressed by P68a). But the IDEA of
   on-demand procedural guidance is useful: the agent could load a
   "TDD" or "debugging" workflow doc when the task matches. P68g
   defers this — it's not on the autonomy critical path.

3. **Avoid — no in-loop stuck detection.** Claude Code's "trust the
   strong model" stance does not generalize to qwen-27B. P67 +
   P68f are CORRECT for the regime gil targets. Don't follow the
   Anthropic harness on this.

4. **Avoid — kilobyte-scale system prompt.** Claude Code's prompt
   is large (~5K tokens of context). Every turn re-sends it. With
   prefix cache it's cheap, but the maintenance cost is high. gil
   should keep its prompt focused and rely on tool schemas for
   detail.

5. **Steal — "When given an unclear or generic instruction"
   routing rule explicit at the top of the prompt.** Claude Code
   nests this in a longer paragraph; gil's P68a put it as a numbered
   Routing block. gil's structure is actually clearer — keep it.

## Cross-cutting observation

The biggest gap between Claude Code and gil is NOT a technical one
— it's the regime assumption. Claude Code can be minimal at the
harness layer because Opus/Sonnet do the heavy lifting. gil cannot
because qwen-27B can't. Every "gil should steal X" needs to be
filtered through "does this make sense when the underlying model
is weaker?" If yes, steal. If no (e.g., "trust the model" stance),
avoid.
