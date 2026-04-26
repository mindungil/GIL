# Phase 6 — Context / Memory / Repomap

> Cache-preserving compression (`core/compact`), 6-file memory bank (`core/memory`), and tree-sitter+PageRank repomap (`core/repomap`). Plus Phase 5 deferred items: 4 stuck-recovery strategies and macOS Seatbelt.

**Goal**: Make multi-day, 100M-token autonomous runs sustainable. Compaction keeps cost down (cache hits), memory bank preserves state across compactions, repomap gives the agent a map of the codebase without flooding context.

**Architecture**:
- `core/compact` — Compactor with Head/Middle/Tail invariant (Hermes pattern), OpenCode summary template, anti-thrashing tracker, Anthropic system-and-3 cache_control placement.
- `core/memory` — Bank type managing 6 markdown files at `<sessionDir>/memory/`, `memory_update`/`memory_load` tools, post-verify-pass milestone gate.
- `core/repomap` — tree-sitter (smacker/go-tree-sitter) symbol extraction (Go/Python/JS/TS), PageRank scoring, binary-search token fitting, `repomap` tool.
- Stuck recovery: AltToolOrder, ResetSection, AdversaryConsult (SubagentBranch deferred — needs sub-engine API).
- Sandbox: macOS Seatbelt (sb-exec wrapper).

---

## Track A — core/compact (cache-preserving compression)

### T1: core/compact.Compactor — Head/Middle/Tail split + LLM summary

**Files**:
- Create: `core/compact/compactor.go`
- Create: `core/compact/compactor_test.go`

```go
package compact

type Result struct {
    OriginalCount  int       // # messages before
    CompactedCount int       // # messages after
    SavedTokens    int64     // estimated saved tokens
    SummaryMarkdown string   // the inserted summary
}

type Compactor struct {
    Provider provider.Provider
    Model    string
    HeadKeep int      // first N messages kept verbatim (default 2)
    TailKeep int      // last N messages kept verbatim (default 6)
    MinMiddle int     // skip compact if middle has fewer than this (default 8)
}

// Compact returns a NEW message slice. The original is not mutated (deep-copy semantics).
// Layout: [head...] + [synthetic-summary-message] + [tail...].
// Returns Result describing what changed; if Compact decides not to compact (middle too small),
// returns the original slice and OriginalCount==CompactedCount.
func (c *Compactor) Compact(ctx context.Context, msgs []provider.Message) ([]provider.Message, Result, error)
```

Implementation:
- If `len(msgs) - HeadKeep - TailKeep < MinMiddle` → return msgs unchanged.
- Build summary prompt asking model to produce OpenCode markdown (Goal / Constraints / Progress with Done/InProgress/Blocked subsections).
- Insert as a single user message with role=User containing the markdown.
- Estimate token savings: sum content lengths of removed middle, multiply by 0.25.

Tests:
- Compact_NoOp_WhenMiddleTooSmall
- Compact_PreservesHeadAndTail_Verbatim (deep equality on first N + last M)
- Compact_OriginalSliceUnmutated
- Compact_InsertedSummaryIsMarkdown
- Compact_ReturnsTokenSavings

Commit: `feat(core/compact): Compactor with Head/Tail preserve + LLM summary`

### T2: SummaryTemplate — OpenCode markdown structure

**Files**:
- Create: `core/compact/template.go`
- Create: `core/compact/template_test.go`

Extract the prompt-building from T1 into a `BuildSummaryPrompt(msgs []provider.Message) string` helper that produces a deterministic prompt:

```
You are summarizing the middle of a long conversation so the assistant can continue without context loss.

Produce ONLY this exact markdown structure (no preamble, no commentary):

## Goal
- <single-sentence task summary inferred from messages>

## Constraints & Preferences
- ...

## Progress
### Done
- ...
### In Progress
- ...
### Blocked
- ...

Conversation to summarize:
<concatenated messages>
```

Tests verify the prompt contains required headings and ends with the conversation block.

Commit: `feat(core/compact): structured summary template (OpenCode pattern)`

### T3: Anti-thrashing tracker (skip if 2 prior compacts saved <10% each)

**Files**:
- Modify: `core/compact/compactor.go` — add `History` field
- Add: `core/compact/history.go` + test

