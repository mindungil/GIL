# Phase 7 — Edit / Patch / Permission / TUI

> Precision file editing (`core/edit`), apply_patch DSL (`core/patch`), permission glob system (`core/permission`), interactive Bubbletea TUI (`tui/`). Plus Phase 6 deferred items: SubagentBranch stuck strategy, Docker workspace backend.

**Goal**: Move from coarse `write_file` (full overwrite) to surgical SEARCH/REPLACE editing. Add a structured patch DSL so the agent can express multi-file changes atomically. Replace the implicit `risk.autonomy=FULL` allow-all with a proper allow/ask/deny permission system. Ship a real terminal UI so users can supervise long-running runs.

**Architecture**: Each track is independent and additive. `core/edit` and `core/patch` add new tools to the run engine. `core/permission` wraps the existing tool dispatch with a gate. `tui/` is a separate Bubbletea client of the gild gRPC server.

---

## Track A — core/edit (Aider SEARCH/REPLACE 4-tier)

### T1: core/edit.MatchEngine — 4-tier matching algorithm

**Files**: `core/edit/match.go`, `core/edit/match_test.go`

**Reference to lift**: `/home/ubuntu/research/aider/aider/coders/editblock_coder.py` lines 157-340. Functions: `replace_most_similar_chunk` (the 4-tier orchestrator), `replace_part_with_missing_leading_whitespace`, `match_but_for_leading_whitespace`, `replace_closest_edit_distance`. Uses Python's `difflib.SequenceMatcher`; in Go we'll port the same algorithm using a Go-native equivalent (we'll implement a small line-similarity helper based on Levenshtein or use `agnivade/levenshtein` if a quick Go lib is acceptable — preferred: stdlib only, hand-roll SequenceMatcher.ratio).

**Tiers**:
1. Exact match — `strings.Index` of part in whole
2. Whitespace-flexible — strip leading whitespace common prefix on each line, retry exact
3. Trailing-whitespace flexible — strip trailing whitespace, retry
4. Fuzzy via SequenceMatcher.ratio ≥ 0.8 — pick the best chunk

```go
type MatchEngine struct {
    FuzzyThreshold float64  // default 0.8
}
type Match struct {
    Tier      int  // 1..4
    StartLine int
    EndLine   int
}
func (m *MatchEngine) Find(whole, part string) (*Match, bool)
func (m *MatchEngine) Replace(whole, part, replace string) (string, *Match, error)
```

Tests cover each tier with intentional whitespace/indent perturbations + a no-match case.

Commit: `feat(core/edit): SEARCH/REPLACE 4-tier matching (Aider lift)`

### T2: core/edit DSL parser — find_original_update_blocks port

**Files**: `core/edit/parser.go`, `core/edit/parser_test.go`

**Reference to lift**: aider editblock_coder.py:439 `find_original_update_blocks` — parses the LLM's textual diff format:
```
path/to/file.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>> REPLACE
```

Returns `[]Block{File, Search, Replace}`. Handles fenced code blocks (```...```), filename detection, and malformed-block error messages with line numbers.

Commit: `feat(core/edit): DSL parser for SEARCH/REPLACE blocks`

### T3: edit tool — applies parsed blocks via MatchEngine

**Files**: `core/tool/edit.go`, `core/tool/edit_test.go`

```go
type Edit struct { WorkingDir string; Engine *edit.MatchEngine }
// Tool name: "edit"
// Args: { "blocks": "<dsl-text>" }
// Applies all blocks; reports per-block result. On any miss, the whole call
// is reported but earlier files may be partially written (workspace
// mutation atomicity is Phase 8 if needed).
```

Surface a `find_similar_lines` hint (port from editblock_coder.py:602) when a block fails to match — paste the closest 6-line chunk so the LLM sees what's actually in the file.

RunService.executeRun adds this to the tool slice.

Commit: `feat(core/tool): edit tool wraps MatchEngine + DSL parser`

---

## Track B — core/patch (Codex apply_patch DSL)

### T4: core/patch parser — port Codex apply-patch parser

**Files**: `core/patch/parser.go`, `core/patch/parser_test.go`

**Reference to lift**: `/home/ubuntu/research/codex/codex-rs/apply-patch/src/parser.rs` (1108 lines — substantial port). The Codex DSL is structured:
```
*** Begin Patch
*** Update File: path/to/file.go
@@ context
- old line
+ new line
*** End Patch
```
Plus `*** Add File:`, `*** Delete File:`, `*** Move File:` directives.

Port the parser as `core/patch.Parse(input string) (*Patch, error)` returning a typed AST:
```go
type Patch struct { Ops []Op }
type Op interface { isOp() }
type AddFile struct { Path string; Content string }
type DeleteFile struct { Path string }
type UpdateFile struct { Path string; Hunks []Hunk }
type Hunk struct { ContextBefore, RemovedLines, AddedLines, ContextAfter []string }
type MoveFile struct { OldPath, NewPath string }
```

This is the largest task in Phase 7 — likely 600-800 lines of Go.

Commit: `feat(core/patch): apply_patch DSL parser (Codex lift)`

### T5: core/patch applier

**Files**: `core/patch/apply.go`, `core/patch/apply_test.go`

**Reference**: Codex `apply-patch/src/lib.rs` — applies each Op against the filesystem. Resolves paths relative to a workspace root, reads existing files, applies hunks via context-anchored matching, writes results.

```go
type Applier struct { WorkspaceDir string; DryRun bool }
type Result struct { Op Op; Applied bool; Err error }
func (a *Applier) Apply(p *Patch) []Result
```

Hunk application uses an exact-context match anchor (Codex doesn't fuzz; if the context doesn't match, the hunk fails). For our use, exact match is fine — fuzzing is `core/edit`'s job.

Commit: `feat(core/patch): applier with context-anchored hunk match`

### T6: apply_patch tool

**Files**: `core/tool/applypatch.go`, `core/tool/applypatch_test.go`

```go
type ApplyPatch struct { WorkspaceDir string }
// Tool name: "apply_patch"
// Args: { "patch": "<dsl-text>" }
```

Returns per-op success/failure; aggregate IsError if any op failed.

Commit: `feat(core/tool): apply_patch tool`

---

## Track C — core/permission (allow/ask/deny + glob)

### T7: core/permission.Evaluator — findLast + glob match

**Files**: `core/permission/evaluator.go`, `core/permission/evaluator_test.go`

**Reference to lift**: 
- `/home/ubuntu/research/opencode/packages/opencode/src/permission/evaluate.ts` (15 lines — small, lift wholesale)
- `/home/ubuntu/research/cline/cli/src/agent/permissionHandler.ts` for the auto-approve bucket pattern

```go
type Decision int
const (
    DecisionAllow Decision = iota
    DecisionAsk
    DecisionDeny
)

type Rule struct {
    Pattern  string  // glob; may match tool name OR tool name + arg pattern
    Decision Decision
}

type Evaluator struct {
    Rules []Rule  // last-matching wins (OpenCode pattern)
}

// Evaluate returns the first decision that matches when scanned in REVERSE
// order (last-wins). If no rule matches, returns DecisionAsk.
func (e *Evaluator) Evaluate(toolName string, key string) Decision
```

Pattern syntax:
- `bash` → matches tool name "bash" with any key
- `bash:rm *` → matches tool name "bash" AND key matches glob "rm *"
- `*` → matches everything

`key` is tool-specific extracted detail (for bash: the command; for write_file: the path).

Tests cover: last-wins semantics, glob matching, empty rules → Ask.

Commit: `feat(core/permission): Evaluator with last-wins + glob (OpenCode lift)`

### T8: AgentLoop integrates permission gate

**Files**: modify `core/runner/runner.go`

Add `Permission *permission.Evaluator` field. Before dispatching each tool call:
1. Extract a key from the tool args (define a tool-specific `KeyFor(args) string` interface or a switch on tool name)
2. Call `Permission.Evaluate(tool.Name(), key)`
3. If Allow → dispatch as before
4. If Deny → emit `permission_denied` event + return tool_result with IsError + content "permission denied"
5. If Ask → for now (no human in the loop in Phase 7), default to Deny with a hint: "no operator available; defaulting to deny" (interactive Ask is Phase 8 with TUI integration)

For the interview phase + spec.risk.autonomy field: when autonomy=FULL, no permission gate; when autonomy=ASK or RESTRICTED, gate is enforced. spec.risk.autonomy already exists in the proto.

Commit: `feat(core/runner): permission gate before tool dispatch`

### T9: Spec → permission rules

**Files**: `core/permission/from_spec.go`

When a frozen spec has `risk.autonomy != FULL`, build an Evaluator from spec policy. Phase 7 keeps this simple: read a list of rule strings from a future spec field `risk.permissions []string` (add to proto). For now, hardcode a sensible default policy when autonomy=RESTRICTED: deny all bash, allow read_file/write_file/repomap/memory_*.

Commit: `feat(core/permission): build Evaluator from spec.risk.autonomy`

---

## Track D — TUI (Bubbletea)

### T10: tui module bootstrap + Bubbletea root model

**Files**: `tui/cmd/giltui/main.go`, `tui/internal/app/model.go`, `tui/internal/app/view.go`

**Reference**: charmbracelet/bubbletea documentation patterns. No specific harness lift — Bubbletea idioms are well-documented.

Add `github.com/charmbracelet/bubbletea` to `tui/go.mod`. Three-pane layout:
- Left: session list (gild ListSessions)
- Center: active session detail (status, iteration, tokens, last events)
- Right: input box for sending control commands

Commit: `feat(tui): Bubbletea root model + three-pane layout`

### T11: Live event stream subscription

**Files**: `tui/internal/event/subscriber.go`

Use the existing SDK `TailRun(sessionID)` to subscribe; pump events into a Bubbletea Cmd channel. Ring buffer (last 200 events) for the session detail pane.

Commit: `feat(tui): live event tail in session detail pane`

### T12: Permission-Ask dialog

**Files**: `tui/internal/app/permission.go`

When the server emits a `permission_ask` event (Phase 7+ feature), the TUI surfaces an inline yes/no prompt. User's response goes back via a new `RunService.AnswerPermission(session_id, allow)` RPC.

Add the RPC to `proto/gil/v1/run.proto` and a server-side handler in `service/run.go`.

Commit: `feat(tui+server): permission_ask dialog + AnswerPermission RPC`

---

## Track E — Phase 6 deferred + integration

### T13: Stuck Recovery SubagentBranch (real impl)

**Files**: modify `core/stuck/recovery.go`

**Reference**: nothing direct in the references — this is a gil-original. Pattern: when stuck, dispatch a fresh sub-AgentLoop with the SAME spec but a RESTRICTED tool subset and a derived "subgoal" message. The sub-loop runs to a small max-iter cap (5); if it succeeds, parent absorbs the result; if it fails, parent gives up on this strategy.

For Phase 7 simplicity: the sub-loop shares the parent's Provider but gets a fresh empty event stream and no Memory/Checkpoint. The "subgoal" is a hardcoded "Re-examine the project structure with repomap and read_file. Do not call write_file or bash. Report your findings."

```go
type SubagentBranchStrategy struct {
    SubLoopMaxIter int  // default 5
}
```

Tests use a mock provider that returns a quick "I see X, recommend Y" answer.

Commit: `feat(core/stuck): SubagentBranchStrategy real impl`

### T14: Docker workspace backend

**Files**: `runtime/docker/backend.go`, `runtime/docker/backend_test.go`

When `spec.workspace.backend == DOCKER`, RunService creates a container, mounts the workspace, and routes tool exec into it. For Phase 7: minimal — `docker run` per command (no persistent container yet; that's Phase 8). Bash tool's CommandWrapper interface is satisfied by a `DockerWrapper` that wraps with `docker exec`.

Update `server/internal/service/run.go` `buildTools` DOCKER branch to use this wrapper instead of returning "not yet supported".

Reference: Codex `docker-sandbox` if it exists; otherwise straight Docker CLI.

Commit: `feat(runtime/docker): Docker workspace backend (per-command exec)`

### T15: e2e7 — edit + patch + permission sanity

**Files**: `tests/e2e/phase07_test.sh`, Makefile e2e7 + e2e-all

Mock provider scripts: edit a file via SEARCH/REPLACE, then apply_patch a Hunk, then attempt a denied bash command (permission gate fires). Verify all three outcomes.

Commit: `test(e2e): phase 7 — edit + patch + permission sanity`

### T16: progress.md Phase 7 update

Mark all complete + outcomes summary.

Commit: `docs(progress): Phase 7 complete`

---

## Phase 7 완료 체크리스트

- [ ] `make e2e-all` 7 phase 통과
- [ ] core/edit: 4-tier 매칭 + DSL parser + edit tool
- [ ] core/patch: apply_patch parser + applier + tool
- [ ] core/permission: Evaluator + AgentLoop gate + spec-driven rules
- [ ] TUI: Bubbletea three-pane + live tail + permission ask dialog
- [ ] SubagentBranch stuck strategy 작동
- [ ] DOCKER workspace backend 작동

## Phase 8 미루는 항목

- core/exec UDS RPC 다단계
- 워크스페이스 mutation atomicity (transactional rollback for partial patches)
- SSH workspace backend
- Multi-user gild
- HTTP/JSON gateway (browser clients)
- VS Code 확장
