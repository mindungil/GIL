# codex (codex-rs) vs gil — comparative inventory (2026-05-12)

Snapshot of structural / functional comparison between OpenAI's
codex CLI (`/home/ubuntu/research/codex/codex-rs/`, ~350K LOC Rust,
119 crates) and gil at v0.2.0 (~87K LOC Go, 8 workspace modules).

Goal: a serious anchor for design decisions — not a marketing
ranking. "Bigger surface" isn't always "better"; "more conservative"
isn't always "safer." Each row notes both numbers and what the
choice trades for.

## 0 · One-paragraph framing

codex is OpenAI's first-party agentic coding CLI: very wide tool
surface, sophisticated guardian/approval model, OAuth-driven login
flow, very mature distribution (npm + brew + binary), single-agent
per session. gil is a smaller, opinionated harness: chat-as-only-
surface, verify-loop as a state machine (acceptance commands actually
flip step status), subagent delegation built in, multi-provider out
of the box (Anthropic native + OpenAI-compatible), MCP wired both in
chat and run mode. codex outweighs gil ~4× in LOC; gil ships a
discipline (verify) + a delegation primitive (subagent) codex does
not.

## 1 · LOC and module layout

| | codex-rs | gil v0.2.0 |
|---|---|---|
| Total LOC | ~350K (Rust) | ~87K (Go) |
| Major modules | 119 crates | 8 workspace modules |
| Top crates by LOC | `tui` 180K, `core` 149K | `core` 36K, `cli` 16K, `server` 14K, `tui` 9K |
| Binary entries | 1 (`codex`) | 4 (`gil`, `gild`, `giltui`, `gilmcp`) |
| Server/client split | Monolith CLI | gRPC daemon (`gild`) + clients (`gil`, `giltui`) |

The architecture difference is significant: codex is a single binary
that does everything in-process. gil splits a long-lived daemon from
short-lived clients via bidirectional gRPC — a session survives
client disconnects and can be tailed concurrently from CLI + TUI.

## 2 · Agent tool surface

**codex** registers tools through `/codex-rs/tools/` (specs) + per-
crate registration. Standard set includes shell exec, file
read/write/edit, apply_patch, MCP forwarders. Tool surface is
"normalized for model" at `/codex-mcp/src/tools.rs:144–160` (dedup +
schema sanitization).

**gil** registers in `server/internal/service/agent_tools.go`
`buildChatToolRegistry`. Final count v0.2.0 (built-in, before MCP
dynamic):

| Family | Tools |
|---|---|
| Read-only meta | show_diff, show_spec, show_status, list_sessions, request_compact |
| Code I/O | read_file, write_file, edit_file, apply_patch, run_bash, grep, glob, web_fetch |
| Verify discipline (M5) | plan_steps, verify, todowrite |
| Lifecycle (G1) | freeze_spec, start_run, apply_diff |
| Subagent (G5) | spawn_agent, wait_agent, agent_status |
| §2.6 verbs | add_to_workingset, drop_from_workingset, list_workingset, stop_run, list_checkpoints, restore_checkpoint, show_instructions, export_session, reset_session |
| MCP (dynamic) | per spec.Tools.McpServers allowlist |

Total ~30 built-in + dynamic MCP. codex's "shell + apply_patch + MCP"
surface is leaner. gil's wave of verb tools (workingset, stop_run,
checkpoints, export, reset) corresponds to what codex doesn't have:
codex assumes the user steers context via the chat; gil makes those
verbs first-class tool calls so the agent can perform them via
natural language.

## 3 · System prompt

| | codex | gil |
|---|---|---|
| Canonical prompt | `/protocol/src/prompts/base_instructions/default.md` (276 LOC) | `defaultChatSystemPrompt` in `server/internal/service/session_prompt.go` (~180 LOC) |
| Specialized prompts | `REVIEW_PROMPT` (88 LOC), `SUMMARIZATION_PROMPT` | `exploreChatSystemPrompt` + `planChatSystemPrompt` (`server/internal/service/agent.go`) |
| Key invariants | obey AGENTS.md, no git commit unless asked, use apply_patch never raw shell edits | natural language only (no slashes), verify before declaring done, status transitions system-enforced |

