# Phase 40 — Subagent depth lift (1 → 2)

## Why

`subagentMaxDepth=1` capped autonomous decomposition at "root can
spawn children but children cannot spawn further." Real complex
work has 3-level structure: a top-level coordinator spawns
sub-coordinators that each spawn workers. Examples:
- "Refactor module X" → split per-submodule subagents → each
  spawns per-file edits
- "Run regressions across the matrix" → split per-config
  subagents → each spawns per-test workers

The depth-1 cap forced the agent to flatten this — either do it
sequentially in the root, or use a single layer of subagents each
doing N tasks serially. Both shapes lose parallelism.

For the autonomous coding harness goal, deeper decomposition is
exactly what unlocks autonomy on harder tasks. The harness should
make safe deeper subagent trees possible.

## Goal

Lift `subagentMaxDepth` from 1 to 2. Depth=1 children CAN spawn
depth=2 grandchildren. Depth=2 cannot spawn further. Existing
per-root concurrency cap (8 active) protects against fork bombs
regardless of distribution across the tree.

## Why 2 and not 3+

Going to 3+ would need a per-depth concurrent cap (e.g. max 4
grandchildren per child) to prevent one greedy depth-1 child from
exhausting the per-root budget with shallow grandchildren and
starving its siblings. That's a richer design; defer to a future
phase when there's a concrete need.

2 unlocks 90% of the decomposition value (coordinator-of-
coordinators is the common shape) while staying within the existing
safety envelope.

## Design

### Constants

```go
const (
    subagentMaxPerRoot = 8  // unchanged — total across the whole tree
    subagentMaxDepth   = 2  // was 1
)
```

### Subagent system prompt

The subagent hint conditionally tells the agent whether it can
spawn further:
- `childDepth < subagentMaxDepth` → "You can spawn one more layer"
- `childDepth >= subagentMaxDepth` → "Do not call spawn_agent"

This way the LLM sees the actual cap, not a stale "you can't spawn"
prompt at every depth.

### spawn_agent tool description

Updated to mention depth=2 + the per-root total cap explicitly so
the parent LLM knows what's available.

### resolveRootSessionID walker

Comment refreshed; the `hopLimit=8` already accommodates the new
depth without code change.

## Acceptance criteria

1. Depth=1 parent spawning → succeeds, depth=2 grandchild created.
2. Depth=2 parent spawning → errSubagentDepthExceeded.
3. Per-root cap still applies across the tree (4th spawn under one
   root → errSubagentLimitReached regardless of depth distribution).
4. Existing 5 subagent registry tests (P0 baseline) pass unchanged
   when an explicit maxDepth is set in the test.
5. The subagent system prompt accurately tells each layer whether
   it can spawn further.

## Result (2026-05-17)

**Shipped.** 6 tests pass (5 existing + 3 new for depth=2
behavior; PerRootCap test is a sanity check across depths).

Test inventory:
- `TestSubagentRegistry_SpawnDecrementsOnRelease` (pre-existing)
- `TestSubagentRegistry_RejectsAtConcurrencyLimit` (pre-existing)
- `TestSubagentRegistry_RejectsBeyondDepth` (pre-existing, uses
  explicit maxDepth=1 — still passes; pins the floor behavior)
- `TestSubagentRegistry_AllowsGrandchildAtDepth2` (NEW)
- `TestSubagentRegistry_RejectsGreatGrandchildAtDepth3` (NEW)
- `TestSubagentRegistry_PerRootCapStillAppliesAcrossTree` (NEW)

LLM-facing text updated:
- `agent_tools_subagent.go` description: explicit depth=2 + per-root
  cap, mentions that depth=1 children can spawn one more layer.
- `agent_tools_subagent.go` subagent system prompt: conditional
  spawnHint based on the child's actual depth — "can spawn one
  more" vs "do not call spawn_agent" depending on depth vs cap.
- `session_prompt.go` chat agent system prompt: spawn_agent tool
  bullet refreshed to "depth 2 (children CAN spawn one further)."

Files touched: 4
- `server/internal/service/subagent_registry.go` — const bump + comments
- `server/internal/service/subagent_registry_test.go` — 3 new tests
- `server/internal/service/agent_tools_subagent.go` — description +
  conditional subagent hint
- `server/internal/service/session_prompt.go` — chat agent system
  prompt tool bullet
- `docs/plans/phase-40-subagent-depth-lift.md` — this doc
