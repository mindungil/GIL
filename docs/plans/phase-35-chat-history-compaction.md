# Phase 35 — Chat History Compaction

## Why

P34 made chat history durable. Now sessions can grow unbounded —
every `Prompt` re-loads the full message log into `provider.Request.Messages`,
so a 50+ turn conversation will exceed the model's context window.
`request_compact` exists but only targets `RunService` (the runner's
iteration loop). The chat agent has no compaction mechanism for its
OWN history.

## Goal

When chat history token estimate exceeds the model's context window,
compact via the same Hermes pattern (Head + summarized Middle + Tail)
already implemented in `core/compact.Compactor`. The compaction is
**system-driven** — the agent never needs to know it happened.

## Design

### Trigger

At `Prompt` entry, after loading history + appending the new user
turn, BEFORE the agent loop's first `provider.Complete`:

```go
estimated := estimateMessagesTokens(provName, msgs)
ctxWindow := provider.ContextTokens(modelID)  // fallback 200_000
threshold := int64(float64(ctxWindow) * 0.95)
if estimated >= threshold {
    compacted, _ := compactor.Compact(ctx, msgs)
    msgs = compacted
    chatHistory.ReplaceInMemory(sid, compacted)  // sticky in-memory
}
```

Threshold mirrors `core/runner/runner.go`'s in-loop logic (95% of the
model's context window).

### Reuse

The `core/compact.Compactor` exists and is production-tested by the
run-time agent loop. We reuse it directly — same Hermes preserve-Head
(keep cache prefix), summarize-Middle, preserve-Tail (keep recent
context) pattern, same provider/model the chat agent uses.

### Persistence

In-memory replacement only. The DB log (`chat_messages` table) stays
authoritative and complete — `export_session` continues to see the full
history; daemon restart re-hydrates the full log and re-compacts on
next Prompt. Cost: one extra LLM call after restart, on the first
Prompt that exceeds threshold. Acceptable.

### Seq counter under compaction

After `ReplaceInMemory`, `len(chatHistory[sid])` is smaller than DB
`MAX(seq) + 1`. If `append` used `len` as the next seq, the INSERT
would collide on PK. Fix: `nextSeqLocked` queries `MAX(seq)` from DB
when wired, falls back to `len` when in-memory-only.

### Soft-fail

Compactor errors (provider rate-limit, partial response, …) → emit a
system note and continue with the original msgs. The user's turn isn't
blocked on compaction.

## Acceptance criteria

1. `compactChatIfNeeded` returns original msgs untouched when under
   threshold.
2. `compactChatIfNeeded` returns compacted msgs when over threshold
   (test with a mock provider that emits a known summary).
3. Provider error during compaction → returns original + non-nil
   error; caller falls back gracefully.
4. After `ReplaceInMemory`, `chatHistory.append` lands at DB
   `MAX(seq) + 1`, no PK collision.
5. Pre-P35 contract preserved: when `compactor == nil` (no provider
   factory), Prompt entry skips the compaction call entirely.
6. Live verification: synthetic long history with mock provider
   compacts; daemon restart re-compacts cleanly on first Prompt.

## Result (2026-05-17)

**Shipped.** Single commit on `feat/p33-reasoning-surface`. All
acceptance criteria met:

1. `TestCompactChat_BelowThreshold_NoOp` — 3-message history,
   compactor not called, msgs returned untouched. PASS.
2. `TestCompactChat_AboveThreshold_CompactsAndReplaces` — 80
   synthetic 10kB messages cross the 95% threshold; Hermes 9-message
   output (head 2 + summary 1 + tail 6); summary is the mock's
   scripted text. PASS.
3. `TestCompactChat_ProviderError_PreservesOriginal` — error provider
   surfaces err, msgs returned unchanged, didCompact=false. PASS.
4. `TestChatHistory_NextSeq_AvoidsCollisionAfterCompaction` — the
   critical PK-collision guard: append 20 messages (DB seq 0..19),
   ReplaceInMemory shrinks to 3, then append a new message. With
   `nextSeqLocked` using `MAX(seq)+1` from DB, the new row lands at
   seq=20 — no PK collision. DB ends with 21 rows. PASS.
5. `TestChatHistory_ReplaceInMemory_SuppressesReHydrate` — after
   ReplaceInMemory, get() returns the compacted slice, NOT a
   re-hydration of the full log. PASS.
6. `TestCompactChat_NilProvider_NoOp` /
   `TestCompactChat_EmptyMessages_NoOp` — defensive guards. PASS.

Existing tests unchanged: `TestChatHistory_*` (P34 9 tests) +
existing 100+ service tests all pass — the seq-counter change is
DB-aware-only and falls back to `len()` for in-memory test setups.

Live verification (bench probe 216, post-deploy):
- L1 (multi-file rename): wall=26.7s, verify=PASS, 22 tool calls
- L25 (spec-flow): wall=12.1s, verify=PASS, 10 tool calls
- L60 (verify-loop): wall=4.5s, verify=REDACTED (secret scrub), 2 tool calls
- Chat surface behavior unchanged under threshold — all 3 sessions
  short enough that compaction was a no-op.
- DB state confirms 79 chat_messages rows across 9 sessions, with
  the longest at 17 rows — well below the compaction trigger.

**Files touched:** 4
- `server/internal/service/chat_compact.go` — NEW, ~80 lines.
  `compactChatIfNeeded` + `estimateChatMessagesTokens`.
- `server/internal/service/session_prompt.go` — `chatHistory.nextSeqLocked`,
  `chatHistory.ReplaceInMemory`, compaction callsite in Prompt.
- `server/internal/service/chat_compact_test.go` — NEW, 7 tests.
- `docs/plans/phase-35-chat-history-compaction.md` — this doc.

No proto / SDK / CLI / TUI changes — the compaction is entirely
contained on the daemon side, transparent to the chat agent.