Both lean on a "concise / direct / no marketing voice" tone. The
verify discipline is gil's distinguishing invariant — codex's
acceptance check is a *prompt suggestion*, gil's `verified` status
can only be set by the system after the verify tool runs the
acceptance command.

## 4 · Verify / acceptance discipline

**codex** — Has "Validating your work" section in the system prompt
(default.md:149–157). Proactive testing in `never`/`on-failure`
approval modes, deferred in interactive. Compaction has pre/post
hooks (`/core/src/compact.rs:69–94`) but no system-enforced
acceptance gating: the model can declare a task done without a test
passing.

**gil** — `plan_steps` declares the work + acceptance commands.
`verify` tool runs those commands and the *system* (not the agent)
sets the step status to `verified` only when the acceptance command
exits 0. The agent literally cannot self-mark a step verified — the
status field is system-managed (`core/verify` + M5 design).

This is the headline architectural difference. codex relies on prompt
discipline; gil relies on a state machine.

## 5 · Subagent / delegation

**codex** — Not present. Single-agent per session. `/cloud-tasks/`
crate exists but it's for offloading long-running work to Google
Cloud Tasks, not for spawning sub-agents.

**gil** — `spawn_agent` / `wait_agent` / `agent_status` tools (G5).
`subagentRegistry` enforces caps (8 concurrent per root, depth 1
V1). `sliceSpec` builds a child FrozenSpec from the parent with
budget ⅓ rule. Ask-callback routes child permission asks to the
root session so the user sees one queue.

For tasks that decompose naturally (investigate X while editing Y),
gil's subagent layer is a real capability that has no codex
equivalent. The trade-off is complexity: codex's single-agent model
is simpler to reason about and debug.

## 6 · MCP support

**codex** — `/codex-mcp/src/` is mature. Per-server lifecycle
manager (`connection_manager.rs`), OAuth scope discovery
(`mcp/auth.rs`), interactive auth elicitation
(`auth_elicitation.rs`), tool name normalization
(`tools.rs:144–160`). Production-grade.

