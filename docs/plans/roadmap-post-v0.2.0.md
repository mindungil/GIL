# Post v0.2.0 — roadmap

> v0.2.0 ships chat-architecture migration (M1-M5), M6 Option A V1
> agent tree, MCP complete, subagent (G5), §2.6 verb-tool wave. What
> comes next, in dependency / severity order — no timeline or phase
> numbers.

## Severity buckets

### Severity 1 — would block a real adoption story

**Real-LLM hardening.** v0.2.0 ran 100% mock providers. Anthropic /
OpenAI / OpenRouter calls in production surface things mock tests
can't: rate-limit dynamics, partial responses, tool-call malformedness
under temperature, cost-meter accuracy. Need:

- VCR-cassette integration tests (record once, replay forever) for
  every supported provider.
- One long-run (1+ hr) live dogfood on a real codebase, with the
  resulting trace persisted under `docs/dogfood/`.
- Failure injection: 503 / 429 / partial tool_use / context overflow
  — verify Retry / compact / stuck-detect / verify-loop discipline
  all behave under genuine model output, not mock turns.

**Surface — Option B/C decision.** *Resolved 2026-05-16 in
[`docs/design/chat-surface-decision.md`](../design/chat-surface-decision.md):*
Both B and C rejected. The actual user pain is narrower than the
multi-pane framing assumed — bare `gil` Renderer has no
ToolCall/ToolResult methods at all, so tool calls happen invisibly on
the daemon. Adopted narrow alternative: ~340 LOC chat tool-call
narration layer (additive, no pane redesign, no snapshot churn). See
the decision doc for rationale and the implementation outline. The
narration layer moves to Severity 2 below.

### Severity 2 — substantive user-visible gaps

**Persistent working set.** *Resolved 2026-05-15: P30 schema v4 +
write-through pattern landed on `feat/p30-workingset-persist`.* The
in-memory `workingSet` now writes through `workingset_entries` and
re-hydrates on first access after a daemon restart.

**Chat tool-call narration.** Per the surface decision above, bare
`gil` chat needs ToolCall/ToolResult Renderer methods so the user can
see what the agent is doing during a turn. ~340 LOC, single PR,
additive. Sits independently of any future Option B revisit.

**Workspace rollback safety net.** `restore_checkpoint` refuses while
a run is active; OK. But after a restore there's no automatic
"refreeze + restart" hint — the agent is mid-conversation when the
files under it changed. Either surface the discontinuity to the LLM
via a system note or document the manual recovery (reset_session →
re-freeze).

**Reasoning split.** Models that emit `<thinking>` / extended thinking
get mashed into the chat transcript as plain text. Need typed
`AssistantOutput` proto with reasoning vs. visible-response branches.
Cross-cuts proto + provider adapter + renderer; bigger than it looks
because the change touches every consumer of `provider.Response.Text`.

### Severity 3 — quality / ergonomics

**Typed tool-action pipeline (#8).** `tool_call` events carry the
raw input JSON; renderers re-parse per tool to make narration ("⚒
read_file path=…"). A typed Part shape would let renderers
short-circuit the re-parse and unify chat-surface narration with
giltui's Agent Tree.

**MCP OAuth + reauth flow.** `core/mcpregistry` stores `bearer:<token>`
inline. `gil mcp login <name>` is a stub. Real OAuth devices flows
(GitHub / Linear / etc.) need a token-refresh loop and revoke handling.

**WorkingSet → repomap integration.** `list_workingset` currently
returns string paths. Repomap (Phase 6, Aider PageRank) builds a
weighted graph of imports; tying WorkingSet members in as boosts
would let the agent steer context retrieval based on user pins.

**Schema migration tool.** Sessions DB schema is at v3. We currently
only forward-migrate at daemon startup. No downgrade, no schema
inspection CLI. `gil doctor` should at least surface "schema v3 of N".

### Severity 4 — long horizon

**Plugin hooks (opencode parity).** opencode exposes ~14 typed Hooks
(`chat.message`, `permission.ask`, `tool.execute.before/after`,
etc.). gil has Renderer but no typed extension surface. Significant
proto + interface work; gated on a concrete plugin need.

**VS Code marketplace publish.** Scaffold exists in `vscode/`;
publisher account + `vsce publish` blocked on user action.

**Cross-host distribution.** Homebrew tap + linux package builds +
.deb/.rpm artefacts are GoReleaser-generated on tag push but no
binary release for v0.2.0 has been cut yet. User action.

**History rewrite (Phase 17 legacy).** 209 pre-v0.1.0-alpha commits
authored as `Test User`. `git filter-repo` to rewrite is a one-time
job; either before or after v1.0 cut.

## Cross-cutting dependencies

- Reasoning split (S2) + typed action pipeline (S3) share the same
  proto change. Land them together.
- Persistent WorkingSet (S2) + repomap integration (S3) share the
  same storage layer.
- Option B/C surface decision (S1) + chat-mode Renderer rework
  (separate) — both interact with `cli/internal/chat/render`.

## v1.0 criteria (informal)

For gil to ship v1.0:

1. Severity 1 cleared. Real LLM behaviour validated, chat surface
   decision landed.
2. WorkingSet persists.
3. Reasoning split + typed action pipeline land. Renderer stops
   re-parsing.
4. At least one external user runs a multi-day task end-to-end and
   submits a writeup.
5. Binary release with checksums + brew tap published.

Until those are done, semver stays in 0.x. v0.2.x is the natural home
for Severity 2 work; v0.3 is the natural home for Severity 1 closure.
