# Phase 27 — Context Wiring Repair — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire up gil's existing-but-uninvoked context-management infrastructure (Compactor, MarkCacheBreakpoints, per-model context window, per-role context budget, grace call on budget exhaust, provider-aware char multiplier) so that the rich code in `core/compact/` and per-model awareness actually run on every production mission.

**Architecture:** Six surgical server-side fixes, no proto changes, no new deps in V1. Each fix is small and independently testable. Tasks are ordered so foundation pieces (model registry, char multiplier) land first; integration (Compactor factory, mainline call sites) builds on them.

**Tech Stack:** Go 1.22+, gRPC, existing test infrastructure. No new dependencies.

**Spec:** `docs/plans/phase-27-context-wiring-repair.md`

---

## File Structure

**New (V1)**
- `core/provider/capacity.go` — per-model context window table
- `core/provider/capacity_test.go`
- `core/provider/tokenest.go` — provider-aware char-multiplier helper
- `core/provider/tokenest_test.go`
- `core/runner/factory.go` — Compactor factory + per-spec assembly
- `core/runner/factory_test.go`
- `core/runner/grace.go` — Hermes-style grace-call wrap-up
- `core/runner/grace_test.go`
- `tests/integration/p27_context_wiring_test.go`

**Modified**
- `core/runner/runner.go` — call sites for cache marker, per-role context check, grace call hook, char-multiplier indirection
- `server/internal/service/run.go` — invoke Compactor factory, assign `loop.Compactor`
- `core/compact/compactor.go` — char-multiplier indirection (use new helper)

**Untouched**
- `proto/`, `sdk/`, `cli/`, `tui/`, `core/memory/`
- `core/compact/cache.go` (logic correct, just needs to be called)

---

## Task 0: Audit Refresh (no code, no commit)

**Purpose:** Verify the file:line citations from the design audit are still accurate before writing code against them.

- [ ] **Step 1: Confirm Compactor wiring gap**

Run:
```bash
cd /home/ubuntu/gil
grep -n 'Compactor' server/internal/service/run.go
grep -rn 'NewCompactor\|loop\.Compactor\s*=' server/ core/runner/
```
Expected: NO matches in production code paths (server/, runner.go non-test). Matches in `core/compact/compactor_test.go`, `core/runner/runner_test.go` are fine. If you find an assignment outside tests, the gap is partially closed — adjust Task 3 accordingly.

- [ ] **Step 2: Confirm MarkCacheBreakpoints gap**

Run:
```bash
cd /home/ubuntu/gil
grep -rn 'MarkCacheBreakpoints' server/ core/runner/ core/provider/
```
Expected: NO matches. If matches exist, Task 4 may be a no-op or different in shape.

- [ ] **Step 3: Confirm 200k hardcode**

Run:
```bash
cd /home/ubuntu/gil
grep -n 'MaxContextTokens\|200_000\|200000' core/runner/runner.go
```
Capture the line where the default is set. This becomes the call site that Task 1 replaces.

- [ ] **Step 4: Confirm char-multiplier site**

Run:
```bash
cd /home/ubuntu/gil
grep -n 'estimateMessagesTokens\|/\s*4\b\|len(.*) / 4' core/runner/runner.go core/compact/compactor.go
```
Expected: two sites (runner.go and compactor.go) doing the 4-char division. These are the targets for Task 2.

- [ ] **Step 5: Resolve any drift**

If the codebase has shifted since the audit (e.g., line numbers different, a function renamed), update this plan inline before starting Task 1. Specifically check:
- Compactor's exact constructor signature (`NewCompactor(...)` vs `New(...)` vs builder)
- Runner's request-construction helper (where `provider.Complete(ctx, req)` is called)
- Spec model field names (`Models.Weak`, `Models.Main` per Phase 19)

No commit at the end of Task 0 — this is investigation only.

---

## Task 1: Per-Model Context Window Registry (Fix 3)

**Files:**
- Create: `core/provider/capacity.go`
- Create: `core/provider/capacity_test.go`

- [ ] **Step 1: Write failing tests**