```go
type CompactionEvent struct {
    OriginalTokens int64
    SavedTokens    int64
    Timestamp      time.Time
}
type History struct {
    Recent []CompactionEvent  // last 5
}

// ShouldSkip returns true if anti-thrashing predicts low value.
// Skips when the last 2 events both saved <10% of original.
func (h *History) ShouldSkip() bool

// Record appends an event, trimming to last 5.
func (h *History) Record(e CompactionEvent)
```

Compactor.Compact consults History at the top: if `c.History != nil && c.History.ShouldSkip()`, return msgs unchanged with a Result reflecting the skip.

Tests:
- ShouldSkip_FalseWhenLessThanTwoEvents
- ShouldSkip_FalseWhenLastTwoSavedOver10Pct
- ShouldSkip_TrueWhenLastTwoBothUnder10Pct
- Record_TrimsToFive

Commit: `feat(core/compact): anti-thrashing history tracker`

### T4: Cache control placement (Anthropic system-and-3)

**Files**:
- Create: `core/compact/cache.go`
- Create: `core/compact/cache_test.go`

```go
// MarkCacheBreakpoints assigns Anthropic cache_control markers per the
// system-and-3 strategy: system message + last 3 non-system messages.
// Sets msg.CacheControl = true on selected messages. Returns the modified slice
// (mutates in place — callers should pass a copy if originals must be preserved).
func MarkCacheBreakpoints(msgs []provider.Message) []provider.Message
```

Need to add `CacheControl bool` field to `provider.Message` (and propagate to anthropic adapter so it sends `cache_control: {type: "ephemeral"}` on the marked block).

Tests:
- MarkCacheBreakpoints_SystemPlusLastThree
- MarkCacheBreakpoints_FewerThanFourMessages
- MarkCacheBreakpoints_NoSystemMessage

Anthropic adapter test: verify the JSON request body has cache_control on the right blocks (use httptest.Server).

Commit: `feat(core/compact): Anthropic cache_control marker placement`

### T5: AgentLoop integrates Compactor + 95% safety net + agent tool

**Files**:
- Modify: `core/runner/runner.go` — add Compactor field, check at iteration start, expose `compact_now` tool
- Create: `core/tool/compact.go` — agent-callable tool that signals "compact next iteration"
- Modify: `core/runner/runner_test.go` — test

AgentLoop:
- Add `Compactor *compact.Compactor` field; nil = no compaction.
- Add `MaxContextTokens int` field; default 200_000 if zero.
- At each iteration start, estimate current token count (sum of message content lengths / 4 as rough estimate). If `> 0.95 * MaxContextTokens` → run Compactor.Compact; replace messages; emit `compaction_done` event.
- The agent tool `compact_now` sets a flag that triggers compaction at the start of the NEXT iteration.

Tests:
- AgentLoop_AutoCompactsAt95Pct (mock provider that returns very long responses → trigger)
- AgentLoop_NoCompactWhenCompactorNil
- AgentLoop_compact_now_ToolTriggers

Commit: `feat(core/runner): AgentLoop auto-compacts at 95% + compact_now tool`

---

## Track B — core/memory (6-file markdown bank)

### T6: core/memory.Bank — 6 files + read/write/init

**Files**:
- Create: `core/memory/bank.go`
- Create: `core/memory/bank_test.go`

```go
package memory

const (
    FileProjectBrief    = "projectbrief.md"
    FileProductContext  = "productContext.md"
    FileActiveContext   = "activeContext.md"
    FileSystemPatterns  = "systemPatterns.md"
    FileTechContext     = "techContext.md"
    FileProgress        = "progress.md"
)

var AllFiles = []string{FileProjectBrief, FileProductContext, FileActiveContext, FileSystemPatterns, FileTechContext, FileProgress}

type Bank struct {
    Dir string  // typically <sessionDir>/memory
}

func New(dir string) *Bank

// Init creates the directory and writes initial empty stubs (with H1 + placeholder).
// Idempotent — existing files are NOT overwritten.
func (b *Bank) Init() error

// InitFromSpec populates files from a frozen spec. Used right after freeze.
// Overwrites only files that are still at their stub content (untouched by agent).
func (b *Bank) InitFromSpec(spec *gilv1.FrozenSpec) error

// Read returns file contents; returns ErrNotFound for unknown files or missing files.
func (b *Bank) Read(file string) (string, error)

// Write replaces the entire file contents.
func (b *Bank) Write(file, content string) error

// Append appends to a file (creates with content if missing).
func (b *Bank) Append(file, content string) error

// AppendSection appends under a markdown heading "## <section>". If the heading
// doesn't exist, it's added at end. Used by the memory_update tool's section mode.
func (b *Bank) AppendSection(file, section, content string) error

// Snapshot returns a map of filename → contents for prompt prepending.
func (b *Bank) Snapshot() (map[string]string, error)

// EstimateTokens returns rough total token count of all files.
func (b *Bank) EstimateTokens() (int, error)
```