**gil** — `core/mcp/` (stdio client, JSON-RPC) + `core/mcpregistry/`
(global + project TOML scope). `launchMCPServers` (run mode) +
`ensureSessionMCPTools` (chat mode) share a per-session subprocess
cache. OAuth login is a stub (`gil mcp login <name>` exists but
doesn't implement the flow). HTTP transport is documented but not
implemented; only stdio works v0.2.0.

codex is well ahead on MCP. gil has the mechanics wired end-to-end
and the spec-as-allowlist contract, but OAuth + HTTP transport are
follow-ups.

## 7 · Sandbox / isolation

| Backend | codex | gil |
|---|---|---|
| Linux bwrap | `/linux-sandbox/`, `/bwrap/` | `runtime/local/bwrap.go` |
| macOS Seatbelt | `/utils/sandbox-summary/` (metadata only) | `runtime/local/seatbelt.go` (implemented) |
| Docker | n/a (relies on Codex CLI install model) | `runtime/docker/` (per-command exec) |
| SSH | n/a | `runtime/ssh/` (per-command exec + rsync sync) |
| Cloud — Modal | n/a | `runtime/modal/` |
| Cloud — Daytona | n/a | `runtime/daytona/` |
| Windows | `/windows-sandbox-rs/` | not supported |

Both are credible. codex covers Windows; gil covers Docker, SSH, and
cloud (Modal/Daytona) sandboxes. Different bets.

## 8 · Permission / approval model

**codex** — `AskForApproval` enum (5 levels: UnlessTrusted, OnFailure,
OnRequest, Granular, Never) in `/protocol/src/protocol.rs`. Guardian
system has typed approval-request events
(`/core/src/guardian/approval_request.rs`), per-tool risk scoring
(`/protocol/src/approvals.rs`), and a structured "what's about to
happen + what's the risk" surface for the user.

**gil** — `AutonomyDial` enum (4 levels: PLAN_ONLY, ASK_PER_ACTION,
ASK_DESTRUCTIVE_ONLY, FULL) in `proto/gil/v1/spec.proto`. Permission
evaluator (`core/permission/evaluator.go`) + wildcard rules + per-
spec ruleset. Permission asks bridge to both giltui and CLI REPL
since G4. Less granular than codex's guardian model.

codex's guardian model is more sophisticated; gil's autonomy dial is
simpler and easier to reason about per-session.

## 9 · Context management (compaction)

**codex** — `compact.rs` (146 LOC) auto-compacts when message
history reaches `model_auto_compact_token_limit`. Summarization is
model-driven via SUMMARIZATION_PROMPT. Remote-compaction variant
(`compact_remote_v2.rs`) calls a provider-backed compactor.

**gil** — `core/compact/` (compactor + history + template + cache).
Hermes-pattern Head/Middle/Tail compression with anti-thrashing
guards + Anthropic `cache_control` on the system prompt and the last
3 messages so the prompt cache prefix survives compactions. The
cache-preserving design specifically targets multi-day runs where a
single compaction would otherwise invalidate the entire prefix.

Different design points: codex compacts globally with a model
summarization. gil splits the history into preserved (head/tail) +
compressed (middle) so the cache wins.

## 10 · Stuck / loop detection

**codex** — Not present. No loop detection, no "agent gave up"
recovery. Long-context cases are handled via compaction.

**gil** — `core/stuck/` has 6 detection patterns (RepeatedAction,
RepeatedObservation, RepeatedError, Monologue, PingPong, NoProgress,
ContextWindow) and 4 recovery strategies (ModelEscalate, AltToolOrder,
ResetSection, AdversaryConsult, SubagentBranch). Emits
`stuck_detected` / `stuck_recovered` events.

This is another gil-only capability. Whether it pays off depends on
how often models genuinely loop in real workloads — needs empirical
validation (S1 follow-up in the roadmap).

## 11 · Surface model

**codex**:
- Interactive: full-screen TUI (Ratatui, 180K LOC).
- Headless: `codex exec PROMPT` for CI / scripts.
- Review mode: `codex review` non-interactive.
- Many verb subcommands: `login`, `mcp`, `plugin`, `mcp-server`,
  `app-server`, `sandbox`, `completion`, `update`, `debug`.

**gil**:
- Primary: `gil` (no args) → chat REPL, 100% natural language.
- Mission control: `giltui` (bubbletea, 4-pane: sessions / spec /
  activity-or-tree / memory).
- Headless / CI: `gil <verb> <sessionID>` (`gil status`, `gil run`,
  `gil events`, etc.).
- Daemon: `gild` (always-on background).
- MCP server mode: `gilmcp` (gil-as-MCP-server for other agents).

codex's TUI is much larger (Ratatui fullscreen) and works as a
standalone primary surface. gil splits two surfaces: a single-column
chat REPL (the canonical one) and a multi-pane mission-control TUI
that watches sessions running in the daemon. Both designs are valid;
codex is "TUI-first", gil is "chat-first with optional dashboard."

## 12 · Provider support

**codex** — OpenAI (default), Amazon Bedrock (`/model-provider/src/
amazon_bedrock/`), generic OpenAI-compatible endpoints
(`/models-manager/`). Auth abstraction routes per provider (ChatGPT
OAuth, API key, Bedrock SigV4).

**gil** — Anthropic native (claude-opus / sonnet / haiku 4.x),
OpenAI-compatible adapter covering OpenAI, OpenRouter, vLLM, Ollama.
No Anthropic-via-Bedrock, no Gemini (sole provider key in current
`~/.env`).

codex's Bedrock support is enterprise-relevant. gil's Anthropic-
native path is a real engineering investment (cache_control,
tool-use schema, system prompt format) that gives better behavior
than the OpenAI-compatible adapter when targeting Claude models.

## 13 · Distribution

| | codex | gil v0.2.0 |
|---|---|---|
| Pre-built binaries | GitHub releases (x86_64 / ARM / Windows) | GitHub releases: darwin/linux × amd64/arm64 (4 tar.gz) |
| Linux packages | n/a (npm bundles binary) | .deb + .rpm × amd64/arm64 |
| Homebrew | `brew install --cask codex` | tap not yet created — disabled in goreleaser |
| npm | `@openai/codex` | not provided |
| Source build | `cargo build --release` | `make install` |
| VS Code | (separate IDE plugin) | scaffold in `vscode/` (not yet published) |

codex's distribution is much more mature — npm + brew + binary
across platforms is a hard combination to match. gil ships Linux
.deb/.rpm which codex doesn't. Homebrew is gil's near-term gap (one
tap repo away).

## 14 · Approximate position summary

What codex has that gil doesn't:
- 4× LOC, 119 crates, mature crate boundaries.
- Guardian / approval model with typed risk events + 5-level
  approval enum.
- mature MCP OAuth + HTTP transport + auth elicitation.
- npm + brew + multi-OS distribution.
- Windows sandbox support.
- Amazon Bedrock provider.

What gil has that codex doesn't:
- Verify-loop discipline as a state machine (system-enforced
  `verified` status).
- Subagent delegation (`spawn_agent` / `wait_agent` / `agent_status`).
- Stuck detection (6 patterns) + recovery (4 strategies).
- Cache-preserving compression (Hermes Head/Middle/Tail + Anthropic
  cache_control).
- gRPC daemon-client split (sessions survive client disconnect,
  multi-client tail).
- §2.6 verb tools (working set, checkpoints, export, reset) as
  agent-callable rather than client-side slashes.
- Anthropic-native provider implementation.
- Docker / SSH / Modal / Daytona sandbox backends.
- Linux .deb / .rpm package outputs.
- MCP server mode (`gilmcp`) for being consumed by other agents.

Net read: codex is broader, more polished on distribution and
permissions; gil is more opinionated on discipline (verify) and
delegation (subagent). The architectural bets are different enough
that "which is better" is task-dependent.

## 15 · Open questions for perf comparison

The structural / functional comparison above is the foundation. A
real perf comparison requires actually running both on the same
workload:

- **Same task pool**. SWE-bench Lite (300 tasks) is the canonical
  benchmark. `python/gil_swebench/` already has a harness for gil;
  codex's `--exec` mode is the equivalent.
- **Same provider**. Both gil and codex support OpenAI's chat
  completions. Holding the model constant (e.g. `gpt-4o-mini` or
  `claude-haiku-4-5` via codex's OpenAI-compatible path) is the only
  way to isolate the harness.
- **Metrics**: pass rate, iterations to completion, total tokens
  consumed, total wall time, total cost.

What's blocking this in the current environment:
- No ANTHROPIC_API_KEY or OPENAI_API_KEY in `~/.env` — only
  `GEMINI_API_KEY`, which neither harness supports out of the box.
- cargo / rust toolchain not installed — codex's binary isn't built.

When those two are in place, the perf comparison can be parameterised
on a small task pool (5–10 SWE-bench Lite tasks) and run to
completion in a couple of hours. Until then, the structural
comparison above is the most we can do without simulating outcomes.

## 16 · Execution perf — OSLab qwen3.6-27b (2026-05-12)

We did run a live comparison on the same vLLM endpoint
(`http://113.198.66.44:10010/v1`, model `qwen3.6-27b`, OSLab-hosted).
Both harnesses configured to talk to the same endpoint over OpenAI-
compatible Chat Completions. codex 0.50.0 (`@openai/codex@0.50.0`
npm tarball, last release that still supports `wire_api = "chat"`;
0.125+ dropped it in favor of the Responses API only). gil v0.2.0
(`v0.2.0-2-gca49a78`, freshly `make install`-ed off main).

A bug found in the process: gil's `chat --working-dir` flag was
registered (G4 follow-up) but didn't propagate to the auto-created
session's `working_dir`, so the agent's tools failed with "session
has no working directory." Fixed in the same session: added
`working_dir = 5;` to `PromptRequest`, plumbed through SDK + server
auto-create path. The benchmark below is on the post-fix binary.

### Task pool

Three small tasks, each a clean scratch dir:

- **T1 — Write quicksort from scratch.** Empty Go module dir. Goal:
  create `qsort.go` (`Quicksort([]int) []int`), `qsort_test.go` with
  ≥3 cases (empty / reverse / already-sorted), run `go test`,
  confirm pass. Requires 4–6 tool calls.
- **T2 — Fix an off-by-one bug.** Given `sum.go` with
  `for i := lo; i < hi; i++` (loop should be `<=`) and a failing
  test. Goal: read, fix, re-run, confirm pass. Requires 3–4 tool
  calls.
- **T3 — Explain DFS.** Text-only prompt. "Explain in 2-3 sentences
  what a depth-first search is. No code, just prose." Requires 0
  tool calls.

### Results

| Task | gil v0.2.0 | codex 0.50.0 |
|---|---|---|
| Sanity ("Reply: SANITY_OK") | 2.2 s ✓ | 2.3 s ✓ |
| T1 quicksort (multi-turn tool) | **22.6 s ✓** — `go test` pass | ✗ — 400 mid-stream on 2nd turn |
| T2 off-by-one fix (multi-turn tool) | **9.5 s ✓** — fixed, test pass | ✗ — 400 mid-stream after 1st tool call |
| T3 DFS explain (text-only) | 3.2 s ✓ | 3.2 s ✓ |

### What broke for codex

codex 0.50.0's vllm path consistently fails on **multi-turn tool
flows**. First tool call (e.g. `find *.go`) succeeds; the very next
turn — when codex sends the assistant-message-with-tool_call back to
vllm — vllm replies HTTP 400:

```
{"error":{"message":"Extra data: line 1 column 67 (char 66)",
 "type":"BadRequestError","param":null,"code":400}}
```

Reproduced across two distinct multi-turn tasks (T1, T2), with
retries disabled (`stream_max_retries=0`, `request_max_retries=0`).
Same prompts run text-only (T3) complete cleanly. The bug is
codex's specific tool-call serialisation that vllm-qwen's
chat-completions implementation cannot parse. codex 0.125+ removed
chat-API support entirely (only Responses now), which is the
opposite direction from what'd fix this.

This is itself a comparison datapoint: gil's vllm path is solid
(7 tool calls / 4 verify rounds / 1 plan_steps in T1, all green);
codex's vllm path can only do single-turn or text-only.

### gil tool-call breakdown (T1)

```
plan_steps  →  Initialize Go module + run go test (2 acceptance checks)
run_bash    →  go mod init qsort
write_file  →  qsort.go (632 bytes)
write_file  →  qsort_test.go (1173 bytes)
verify      →  go test -v ./... → exit 0, 575 ms, 5/5 PASS
```

Plan + verify discipline (M5) actually fires — `verified` status
flips because the acceptance check succeeded, not because the agent
asserted success. This is the gil-only state-machine discipline
described in §4 of this doc; in the live run it cost ~600 ms (the
go-test wall time) for full ground-truth on completion.

### Honest caveats

- **Sample size.** Three tasks isn't a benchmark, it's a smoke
  test. Real comparison needs N≥20 SWE-bench Lite tasks across a
  spread of difficulty.
- **Provider asymmetry.** codex hit a vllm tool-call bug that
  isn't fundamental — it'd presumably work against
  `api.openai.com`. gil's Anthropic-native + OpenAI-compatible
  paths are both well-tested. A fair perf comparison needs the
  *same model on a fully-conformant provider* on both sides.
- **Iterations / tokens not measured.** gil emits per-turn metrics
  in its event stream; codex's `--exec` output doesn't break those
  out without parsing the rollout JSON. Both harnesses ran on
  whatever budget the agent decided to spend; nothing imposed a
  cap.

### Takeaway from the live run

For an offline / on-prem / OSS-LLM deployment story, **gil
empirically works on a real vllm endpoint with tool calls; codex
0.50.0 does not**. For text-only conversational use both finish at
parity speed. For the comparison to extend further (token counts,
iterations, pass rates at scale) we need a fully OpenAI-conformant
endpoint codex's vllm path can use — or, equivalently, a future
codex version that re-supports the chat API + a vllm build with
strict OpenAI tool_call format compliance. Neither is in the
current environment.