Create `core/provider/capacity_test.go`:
```go
package provider

import (
    "testing"

    "github.com/stretchr/testify/require"
)

func TestContextTokens_KnownAnthropic(t *testing.T) {
    require.Equal(t, int64(1_000_000), ContextTokens("claude-opus-4-7"))
    require.Equal(t, int64(1_000_000), ContextTokens("claude-opus-4-7[1m]"))
    require.Equal(t, int64(200_000), ContextTokens("claude-sonnet-4-6"))
    require.Equal(t, int64(200_000), ContextTokens("claude-haiku-4-5-20251001"))
}

func TestContextTokens_KnownOpenAI(t *testing.T) {
    require.Equal(t, int64(128_000), ContextTokens("gpt-4o"))
    require.Equal(t, int64(128_000), ContextTokens("gpt-4o-mini"))
    require.Equal(t, int64(400_000), ContextTokens("gpt-5"))
}

func TestContextTokens_KnownGoogle(t *testing.T) {
    require.Equal(t, int64(1_000_000), ContextTokens("gemini-2-pro"))
    require.Equal(t, int64(1_000_000), ContextTokens("gemini-1.5-flash"))
}

func TestContextTokens_KnownOllama(t *testing.T) {
    require.Equal(t, int64(8_192), ContextTokens("ollama:llama3:8b"))
    require.Equal(t, int64(32_768), ContextTokens("ollama:qwen3-coder:32b"))
}

func TestContextTokens_UnknownReturnsConservativeDefault(t *testing.T) {
    require.Equal(t, int64(200_000), ContextTokens("future-model-v9"))
    require.Equal(t, int64(200_000), ContextTokens(""))
}
```

- [ ] **Step 2: Run (expect fail — symbol undefined)**

Run: `cd /home/ubuntu/gil && go test ./core/provider/... -run TestContextTokens`
Expected: build error — `undefined: ContextTokens`.

- [ ] **Step 3: Implement registry**

Create `core/provider/capacity.go`:
```go
package provider

// modelContextTokens is the seed table of per-model context window
// capacities. Update as new models ship; unknown models fall back to a
// conservative 200k default. The default is chosen so that the
// compaction trigger fires no later than the most common model class
// (Sonnet/Haiku/GPT-4 family at 128-200k).
var modelContextTokens = map[string]int64{
    // Anthropic
    "claude-opus-4-7":           1_000_000,
    "claude-opus-4-7[1m]":       1_000_000,
    "claude-sonnet-4-6":           200_000,
    "claude-haiku-4-5-20251001":   200_000,

    // OpenAI
    "gpt-4o":      128_000,
    "gpt-4o-mini": 128_000,
    "gpt-5":       400_000,

    // Google
    "gemini-2-pro":     1_000_000,
    "gemini-1.5-flash": 1_000_000,

    // Local / Ollama (per-model varies; seed values from common configs)
    "ollama:llama3:8b":         8_192,
    "ollama:qwen3-coder:32b":  32_768,
}

// ContextTokens returns the maximum context window in tokens for the
// given model identifier. Unknown models receive a conservative 200k
// default so the compaction trigger still fires reasonably.
func ContextTokens(model string) int64 {
    if v, ok := modelContextTokens[model]; ok {
        return v
    }
    return 200_000
}
```