Tests cover all methods + edge cases (missing files, overwrite-only-stubs in InitFromSpec, AppendSection creating new heading).

Commit: `feat(core/memory): Bank type managing 6 markdown files`

### T7: memory_update + memory_load tools

**Files**:
- Create: `core/tool/memory.go` — both tools
- Create: `core/tool/memory_test.go`

```go
type MemoryUpdate struct {
    Bank *memory.Bank
}
// Tool name: "memory_update"
// Args: { "file": "progress", "section": "Done" (optional), "content": "...", "replace": false (optional) }
// - file: short name without .md (auto-suffixed). One of the 6 known names.
// - section: when provided, AppendSection(file, section, content)
// - replace=true: full rewrite
// - replace=false (default), no section: Append(file, content)

type MemoryLoad struct {
    Bank *memory.Bank
}
// Tool name: "memory_load"
// Args: { "file": "techContext" } → returns the full file content as the tool result.
```

Both tools validate that `file` is one of `memory.AllFiles` (or short name resolves to one). Reject unknown files with IsError.

Tests verify both happy paths + invalid file name + replace=true behavior.

Commit: `feat(core/tool): memory_update + memory_load tools`

### T8: AgentLoop prepends bank snapshot to system prompt

**Files**:
- Modify: `core/runner/runner.go` — system prompt builder includes bank snapshot
- Modify: `core/runner/runner_test.go`

In `buildSystemPrompt`, after the existing content, append:
```
## Memory Bank

The following files in <sessionDir>/memory/ track persistent state:

### projectbrief.md
<content or "(empty)">

### productContext.md
<content>

...
```

Behavior:
- AgentLoop gets a `Memory *memory.Bank` field. Nil = no prepend.
- When Bank.EstimateTokens() <= 4000, prepend ALL 6 files.
- Else, prepend ONLY progress.md and tell the agent: "Other memory files available via memory_load tool."

Tests:
- AgentLoop_PrependsAllBankFiles_WhenSmall
- AgentLoop_PrependsOnlyProgress_WhenLarge

Commit: `feat(core/runner): system prompt prepends memory bank`

### T9: Post-verify-pass memory milestone gate

**Files**:
- Modify: `core/runner/runner.go` — after `allPass` true, before `done` return, run a single milestone audit turn

When verifier passes (all checks green) and Bank is non-nil:
- Emit a single user message: "Verification passed. Before declaring done, update the memory bank if needed (use memory_update). Reply with 'updated' or 'no update'."
- Run one provider turn with tools enabled.
- If the agent calls memory_update, dispatch it normally. Then emit `memory_milestone_done` event and proceed to done.
- If not, just proceed to done.

Tests:
- AgentLoop_MilestoneGate_AgentCallsMemoryUpdate
- AgentLoop_MilestoneGate_AgentSkips
- AgentLoop_MilestoneGate_SkippedWhenBankNil

Commit: `feat(core/runner): post-verify milestone memory update gate`

---

## Track C — core/repomap (tree-sitter + PageRank)

### T10: core/repomap package + tree-sitter Go binding

**Files**:
- Modify: `core/go.mod` — add `github.com/smacker/go-tree-sitter` + grammars
- Create: `core/repomap/parser.go`
- Create: `core/repomap/parser_test.go`

```go
package repomap

type Symbol struct {
    Name    string
    Kind    string  // "func" | "struct" | "interface" | "method" | "class" | "var"
    File    string
    Line    int
    EndLine int
}

type Reference struct {
    Name string
    File string
    Line int
}

type FileSymbols struct {
    File       string
    Defs       []Symbol
    Refs       []Reference
}

// ParseFile loads a single file and returns its symbols.
// Supported languages by file extension: .go, .py, .js, .ts, .jsx, .tsx.
// Unsupported extensions return (nil, ErrUnsupportedLanguage).
func ParseFile(path string) (*FileSymbols, error)
```

