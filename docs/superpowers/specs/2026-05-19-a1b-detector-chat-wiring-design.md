# A1b — `core/stuck/Detector` chat-path wiring (with AdversaryConsult opt-in)

Status: draft → in implementation
Authors: jedutools@gmail.com (driver), Claude Opus 4.7 (1M context) (drafter)
Branch: `feat/p67-detector-chat-wiring`

## Motivation

2026-05-19 chess N=5 @ T=0.3 probe: **0/5 PASS, 5/5 premature_stop** at every sampling temperature tried so far. Chess boundary is reorientation-shaped, not sampling-shaped — agent commits to a wrong initial design, ends turns with `end_turn`, and dogfood/operator re-prompts indefinitely.

The escape hatch already exists in code: `AdversaryConsultStrategy` (`core/stuck/recovery.go:280-344`, lifted from Goose's adversary_inspector). Runner mode wires it via `core/stuck/Detector` + `StuckStrategy`. Chat mode does not — chat has only a one-pattern ad-hoc detector (`chatStuckCheck`, P39) that matches `PatternRepeatedActionObservation`. Chess never triggers P39 (each turn writes different code → no identical repeats), so the existing seam is dead-wiring for chess.

Goal: pull the full `core/stuck/Detector` (including `PatternNoProgress` which matches varied-but-futile work like chess) into the chat path, route signals through `StuckStrategy`, and surface `AdversaryConsultStrategy` decisions as visible system Parts. Make adversary calls opt-in via `Risk.AdversaryModel` (already a proto field, runner-only consumer so far).

3-axis impact:

| axis | impact |
|---|---|
| 정확도 (PASS/N) | direct — chess premature_stop becomes recoverable |
| 완성도 (no premature_stop) | direct — adversary suggestion gives agent a concrete next step |
| context 유지도 (max-turn-tok) | risk — each adversary call burns ~256 output + N input tokens. Mitigated by per-session budget cap and ContextWindow-pattern skip |

## Non-goals

- Persisting the event ring buffer across daemon restarts (P36/P37 reaper handles restart; stuck history on each restart starts empty — acceptable for autonomous-run scope).
- Wiring `SubagentBranchStrategy` or `ResetSectionStrategy` into chat. Chat has no checkpoint store, and depth=2 subagent spawning is already covered by P40 with a separate trigger. Out of scope.
- Adding new stuck patterns. We use the existing six from `core/stuck/detector.go` only.
- Replacing model-escalation with a "switch to bigger model" mechanism. `ModelEscalateStrategy` returns a Decision; in chat we treat it as a no-op for v1 (chat is single-model) and emit only as an observability event.

## Architecture

Two boundaries:

1. **Detector boundary** — `core/stuck/Detector` is unchanged. It is a pure function over `[]event.Event`. Chat path becomes its second consumer (runner is the first).
2. **Strategy boundary** — `StuckStrategy.Apply` is unchanged. Chat dispatcher constructs the same `ApplyRequest` the runner does, with `AdversaryModel` sourced from `Risk.AdversaryModel`.

Net new code lives in:
- `server/internal/service/session_prompt.go` (chat agent loop) — event collection, dispatcher invocation, Part emission.
- `server/internal/service/chat_stuck.go` (new, ~150 LOC) — per-session ring buffer + dispatch helper + budget tracking. Pulled out of `session_prompt.go` for testability.
- `cli/internal/cmd/dogfood.go` — `--adversary-model` flag, threaded through to `PromptOptions`.
- `sdk/client.go` — `PromptOptions.AdversaryModel` field.
- `proto/gil/v1/session.proto` — already has `RiskProfile.adversary_model` (field exists from runner work); thread through `PromptRequest` as field 7.

**Removed code**:
- `chatStuckSig`, `chatStuckCheck`, `chatStuckFired`, `chatCallSigs` (P39 ad-hoc) — `PatternRepeatedActionObservation` replaces them functionally.

## Components

### a. Event ring buffer

Per-session `*chatEventBuffer` struct in `chat_stuck.go`:

```go
type chatEventBuffer struct {
    mu     sync.Mutex
    events []event.Event // cap = 200, drop-oldest when full
    iter   int           // monotonically increasing, ++ per user turn
    seenThisTurn map[stuck.Pattern]bool // per-turn dedup
    adversaryCalls int  // per-session counter, capped at 5 (v1)
}

func (b *chatEventBuffer) push(e event.Event)
func (b *chatEventBuffer) snapshot() []event.Event   // returns a copy for Detector.Check
func (b *chatEventBuffer) resetTurn()                // clears seenThisTurn, increments iter
```

- Cap 200 events × ~50 byte stub = 10KB/session. Negligible.
- `iter` is the source for `iteration_start` event data field.
- Lifetime: in-memory, not persisted. Restart → buffer reset → first signals after restart need 4 fresh iters to fire (NoProgress threshold). Acceptable.

### b. Event emitters in chat agent loop

Already emit: `tool_call`, `tool_result`, `stuck_detected` (P39 — being removed).

Add:
- `iteration_start` — at top of `Prompt()` per user turn. Data: `{"iter": N}` where N comes from `chatEventBuffer.iter`.
- `verify_run` — synthetic, emitted *immediately before* dispatchTool when `call.Name == "verify"`. Data: empty.
- `verify_result` — synthetic, emitted *immediately after* tool_result is appended when `call.Name == "verify"`. Data: `{"passed": !result.IsError}`. Heuristic: result.IsError=false → passed=true. If we need stricter "passed" parsing later (parse "PASS"/"FAIL" out of result.Content), do it then.
- `provider_response` — emitted at provider Complete response close. Data: `{"text_len": <int>}`. Full text NOT stored (context bloat). Detector's Monologue check uses length thresholds only — see `core/stuck/detector.go:checkMonologue`.

All events flow through both `emitChatEvent` (existing — for giltui Tail subscribers and DB audit) and `chatEventBuffer.push` (new — for Detector consumption).

### c. Detector + Strategy dispatcher

In `chat_stuck.go`:

```go
type chatStuckDispatcher struct {
    detector   *stuck.Detector
    strategies []stuck.Strategy  // order: ModelEscalate, AltToolOrder, AdversaryConsult
    provider   provider.Provider
    model      string            // current chat model
    riskAdv    string            // Risk.AdversaryModel; "" disables AdversaryConsult
}

func (d *chatStuckDispatcher) tick(ctx, buf *chatEventBuffer, history []provider.Message) []stuck.Decision
```

`tick` is called after every `tool_result` push. Steps:
1. `signals := d.detector.Check(buf.snapshot())`
2. For each signal: if `buf.seenThisTurn[signal.Pattern]` continue, else set true.
3. For each new signal: try each strategy in order until one returns Decision (not `ErrNoFallback`).
4. If strategy is `AdversaryConsultStrategy`:
   - if `riskAdv == ""` → skip (treat as ErrNoFallback)
   - if `buf.adversaryCalls >= 5` → emit `adversary_skipped_budget` event + skip
   - else `buf.adversaryCalls++`, call `Apply`
5. Return decisions; caller emits Parts/events.

### d. Suggestion delivery

For every returned `Decision`:
- Visible Part: `[system] adversary: <Explanation>` if `Action == ActionAdversaryConsult`. For other actions, `[system] stuck-recover (<action>): <Explanation>` (so AltToolOrder hints surface via the same path — Gap 1 fix).
- Event: `adversary_consulted` with `{action, pattern, suggestion}` payload.

The agent sees these Parts as part of the next inference's stream history (chat history already includes assistant Part text, so when the LLM resumes, the system Part is in-context).

## Data flow

```
chat Prompt() entry
  → buf.resetTurn()
  → buf.push(iteration_start{iter})
loop:
  agent → provider.Complete
  emit provider_response{text_len}
  buf.push(provider_response{...})
  for each tool_call in response:
    emit tool_call
    buf.push(tool_call)
    if name == "verify":
      emit verify_run
      buf.push(verify_run)
    dispatch tool
    emit tool_result
    buf.push(tool_result)
    if name == "verify":
      emit verify_result{passed: !IsError}
      buf.push(verify_result)
    decisions := dispatcher.tick(ctx, buf, history)
    for d in decisions:
      stream.Send(Part{Text: "[system] ..."})
      emit adversary_consulted
  if stop_reason == end_turn: break
Prompt() returns
```

**Cross-turn property**: `iter` is monotonically increasing across user turns within the same session. `iteration_start` events stay in the ring buffer across turn boundaries (until evicted by cap). `checkNoProgress` walks `iteration_start` markers in the window — so cumulative 4 iterations of "verify failing + files churning" across user turns matches even though each turn is a separate `Prompt()` call. This is the chess fix.

## Opt-in & budget (Gap 2 fix)

| knob | default | source |
|---|---|---|
| Detector running | ON | hardcoded — system safeguard |
| ModelEscalate signal → action | log only (no model switch in chat v1) | hardcoded |
| AltToolOrder signal → action | visible Part hint | hardcoded |
| AdversaryConsult call | OFF | `Risk.AdversaryModel == ""` |
| Per-session adversary cap | 5 | const (1st-pass — adjust after telemetry) |
| Per-turn pattern dedup | ON | hardcoded — prevents fire-storm |

`--adversary-model <model>` flag on `gil dogfood` threads through `PromptOptions.AdversaryModel` → `PromptRequest.AdversaryModel` (proto field 7) → `Risk.AdversaryModel` at chat session entry. Empty string disables adversary path. Other strategies (AltToolOrder visible Part hint) still fire unconditionally.

**Budget cap rationale**: 5 calls × ~512 tok (input+output) ≈ 2.5k tok overhead per session. In a 12h autonomous run with ~150 turns, this is ~3% of typical token usage. If telemetry shows adversary is firing too often or too rarely, raise/lower the const and add `chat.adversary_max_calls` config. v1 is hardcoded; iteration is a follow-up phase.

## Error handling

| failure | behavior |
|---|---|
| `Detector.Check` panic | recover in dispatcher, log warning, no Part, agent loop continues |
| `StuckStrategy.Apply` returns error | warning event, no Part, continue |
| `Apply` returns `ErrNoFallback` | silent skip, try next strategy in order |
| Adversary LLM call timeout | `Apply` returns wrapped error → warning event, no Part |
| Empty/garbage adversary response | strategy already returns `ErrNoFallback` for empty (`recovery.go:336`) |
| `Provider == nil` | strategy returns `ErrNoFallback` (defensive — should not happen in chat) |
| `PatternContextWindowError` signal | strategy internally returns `ErrNoFallback` to avoid making overflow worse. Emit a `[system] context_overflow_detected` Part instead (no LLM call) |
| Daemon panic during Detector tick | chat loop has top-level recover (existing) — covers us |

## Testing (Gap 3 fix incorporated)

### Unit (server/internal/service)

`chat_stuck_test.go` (new):

1. **emit_test** — drive chat loop with mock provider that emits tool_call("verify") + tool_call("write_file") sequence. Assert ring buffer contains in order: iteration_start, provider_response, tool_call(verify), verify_run, tool_result(verify), verify_result, tool_call(write_file), tool_result(write_file).
2. **no_progress_test** — 4 user turns, each: write_file → verify (passed=false) → end_turn. After 4th turn's verify_result, `Detector.Check` returns at least one `PatternNoProgress` signal. Verifies cross-turn iter accumulation.
3. **adversary_opt_in_test** — same scenario:
   - `AdversaryModel == ""` → no adversary Part emitted. Other strategies may emit (AltToolOrder Part if signal matches).
   - `AdversaryModel == "test-model"` + stub provider returns "Read X.go" → exactly one `[system] adversary: ...` Part on the stream.
4. **budget_test** — 6 consecutive turns each triggering NoProgress → 5 adversary Parts + 1 `adversary_skipped_budget` event. Counter persists across turns within the session.
5. **dedup_test** — within one turn, two tool_result pushes that both leave Detector returning NoProgress → strategy.Apply called once.
6. **error_test** — stub provider returns error on adversary call → chat loop completes normally, no Part, warning event present.

### Integration

`docs/eval/variance-probe.sh` already supports an N-run sweep. Add an env-passed `--adversary-model` argument propagation; re-run chess N=5 with `--adversary-model qwen3.6-27b` (self-consult is acceptable per `recovery.go:316` fallback). Compare against the existing N=5 baseline at T=0.3 (0/5 PASS, 5/5 prem-stop).

**Success criteria** (any one is sufficient to confirm A1b is load-bearing for chess):
- PASS/N ≥ 1/5 (positive accuracy lift)
- premature_stop count < 5/5 (completeness lift even without PASS)
- `adversary_consulted` event count ≥ 1 per run, on average (signal-mechanism actually firing)

**3-axis measurement** (Gap 3):
- 정확도: PASS/N (already in driver)
- 완성도: premature count (already in driver)
- context 유지도: max-turn-tok delta. Baseline N=5 chess: 97k–931k. After adversary wiring, expect ≤ +5k per call × 5-call cap = +25k worst case. **Add assertion** in re-measurement: max-turn-tok 95th percentile increase ≤ 30k. If exceeded → adversary call is producing context bloat; revisit input-message slice size.

### Negative validation (dead-wiring guard per `[[feedback_check_production_wiring]]`)

Before declaring A1b done: take the existing `/tmp/gil-variance-probe-3310234/07-chess-r{1..5}.jsonl` traces, replay them into a `chatEventBuffer` by reconstructing the synthetic events (iteration_start at each user turn, verify_run/verify_result around each verify tool_call), then call `Detector.Check`. Assert `PatternNoProgress` is present in the returned signals for at least 3 of 5 traces.

If it doesn't fire → either `NoProgressThreshold` needs adjustment for chat semantics, or our event reconstruction is incomplete. Diagnose before the integration sweep — saves an hour of pointless chess runs.

## Open questions / known limits

- **AltToolOrder Decision content**: the existing `AltToolOrderStrategy.Apply` returns a Decision whose Explanation is a tool-order hint string. We surface it verbatim as a `[system] stuck-recover (alt_tool_order): <text>` Part. Whether the LLM actually follows the hint depends on the model — not measured.
- **Adversary self-consult bias**: if user passes `--adversary-model qwen3.6-27b` (same model), the "adversarial" review is just a re-prompt with a different system message. Cheaper than cross-model but weaker. Cross-model (e.g., a Claude API key) is the better long-term play — gated by credential availability.
- **Ring buffer eviction during long autonomous runs**: 200 events ≈ 10 turns at chess's tool density. NoProgress requires `threshold=4` consecutive iters in-window; longer-lived patterns are not detectable. Bumping cap to 500 if needed is one-line change post-measurement.
- **No persistence of buffer**: see Non-goals.

## Implementation phases

See `docs/superpowers/plans/2026-05-19-a1b-detector-chat-wiring-plan.md` (created by writing-plans skill).

- P67a — proto + sdk + flag wiring (AdversaryModel through to chat)
- P67b — event ring buffer + synthetic emits
- P67c — Detector dispatcher + visible Part delivery
- P67d — per-turn dedup + per-session budget cap
- P67e — remove P39 ad-hoc detector (Detector covers it)
- P67f — negative validation replay + chess re-measurement + task-surface.md update

Each phase has TDD red→green tests in scope before code.

## Related memories / docs

- [[gil-adversary-seam]] — original gap memory; updated when this lands.
- [[gil-eval-findings-2026-05-18]] — Finding #6 (temperature lever) and the chess boundary.
- [[gil-autonomy-arc-2026-05-17]] — P34-P40 chain; P39 ad-hoc detector removed in P67e.
- [[feedback-check-production-wiring]] — drives Negative validation step.
- [[feedback-agent-drives-system-safeguards]] — detector always on, adversary opt-in.
- `docs/eval/task-surface.md` — running results table.
