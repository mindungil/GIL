# Phase 39 — Chat-side stuck detection

## Why

The run-time agent loop (`core/runner`) already has rich stuck
detection + recovery (SwitchModel, AltToolOrder, AdversaryConsult,
SubagentBranch, ResetSection — see `core/stuck/recovery.go`). The
chat-surface agent loop (`SessionService.Prompt`) has NONE.

When a chat agent loops on the same tool with the same input — e.g.,
keeps calling `read_file path=main.go` over and over because some
parsing went wrong — the user sees the ⚒ icons stream by but has no
clear "agent is stuck, refine your prompt" signal. The `maxAgentTurns`
cap (8) eventually terminates the turn, but until then it's a silent
waste of tokens.

For the autonomous coding harness goal, this matters because:
- Silent unproductive loops erode the user's trust that the chat
  agent is actually making progress.
- A visible stuck signal lets the user decide quickly (refine prompt
  vs. just wait for cap vs. stop the run).

## Goal

When a chat agent calls the same (tool, input) three times in a row
within one Prompt turn, surface a `stuck_detected` signal both as:
1. An event on the per-session stream (audit / TUI Tail mirroring).
2. A visible system Part text on the gRPC stream so the chat user
   sees it inline without needing a separate observer.

Single signal per turn — productive calls after the streak don't
re-fire.

## Non-goals

- Stuck recovery in chat. The chat agent doesn't have a frozen spec
  or a model chain, so the run-time strategies don't apply. v1 is
  observability only.
- Detection beyond consecutive identical calls. The full
  `core/stuck.Detector` covers monologue, ping-pong, no-progress,
  context-window-error. Those need event streams + iter counters
  the chat loop doesn't track today.
- Breaking the loop on stuck. `maxAgentTurns=8` already caps; the
  P39 signal is informational.

## Design

### Helpers (testable)

```go
func chatStuckSig(name string, input []byte) string  // sha256 truncated
func chatStuckCheck(sigs []string, window int) bool  // trailing all-same
```

Pure functions with their own unit tests so the behavior is pinned
even when the integration site refactors.

### Integration in Prompt

Inside the chat agent loop, per tool call:
```go
if !chatStuckFired {
    chatCallSigs = append(chatCallSigs, chatStuckSig(call.Name, call.Input))
    if chatStuckCheck(chatCallSigs, chatStuckRepeats /* 3 */) {
        emitChatEvent("stuck_detected", ...)
        stream.Send(Part_Text{Content: "[system] stuck_detected: ..."})
        chatStuckFired = true  // single signal per turn
    }
}
```

State is per-Prompt (resets on next user message). No cross-turn
memory; if the agent keeps looping across turns, each turn re-checks
its own 3-call window.

## Acceptance criteria

1. 3 identical (name + input) tool calls → 1 stuck warning Part on
   the stream.
2. 3 calls with different inputs (or different names) → NO warning.
3. >3 identical calls → still exactly 1 warning per turn.
4. The warning Part is `Part_Text` so chat clients render it inline.
5. `chatStuckSig` is deterministic and discriminates name and input.
6. `chatStuckCheck` handles empty / undersized / negative window
   inputs gracefully.

## Result (2026-05-17)

**Shipped.** 6 unit tests (2 pure-function + 4 end-to-end through
Prompt) all PASS.

The integration uses the existing `emitChatEvent` for the event
(no new event type added) plus a `Part_Text` with `[system]
stuck_detected: ...` prefix matching the chat surface's existing
system-note style.

**Files touched:** 3
- `server/internal/service/session_prompt.go` — chatStuckSig +
  chatStuckCheck helpers, in-loop tracking + emit.
- `server/internal/service/chat_stuck_test.go` (new) — 6 tests.
- `docs/plans/phase-39-chat-stuck-detection.md` — this doc.