Use language-specific tree-sitter queries. Supply a query string per language extracting function/struct/interface/class definitions and identifier references.

Tests use small Go + Python fixture files in `core/repomap/testdata/`.

Commit: `feat(core/repomap): tree-sitter symbol extraction (go/py/js/ts)`

### T11: Project walker — collects all FileSymbols under a root

**Files**:
- Create: `core/repomap/walker.go`
- Create: `core/repomap/walker_test.go`

```go
// WalkProject visits all files under root, parses each supported file,
// and returns the aggregated symbols. Skips .git/, vendor/, node_modules/,
// __pycache__/, build/, dist/.
func WalkProject(ctx context.Context, root string, opts WalkOptions) ([]*FileSymbols, error)

type WalkOptions struct {
    MaxFileSize int64  // skip files larger than this (default 256KB)
    Exclude     []string  // additional glob patterns
}
```

Tests with a small fixture project containing Go + Python files.

Commit: `feat(core/repomap): project walker with sensible defaults`

### T12: PageRank scoring over the def-ref graph

**Files**:
- Create: `core/repomap/pagerank.go`
- Create: `core/repomap/pagerank_test.go`

```go
// Rank computes a PageRank-style score for each symbol. The graph has:
//  - Nodes: every (file, symbol-name) pair from FileSymbols.Defs
//  - Edges: for every Reference whose Name matches a definition, add an edge
//    from the referencing file's symbols to the defined symbol.
// Returns a sorted slice (highest score first).
func Rank(symbols []*FileSymbols) []ScoredSymbol

type ScoredSymbol struct {
    Symbol Symbol
    Score  float64
    InDegree int
}
```

Use 30 iterations, damping=0.85. Tolerate disconnected nodes.

Tests:
- Rank_HigherScoreForReferencedSymbols (a fn called from 5 places ranks above one called from 1)
- Rank_SortedDescending
- Rank_StableForSameInput (deterministic seed)

Commit: `feat(core/repomap): PageRank scoring`

### T13: Token-budget binary-search fitter

**Files**:
- Create: `core/repomap/fit.go`
- Create: `core/repomap/fit_test.go`

```go
// Fit produces the best repomap markdown that fits within maxTokens.
// Strategy: binary-search the cut point in the ranked symbol list,
// rendering the top-K and measuring estimated tokens, finding the largest K
// that fits. Render format: per-file collapsed sections with symbol signatures.
func Fit(ranked []ScoredSymbol, maxTokens int) string

// EstimateTokens is a lightweight tokenizer-free estimator: 1 token ≈ 4 chars.
func EstimateTokens(s string) int
```

Tests verify monotonicity and that Fit's output is <= maxTokens estimated.

Commit: `feat(core/repomap): binary-search token budget fitter`

### T14: `repomap` tool exposed to the agent

**Files**:
- Create: `core/tool/repomap.go`
- Create: `core/tool/repomap_test.go`

```go
type Repomap struct {
    Root string  // workspace root
    MaxTokens int  // default 4096
}
// Tool name: "repomap"
// Args: { "max_tokens": 4096 (optional) }
// Returns: a markdown repomap (top-ranked symbols across the project, fitted to max_tokens).
```

Caches results per (root, max_tokens) pair for 60s to avoid re-parsing on rapid calls.

RunService wires `Repomap{Root: workspaceDir}` into the tool list.

Tests with a small fixture project.

Commit: `feat(core/tool): repomap tool with TTL cache`

---

## Track D — Phase 5 deferred + integration

### T15: Stuck Recovery — AltToolOrder

**Files**:
- Modify: `core/stuck/recovery.go` — replace the stub with a real implementation

```go
type AltToolOrderStrategy struct{}
// Apply returns an ActionAltToolOrder Decision with a hint string the
// AgentLoop appends to the next system prompt:
//   "RECENT STUCK: avoid the tool sequence that just looped. Try a different tool first."
// Pure planning — no state mutation.
```

Add `ActionAltToolOrder` handling in AgentLoop: when the strategy returns this action, set a one-iteration `extraSystemNote` that gets appended to system prompt for the NEXT iteration only.

Tests for both the strategy and the loop integration.

Commit: `feat(core/stuck): AltToolOrderStrategy real impl + loop integration`

### T16: Stuck Recovery — ResetSection (uses ShadowGit)

**Files**:
- Modify: `core/stuck/recovery.go` — ResetSectionStrategy real impl
- Modify: `core/runner/runner.go` — handle ActionResetSection