- [ ] **Step 4: Run (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./core/provider/... -run TestContextTokens -v`
Expected: 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add core/provider/capacity.go core/provider/capacity_test.go
git commit -m "feat(provider): P27 T1 — per-model context window registry"
```

---

## Task 2: Provider-Aware Char Multiplier (Fix 6)

**Files:**
- Create: `core/provider/tokenest.go`
- Create: `core/provider/tokenest_test.go`
- Modify: `core/runner/runner.go` (replace inline `/4` with helper call)
- Modify: `core/compact/compactor.go` (same)

- [ ] **Step 1: Write failing tests**

Create `core/provider/tokenest_test.go`:
```go
package provider

import (
    "testing"

    "github.com/stretchr/testify/require"
)

// Same input string, different providers → different estimates.
func TestEstimateTokens_PerProviderDiffer(t *testing.T) {
    s := "Lorem ipsum dolor sit amet, consectetur adipiscing elit."
    a := EstimateTokens("anthropic", s)
    o := EstimateTokens("openai", s)
    g := EstimateTokens("google", s)
    l := EstimateTokens("ollama", s)
    require.NotEqual(t, a, o, "anthropic ≠ openai estimates")
    require.NotEqual(t, o, g, "openai ≠ google estimates")
    require.NotEqual(t, g, l, "google ≠ ollama estimates")
}

func TestEstimateTokens_AnthropicDenser(t *testing.T) {
    s := "func foo() { return bar(baz, qux) }"
    a := EstimateTokens("anthropic", s)
    o := EstimateTokens("openai", s)
    require.Greater(t, a, o, "anthropic estimate > openai (denser code tokens)")
}

func TestEstimateTokens_UnknownProviderUsesDefault(t *testing.T) {
    s := "hello world hello world hello world hello world"
    e := EstimateTokens("unknown", s)
    // Default is 4.0 chars/token — same as openai.
    require.Equal(t, EstimateTokens("openai", s), e)
}

func TestEstimateTokens_EmptyIsZero(t *testing.T) {
    require.Equal(t, 0, EstimateTokens("openai", ""))
}
```

- [ ] **Step 2: Run (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./core/provider/... -run TestEstimateTokens`
Expected: build error.

- [ ] **Step 3: Implement helper**

Create `core/provider/tokenest.go`:
```go
package provider

// providerCharsPerToken is a coarse heuristic that improves on a
// single global 4-chars-per-token rule by acknowledging that different
// providers' tokenizers encode text at different densities. Values
// reflect rough averages across code-heavy mission content.
//
// Phase 27.5 will replace this for OpenAI with tiktoken-go (offline,
// accurate). Phase 28 will integrate Anthropic and Google count_tokens
// API calls with response caching.
var providerCharsPerToken = map[string]float64{
    "anthropic": 3.5,
    "openai":    4.0,
    "google":    3.8,
    "ollama":    4.5,
}

const defaultCharsPerToken = 4.0

// EstimateTokens returns a coarse token-count estimate for the given
// string under the given provider's tokenizer characteristics. This is
// a heuristic — accurate to ~85% — and exists so the compaction
// trigger fires at a reasonable threshold across providers without
// pulling in heavy tokenizer dependencies in V1.
func EstimateTokens(providerID, s string) int {
    if s == "" {
        return 0
    }
    cpt, ok := providerCharsPerToken[providerID]
    if !ok {
        cpt = defaultCharsPerToken
    }
    return int(float64(len(s))/cpt + 0.5)
}
```

- [ ] **Step 4: Run (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./core/provider/... -run TestEstimateTokens -v`
Expected: 4 tests PASS.

- [ ] **Step 5: Replace 4-char division in runner.go and compactor.go**

Find the existing `estimateMessagesTokens` (or equivalent) in `core/runner/runner.go`. It looks like:
```go
func estimateMessagesTokens(msgs []Message) int {
    n := 0
    for _, m := range msgs {
        n += len(m.Content) / 4
        // ... tool calls, tool results similar
    }
    return n
}
```

Refactor to take a provider ID:
```go
func estimateMessagesTokens(providerID string, msgs []Message) int {
    n := 0
    for _, m := range msgs {
        n += provider.EstimateTokens(providerID, m.Content)
        for _, tc := range m.ToolCalls {
            n += provider.EstimateTokens(providerID, tc.Input)
        }
        for _, tr := range m.ToolResults {
            n += provider.EstimateTokens(providerID, tr.Content)
        }
    }
    return n
}
```

Add `"github.com/mindungil/gil/core/provider"` to imports if not present. Update all call sites in runner.go to pass the provider ID. Mirror the same change in `core/compact/compactor.go` (its `estimateMessagesTokens` becomes provider-aware too).

For call sites that don't yet know the provider ID (rare), pass `"openai"` as the safe default (4-char equivalent). Mark such sites with a `// TODO P27.5: thread provider context here` comment.

- [ ] **Step 6: Run all tests**

Run: `cd /home/ubuntu/gil && go test ./core/...`
Expected: PASS. Existing tests that called `estimateMessagesTokens` with the old signature now fail; update them to pass a provider string.

- [ ] **Step 7: Commit**

```bash
cd /home/ubuntu/gil
git add core/provider/tokenest.go core/provider/tokenest_test.go core/runner/runner.go core/compact/compactor.go
# Plus any test files updated for the new signature
git add core/runner/runner_test.go core/compact/compactor_test.go
git commit -m "feat(provider): P27 T2 — provider-aware char multiplier (replaces uniform /4)"
```

---

## Task 3: Compactor Factory + Production Wire-Up (Fix 1)

**Files:**
- Create: `core/runner/factory.go`
- Create: `core/runner/factory_test.go`
- Modify: `server/internal/service/run.go`

- [ ] **Step 1: Read current factory pattern in run.go**

Run:
```bash
grep -n 'NewAgentLoop\|loop\.\|AgentLoop{' /home/ubuntu/gil/server/internal/service/run.go
```
Capture the call site where `AgentLoop` is constructed. This is where the new factory output gets assigned.

Read the Compactor signature:
```bash
grep -n 'func NewCompactor\|func New\b' /home/ubuntu/gil/core/compact/compactor.go
```
Note the exact name and parameters. The plan assumes `compact.NewCompactor(provider, model, opts)`; rename if needed.

- [ ] **Step 2: Write failing test**

Create `core/runner/factory_test.go`:
```go
package runner

import (
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/mindungil/gil/core/compact"
    "github.com/mindungil/gil/sdk"
)

// fakeProvider satisfies whatever provider interface compact.NewCompactor needs.
// Replace with the real interface and a stub that returns canned summaries.
type fakeProvider struct{}

func (fakeProvider) Complete(ctx interface{}, req interface{}) (interface{}, error) {
    return nil, nil
}

func TestNewCompactorFromSpec_PrefersWeakModel(t *testing.T) {
    spec := &sdk.SpecView{ /* Build with Models.Weak="haiku", Models.Main="opus" */ }
    providers := map[string]Provider{ /* mock anthropic */ }
    c, err := NewCompactorFromSpec(spec, providers)
    require.NoError(t, err)
    require.NotNil(t, c)
    // Implementation-detail check: the compactor's model field should be the weak one.
    require.Equal(t, "haiku", compactorModel(c))
}

func TestNewCompactorFromSpec_FallsBackToMainWhenNoWeak(t *testing.T) {
    spec := &sdk.SpecView{ /* Build with only Models.Main="opus" */ }
    providers := map[string]Provider{ /* mock */ }
    c, err := NewCompactorFromSpec(spec, providers)
    require.NoError(t, err)
    require.Equal(t, "opus", compactorModel(c))
}

// compactorModel exposes the configured model for assertion. May
// require adding an exported accessor on *compact.Compactor or making
// the field exported. Choose the lowest-friction option.
func compactorModel(c *compact.Compactor) string { return c.Model() }
```

> **Note:** The exact `sdk.SpecView` builder pattern + the `Provider` type are gil-internal; copy from existing tests in `core/runner/runner_test.go` for shape.

- [ ] **Step 3: Run (expect fail — symbol undefined)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestNewCompactorFromSpec`
Expected: build error.

- [ ] **Step 4: Implement factory**

Create `core/runner/factory.go`:
```go
package runner

import (
    "fmt"

    "github.com/mindungil/gil/core/compact"
    "github.com/mindungil/gil/sdk"
)

// NewCompactorFromSpec builds a production-ready Compactor from a
// resolved spec and the provider registry available to the run loop.
//
// Model selection: prefer spec.Models.Weak (cost-efficient summarizer);
// fall back to spec.Models.Main if Weak is unset. The summarizer's
// provider must exist in the providers map.
func NewCompactorFromSpec(spec *sdk.SpecView, providers map[string]Provider) (*compact.Compactor, error) {
    model := spec.Models.Weak
    if model == "" {
        model = spec.Models.Main
    }
    if model == "" {
        return nil, fmt.Errorf("NewCompactorFromSpec: no model configured (neither Weak nor Main)")
    }
    providerID := providerIDForModel(model)
    p, ok := providers[providerID]
    if !ok {
        return nil, fmt.Errorf("NewCompactorFromSpec: provider %q not in registry", providerID)
    }
    return compact.NewCompactor(p, model, compact.Opts{
        Head:                2,
        Tail:                6,
        ThresholdPercentage: 95,
    }), nil
}

// providerIDForModel maps a model identifier to its provider ID.
// Mirrors the existing convention in core/runner/runner.go's classifier.
func providerIDForModel(model string) string {
    switch {
    case len(model) >= 6 && model[:6] == "claude":
        return "anthropic"
    case len(model) >= 3 && model[:3] == "gpt":
        return "openai"
    case len(model) >= 6 && model[:6] == "gemini":
        return "google"
    case len(model) >= 7 && model[:7] == "ollama:":
        return "ollama"
    default:
        return "openai"
    }
}
```

If the existing codebase already has a `providerIDForModel`-equivalent helper, import it instead of duplicating.

- [ ] **Step 5: Wire factory into RunService**

Edit `server/internal/service/run.go`. Find the `NewAgentLoop(...)` call site (Step 1 located it). Add:
```go
loop := runner.NewAgentLoop(/* existing args */)

// P27 T3: instantiate Compactor from spec so the 95% threshold check
// in runner.go is no longer dead code.
compactor, err := runner.NewCompactorFromSpec(spec, providers)
if err != nil {
    return fmt.Errorf("compactor setup: %w", err)
}
loop.Compactor = compactor
```

- [ ] **Step 6: Run tests**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... ./server/...`
Expected: PASS. If a server integration test fails because the spec stub doesn't have Models, update the stub.

- [ ] **Step 7: Commit**

```bash
cd /home/ubuntu/gil
git add core/runner/factory.go core/runner/factory_test.go server/internal/service/run.go
git commit -m "feat(runner): P27 T3 — Compactor factory + production wire-up"
```

---

## Task 4: Cache Breakpoint Mainline Call (Fix 2)

**Files:**
- Modify: `core/runner/runner.go`
- Modify: `core/runner/runner_test.go` (add coverage)

- [ ] **Step 1: Locate the request-construction site**

Run:
```bash
grep -n 'provider.Complete\|p.Complete(\|Provider.Complete' /home/ubuntu/gil/core/runner/runner.go
```
Note the line. The cache-marker call needs to happen on `req.Messages` immediately before this line.

- [ ] **Step 2: Write failing test**

Append to `core/runner/runner_test.go`:
```go
func TestRunner_AnthropicRequest_HasCacheMarkers(t *testing.T) {
    captured := &capturedReqProvider{}
    loop := newTestLoop(t, withMainProvider("anthropic", captured), withModel("claude-sonnet-4-6"))
    // Pre-load 5 messages so the marker has something to mark.
    seedMessages(loop, 5)
    require.NoError(t, loop.runOneIteration(context.Background()))

    require.NotNil(t, captured.req, "provider must have been called")
    // Last 3 messages should carry CacheControl=ephemeral.
    n := len(captured.req.Messages)
    for i := n - 3; i < n; i++ {
        require.Equal(t, "ephemeral", captured.req.Messages[i].CacheControl,
            "message %d missing cache marker", i)
    }
}

func TestRunner_OpenAIRequest_NoCacheMarkers(t *testing.T) {
    captured := &capturedReqProvider{}
    loop := newTestLoop(t, withMainProvider("openai", captured), withModel("gpt-4o"))
    seedMessages(loop, 5)
    require.NoError(t, loop.runOneIteration(context.Background()))

    for _, m := range captured.req.Messages {
        require.Empty(t, m.CacheControl, "non-Anthropic must not get cache markers")
    }
}
```

If `capturedReqProvider`, `newTestLoop`, `seedMessages` don't exist, write minimal versions inline matching gil's existing test helpers (look at `runner_test.go` Top of file for patterns).

- [ ] **Step 3: Run (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestRunner_(Anthropic|OpenAI)Request_`
Expected: FAIL — assertions about cache markers don't hold.

- [ ] **Step 4: Insert the call site**

In `core/runner/runner.go`, immediately before `provider.Complete(ctx, req)`:
```go
// P27 T4: Apply Anthropic prompt-caching markers to system + last 3
// messages so the static prefix is cached and recent turns hit the
// rolling cache window. No-op for non-Anthropic providers.
if req.Provider == "anthropic" {
    req.Messages = compact.MarkCacheBreakpoints(req.Messages, compact.CacheOpts{Recent: 3})
}
resp, err := provider.Complete(ctx, req)
```

Add `"github.com/mindungil/gil/core/compact"` to imports. If `req.Provider` isn't a field, derive from the model:
```go
if provider.IsAnthropic(req.Model) {
    // ...
}
```
(or use the helper from Task 3's `providerIDForModel`).

- [ ] **Step 5: Run (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -v`
Expected: new tests PASS, existing PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/ubuntu/gil
git add core/runner/runner.go core/runner/runner_test.go
git commit -m "feat(runner): P27 T4 — MarkCacheBreakpoints called on Anthropic mainline"
```

---

## Task 5: Per-Role Context Budget (Fix 4)

**Files:**
- Modify: `core/runner/runner.go`
- Modify: `core/runner/runner_test.go`

Adapt the compaction trigger to use the next-turn model's context window via `provider.ContextTokens(model)` (Task 1 output) rather than a single `MaxContextTokens` field.

- [ ] **Step 1: Write failing test**

Append to `core/runner/runner_test.go`:
```go
func TestRunner_PerRoleContextWindow_SmallEditorTriggersEarly(t *testing.T) {
    // Spec routes to small Ollama editor → threshold = 8k, not 200k.
    spec := newSpecWithModels(t, sdk.Models{
        Main:   "claude-sonnet-4-6",       // 200k
        Editor: "ollama:llama3:8b",        // 8k
    })
    loop := newTestLoop(t, withSpec(spec))
    // Seed messages totaling ~7,500 tokens (over 95% of 8k = 7,600).
    seedMessagesWithTokenCount(loop, 7_500)

    triggered := false
    loop.OnCompactRequested = func() { triggered = true }

    // Force the next turn to be classified as 'editor' role.
    loop.forceNextRole = "editor"
    require.NoError(t, loop.runOneIteration(context.Background()))

    require.True(t, triggered, "small-context editor role must trigger compaction at 7.5k")
}

func TestRunner_PerRoleContextWindow_LargeMainTolerates(t *testing.T) {
    spec := newSpecWithModels(t, sdk.Models{
        Main: "claude-opus-4-7", // 1M
    })
    loop := newTestLoop(t, withSpec(spec))
    seedMessagesWithTokenCount(loop, 7_500)

    triggered := false
    loop.OnCompactRequested = func() { triggered = true }

    require.NoError(t, loop.runOneIteration(context.Background()))
    require.False(t, triggered, "1M-context main model must not trigger at 7.5k")
}
```

- [ ] **Step 2: Run (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestRunner_PerRoleContextWindow`
Expected: FAIL — current code uses single MaxContextTokens regardless of model.

- [ ] **Step 3: Refactor the threshold check**

In `core/runner/runner.go` find the existing trigger (Task 0 Step 3 located it):
```go
threshold := int64(float64(a.MaxContextTokens) * 0.95)
if a.totalContextTokens >= threshold && a.Compactor != nil {
    a.Compactor.RequestCompact()
}
```

Replace with:
```go
nextModel := a.modelForNextTurn(role)
ctxWindow := provider.ContextTokens(nextModel)
threshold := int64(float64(ctxWindow) * 0.95)
if a.totalContextTokens >= threshold && a.Compactor != nil {
    a.Compactor.RequestCompact()
}
```

If `modelForNextTurn(role)` doesn't exist, add it as a small helper that returns `a.Models[role]` falling back to `a.Models["main"]`.

Keep `MaxContextTokens` as a fallback when role/model can't be determined: `if ctxWindow == 0 { ctxWindow = a.MaxContextTokens }`.

- [ ] **Step 4: Run (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestRunner_PerRoleContextWindow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add core/runner/runner.go core/runner/runner_test.go
git commit -m "feat(runner): P27 T5 — per-role context window adapts compaction trigger"
```

---

## Task 6: Grace Call on Budget Exhaustion (Fix 5)

**Files:**
- Create: `core/runner/grace.go`
- Create: `core/runner/grace_test.go`
- Modify: `core/runner/runner.go` (hook into existing budget-check path)

- [ ] **Step 1: Write failing test**

Create `core/runner/grace_test.go`:
```go
package runner

import (
    "context"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestGraceCall_FiresOnceOnBudgetExhaust(t *testing.T) {
    captured := &capturedReqProvider{}
    loop := newTestLoop(t, withMainProvider("anthropic", captured),
        withBudget(100 /*tokens*/), withCostBudget(1.0))
    // Drive usage past budget.
    loop.totalTokens = 110

    err := loop.checkBudgetAndMaybeGrace(context.Background())
    require.NoError(t, err)
    require.NotNil(t, captured.req, "grace call must invoke the provider once")
    require.Equal(t, 1, captured.callCount, "grace must fire exactly once")

    // Wrap-up message must request a hand-off summary.
    last := captured.req.Messages[len(captured.req.Messages)-1]
    require.Contains(t, strings.ToUpper(last.Content), "WRAP")
    require.Contains(t, strings.ToLower(last.Content), "pending")
    require.Contains(t, strings.ToLower(last.Content), "resume")
}

func TestGraceCall_RespectsNoGraceFlag(t *testing.T) {
    captured := &capturedReqProvider{}
    loop := newTestLoop(t, withMainProvider("anthropic", captured),
        withBudget(100), withCostBudget(1.0), withNoGrace(true))
    loop.totalTokens = 110

    require.NoError(t, loop.checkBudgetAndMaybeGrace(context.Background()))
    require.Equal(t, 0, captured.callCount, "no-grace flag must skip the wrap-up call")
}

func TestGraceCall_EndStatusIncludesHandoff(t *testing.T) {
    loop := newTestLoop(t, withMainProvider("anthropic", &capturedReqProvider{}),
        withBudget(100), withCostBudget(1.0))
    loop.totalTokens = 110
    require.NoError(t, loop.checkBudgetAndMaybeGrace(context.Background()))
    require.Equal(t, "budget_exhausted_with_handoff", loop.status)
}
```

- [ ] **Step 2: Run (expect fail)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestGraceCall`
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement grace call**

Create `core/runner/grace.go`:
```go
package runner

import (
    "context"
    "fmt"
)

// graceWrapUpPrompt is the synthetic user instruction inserted on the
// final turn before budget exhaustion. The agent is asked to stop work
// and emit a hand-off summary so a future resume has a clean starting
// point.
const graceWrapUpPrompt = `[BUDGET WRAP-UP] You are about to hit the budget cap. This is your final turn.
Stop work. Output: (1) what got done, (2) what's pending, (3) which file/state
the next iteration should resume from. Do not call tools.`

// checkBudgetAndMaybeGrace inspects current usage against budget. If
// the next turn would exceed the cap, fire one final "wrap-up" call to
// the provider asking for a hand-off summary, then halt with status
// "budget_exhausted_with_handoff". The behavior is gated by the
// NoGrace flag for users who prefer a hard cutoff.
func (a *AgentLoop) checkBudgetAndMaybeGrace(ctx context.Context) error {
    if a.totalTokens < a.budgetMaxTokens && a.totalCostUSD < a.budgetMaxCostUSD {
        return nil // still under cap
    }
    if a.NoGrace {
        a.status = "budget_exhausted"
        return nil
    }
    if a.graceFired {
        a.status = "budget_exhausted_with_handoff"
        return nil // already grace-called; do not loop
    }
    a.graceFired = true

    req := a.buildRequest()
    req.Messages = append(req.Messages, Message{
        Role:    "user",
        Content: graceWrapUpPrompt,
    })
    p, ok := a.Providers[providerIDForModel(req.Model)]
    if !ok {
        return fmt.Errorf("grace call: provider for %q missing", req.Model)
    }
    resp, err := p.Complete(ctx, req)
    if err != nil {
        // Even on error, we set the handoff status so callers know we tried.
        a.status = "budget_exhausted_with_handoff"
        return nil
    }
    a.appendAssistant(resp)
    a.status = "budget_exhausted_with_handoff"
    return nil
}
```

Add fields to `AgentLoop` in `runner.go`:
```go
NoGrace    bool
graceFired bool
```

Hook the call into the existing budget-check site in `runner.go` (currently sets `status="budget_exhausted"` cold). Replace:
```go
if budgetExceeded {
    a.status = "budget_exhausted"
    return
}
```
with:
```go
if budgetExceeded {
    if err := a.checkBudgetAndMaybeGrace(ctx); err != nil {
        return // logs error in status callback
    }
    return
}
```

If `buildRequest()`, `appendAssistant()`, or similar helpers don't exist, lift them from existing turn-construction code in runner.go.

- [ ] **Step 4: Run (expect pass)**

Run: `cd /home/ubuntu/gil && go test ./core/runner/... -run TestGraceCall -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/ubuntu/gil
git add core/runner/grace.go core/runner/grace_test.go core/runner/runner.go
git commit -m "feat(runner): P27 T6 — Hermes-style grace call on budget exhaustion"
```

---

## Task 7: Integration Test

**Files:**
- Create: `tests/integration/p27_context_wiring_test.go`

- [ ] **Step 1: Build the test**

Create `tests/integration/p27_context_wiring_test.go`:
```go
//go:build integration

package integration

import (
    "context"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestP27_CompactionFiresOnLargeRun(t *testing.T) {
    daemon := startTestDaemon(t)
    defer daemon.Stop()

    sess := daemon.NewSession(t, "test-mission")
    daemon.FreezeSpecMinimal(t, sess.ID)

    // Mock provider responds with synthetic large tool outputs.
    daemon.SetProviderResponse(t, generateLargeToolOutput(8_000 /* tokens */))

    err := daemon.RunIters(t, sess.ID, 30)
    require.NoError(t, err)

    events := daemon.AllEvents(t, sess.ID)
    var sawCompaction bool
    for _, e := range events {
        if strings.Contains(e.Kind, "compaction") {
            sawCompaction = true
        }
    }
    require.True(t, sawCompaction, "expected at least one compaction event")
}

func TestP27_AnthropicCacheMarkersInRequest(t *testing.T) {
    daemon := startTestDaemon(t)
    defer daemon.Stop()

    sess := daemon.NewSessionWithModel(t, "test-mission", "claude-sonnet-4-6")
    daemon.FreezeSpecMinimal(t, sess.ID)
    err := daemon.RunIters(t, sess.ID, 5)
    require.NoError(t, err)

    last := daemon.LastProviderRequest(t)
    require.NotEmpty(t, last.Messages)
    n := len(last.Messages)
    var marked int
    for i := n - 3; i < n; i++ {
        if last.Messages[i].CacheControl == "ephemeral" {
            marked++
        }
    }
    require.GreaterOrEqual(t, marked, 1, "at least one of last-3 messages must have cache marker")
}
```

If the test daemon helpers (`startTestDaemon`, `NewSession`, `FreezeSpecMinimal`, etc.) don't exist with those names, look for the closest equivalents in `tests/integration/*` and adapt. If the integration scaffolding is thin, write minimal helpers inline.

- [ ] **Step 2: Run integration tests**

Run: `cd /home/ubuntu/gil && go test -tags integration ./tests/integration/... -run TestP27 -v`
Expected: PASS. If failing because spec/factory wiring (Task 3) didn't take effect, debug there.

- [ ] **Step 3: Commit**

```bash
cd /home/ubuntu/gil
git add tests/integration/p27_context_wiring_test.go
git commit -m "test(integration): P27 T7 — Compactor + cache marker e2e"
```

---

## Task 8: Dogfood E2E

**Goal:** Verify on real Anthropic mission that compaction + cache hits actually occur.

- [ ] **Step 1: Build the daemon + CLI**

```bash
cd /home/ubuntu/gil
go build -o /tmp/gild ./server/cmd/gild
go build -o /tmp/gil ./cli/cmd/gil
```

- [ ] **Step 2: Start daemon**

```bash
/tmp/gild daemon --detach
```

- [ ] **Step 3: Run a long real mission**

Use a synthetic dogfood task with a known target:
```bash
/tmp/gil new --working-dir /tmp/p27-dogfood --goal "explore /etc and write a 300-line analysis to ./ANALYSIS.md"
# Then drive it through interview + run via existing CLI verbs.
/tmp/gil interview <id>  # let it freeze
/tmp/gil run <id>
```

This is a deliberately context-heavy task (read many small files, write a long report) to push iters into compaction territory.

- [ ] **Step 4: Verify acceptance**

Check:
1. Mission completes without `provider error 4xxx context_length_exceeded`.
2. Event log shows ≥1 `compaction.applied` (or similar) event:
   ```bash
   /tmp/gil events <id> | grep -i compact
   ```
3. Anthropic cache hit rate observable (set ANTHROPIC_LOG=debug or inspect Console billing dashboard for the run's session ID).
4. `/tmp/gil cost <id> --by-role` shows breakdown — sanity-check that summarizer (weak model) cost is small relative to main turns.

- [ ] **Step 5: Force budget exhaust on a small mission**

```bash
/tmp/gil new --working-dir /tmp/p27-budget --goal "echo done" --budget-max-tokens 5000
/tmp/gil interview <id>
/tmp/gil run <id>
```

The mission should hit budget within a few iters and emit one wrap-up message before stopping with status `budget_exhausted_with_handoff`.

- [ ] **Step 6: Commit phase marker**

```bash
cd /home/ubuntu/gil
git commit --allow-empty -m "chore: P27 V1 dogfood passed — context wiring is live"
```

---

## Task 9: V1.5 + V2 Forward Specs (housekeeping)

**Files:**
- Create: `docs/plans/phase-27.5-tiktoken.md` (one-page stub)
- Create: `docs/plans/phase-28-token-count-api.md` (one-page stub)

- [ ] **Step 1: Write Phase 27.5 stub**

Create `docs/plans/phase-27.5-tiktoken.md`:
```markdown
# Phase 27.5 — OpenAI tiktoken-go Integration

**Status**: planned, not started
**Predecessor**: Phase 27

## Goal

Replace the provider-aware char-multiplier (P27 Fix 6) for OpenAI requests with `pkoukk/tiktoken-go` so OpenAI token estimates are exact.

## Scope

- Add `github.com/pkoukk/tiktoken-go` to go.mod.
- In `core/provider/tokenest.go`, branch on provider: OpenAI calls tiktoken; Anthropic/Google/Ollama keep the multiplier path (unchanged).
- Cache the tiktoken encoder per-model (encoders are reusable; instantiation has cost).
- Add tests asserting exact-token counts for known fixtures (OpenAI publishes reference encodings).

## Estimate

~1-2 days.

## Deferred to Phase 28

Anthropic and Google count_tokens API integration.
```

Create `docs/plans/phase-28-token-count-api.md` (similar one-page stub for Anthropic + Google API integration with response caching).

- [ ] **Step 2: Commit**

```bash
cd /home/ubuntu/gil
git add docs/plans/phase-27.5-tiktoken.md docs/plans/phase-28-token-count-api.md
git commit -m "docs(plans): P27 T9 — V1.5/V2 forward stubs"
```

---

## Self-Review Checklist (engineer should verify before declaring V1 done)

1. **Spec coverage** — every fix in `phase-27-context-wiring-repair.md` §3 maps to a task above.
2. **Compactor wiring is live** — `grep loop.Compactor server/internal/service/run.go` shows assignment.
3. **Cache markers visible in Anthropic requests** — capture a request via mock provider in test, manually verify `cache_control: ephemeral` present.
4. **Per-model context respected** — switching models mid-run changes the threshold (see Task 5 test).
5. **Grace call works** — Task 6 dogfood reproduces.
6. **All tests pass** — `go test ./... && go test -tags integration ./tests/integration/...` clean.
7. **Provider-aware estimates active** — `grep provider.EstimateTokens core/runner core/compact` shows usage, no remaining inline `/4`.

After self-review: P26 (chat surface) implementation can begin. The status strip's iter/cost numbers will reflect a healthy context loop, not a lying surface.