```go
type ResetSectionStrategy struct{}
// Apply returns Decision{Action: ActionResetSection} when stuck pattern is
// RepeatedActionError or RepeatedActionObservation.
```

In AgentLoop, when ActionResetSection received and `a.Checkpoint != nil`:
- List checkpoints via `a.Checkpoint.ListCommits()`.
- Restore to the second-newest (drop the last "iter N").
- Emit `stuck_reset_section` event with the SHA.
- Continue from next iteration.

If Checkpoint is nil, the strategy returns `ErrNoFallback`.

Tests with mocked checkpoint sequence.

Commit: `feat(core/stuck): ResetSectionStrategy uses shadow git rollback`

### T17: Stuck Recovery — AdversaryConsult

**Files**:
- Modify: `core/stuck/recovery.go` — AdversaryConsultStrategy real impl
- Modify: `core/runner/runner.go` — when receiving ActionAdversaryConsult, run a one-shot adversary turn

Strategy needs Provider + Model on the request struct. Extend `ApplyRequest`:
```go
type ApplyRequest struct {
    Signal       Signal
    CurrentModel string
    ModelChain   []string
    Iteration    int
    Provider     provider.Provider  // NEW
    AdversaryModel string            // NEW
    RecentMessages []provider.Message  // NEW (last 10)
}
```

AdversaryConsultStrategy.Apply makes a single Provider.Complete call asking: "The agent is stuck on pattern X. Suggest one concrete next step." Returns Decision{Action: ActionAdversaryConsult, Explanation: <suggestion>}.

AgentLoop handler appends Decision.Explanation as a user note for the next turn.

Tests with mock provider.

Commit: `feat(core/stuck): AdversaryConsultStrategy real impl`

### T18: macOS Seatbelt sandbox

**Files**:
- Create: `runtime/local/seatbelt.go`
- Create: `runtime/local/seatbelt_test.go`

Mirror bwrap.go API but emit `sandbox-exec -p <profile> -- <cmd>` arguments.

Profile per Mode:
- ReadOnly: deny default; allow read; allow file-read* on /; allow file-read*/file-write* on /tmp.
- WorkspaceWrite: same but allow file-write* on workspace.
- FullAccess: pass-through.

`Available()` returns true on Darwin only when `sandbox-exec` is in PATH.

Use a build tag `//go:build darwin` so it compiles only on macOS. Tests use `t.Skip` on Linux.

Commit: `feat(runtime/local): macOS Seatbelt sandbox wrapper`

### T19: e2e6 — full memory + compact + repomap exercise

**Files**:
- Create: `tests/e2e/phase06_test.sh`
- Modify: `Makefile` — e2e6 + e2e-all

Script:
- Spin up gild with mock provider scripted to:
  1. Call repomap tool → assert tool returned markdown
  2. Call memory_update file=progress section=Done content="step 1 done"
  3. Write hello.txt
  4. End turn
- Verify hello.txt created
- Verify `<sessionDir>/memory/progress.md` contains "step 1 done"
- Verify run completed status=done

Mock provider needs a new mode `GIL_MOCK_MODE=run-memory-repomap` returning the scripted turns.

Commit: `test(e2e): phase 6 — memory + compact + repomap sanity`

### T20: progress.md Phase 6 update

Mark all T1-T19 done. Update outcomes summary. Set current phase line.

Commit: `docs(progress): Phase 6 complete`

---

## Phase 6 완료 체크리스트

- [ ] `make e2e-all` 6 phase 통과
- [ ] core/compact: 95% 자동 트리거 + agent tool + anti-thrashing 동작
- [ ] memory bank 6 files: init/read/write + 2 tools + 시스템 프롬프트 prepend + 마일스톤 gate
- [ ] repomap tool: tree-sitter + PageRank + 토큰 fit, agent에게 노출
- [ ] Stuck recovery 4종 모두 실작동 (ModelEscalate + AltToolOrder + ResetSection + AdversaryConsult)
- [ ] macOS Seatbelt 컴파일됨 (Darwin only)

## Phase 7 미루는 항목

- SubagentBranch (sub-engine API 필요)
- core/edit SEARCH/REPLACE 4단 매칭
- core/patch apply_patch DSL
- core/exec UDS RPC 다단계
- core/permission glob
- TUI (Bubbletea)
- DOCKER/SSH workspace backends
