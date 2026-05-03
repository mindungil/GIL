# Phase 26.6 §2.6(b) — Intent Router Design

## 1. Problem

design.md §2.6 mandates a single natural-language surface — slashes are an
escape hatch, not the canonical input. Today's chat surface routes every
non-slash line as `InputPrompt` straight to the interview service. There
is no path from "show me the spec" to `c.Spec(ctx)`, no path from "switch
to the dark-mode session" to `c.SwitchSession(ctx, id)`. Users who don't
memorize `/sessions /switch /new /spec /status /diff /merge /run` cannot
reach those actions in natural language.

P26 explicitly removed the prior regex intent classifier (commit T12,
"chat.go integration (replace intent classifier)"). The hole left
behind is what §2.6 codifies. This phase reintroduces an intent router,
this time LLM-based, with the deterministic slash table preserved as a
hidden fallback for scripts and accessibility.

## 2. Goal

A single small/fast LLM pass classifies each `InputPrompt` line into one
of three buckets, emits a system-visible routing note, and dispatches:

| Bucket | Meaning | Action |
|---|---|---|
| `verb` | maps cleanly to a known service call | dispatch handler with extracted args |
| `forward` | belongs to interview / run conversation | append to interview/run stream as before |
| `ambiguous` | could be either, or args are missing | ask one short clarifying question |

The router runs on every `InputPrompt`. `InputSlash` (lines starting
with `/`) skips the router entirely so power users and scripts have a
deterministic path.

## 3. Scope (V1)

- New `core/intent/` package — pure logic, callable from server.
- New gRPC `IntentService.Classify(prompt, sessionContext) → IntentClassification` RPC.
- New `spec.models.intent` ModelChoice slot — small fast model
  (Haiku-class). Falls back to `spec.models.weak` then `spec.models.main`
  when unset.
- The cli REPL loop calls the router for every `InputPrompt`, surfaces
  the routing decision as a `SystemNote`, then dispatches the
  appropriate service call OR forwards to the existing prompt path.
- Slash table stays in `slash.go` unchanged. `/help` becomes
  deemphasized (not advertised in the affordance line).
- A `--no-intent-router` flag bypasses the router and restores
  pre-Phase-26.6 behavior (debugging escape hatch).

Out of scope for V1:
- Multi-step intents ("first show sessions then switch to the second one").
- Per-user verb customization.
- Server-side caching (each call is a fresh classification).
- Confidence-based fallback to interview service (V1 trusts the
  classifier; if it returns `verb`, we dispatch).

## 4. Verb catalog

Same as the existing slash table. Each verb has a canonical name + a
short prose hint that the classifier sees in its system prompt.

| Canonical | Slash | Args | Prose hint (for classifier) |
|---|---|---|---|
| `sessions` | `/sessions` | none | list past sessions |
| `switch` | `/switch <id\|name>` | `target: string` | resume a different session by id, slug, or descriptive phrase |
| `new` | `/new` | none | start a fresh session |
| `spec` | `/spec` | none | show the frozen spec for the active session |
| `status` | `/status` | none | show progress / health of the active session |
| `diff` | `/diff` | none | preview the diff produced by the run |
| `merge` | `/merge` | none | apply the diff to the working tree |
| `run` | `/run` | none | start the run (only valid when phase = AwaitingConfirm) |
| `quit` | `/quit` | none | exit the chat surface |
| `help` | `/help` | none | show what's possible (deemphasized) |

`switch` is the only verb with structured args. The classifier extracts
`target` from natural-language phrasings ("the dark-mode one", "session
2", "01kqep…").

## 5. Classification contract

### Input
```json
{
  "prompt": "show me the diff",
  "session_phase": "done",        // current phase (idle/interview/awaiting_confirm/run/done/stuck)
  "session_id":   "01KQEP…",      // active session if any, else ""
  "recent_sessions": [            // top-N from pre-first-turn list
    {"id": "01KQEP…", "slug": "add dark mode", "status": "done"},
    {"id": "01KQAB…", "slug": "fix oauth",     "status": "done"}
  ]
}
```

### Output
```json
{
  "kind": "verb",                 // verb | forward | ambiguous
  "verb": "diff",                 // populated when kind == verb
  "args": {},                     // verb-specific; {"target": "01KQEP…"} for switch
  "rationale": "diff-preview verb requested explicitly",  // shown in system note
  "clarification": ""             // populated when kind == ambiguous
}
```

The classifier is given the verb catalog, the canonical-name format,
and concrete examples for each verb. Single LLM call, JSON mode (or
function-call style if the provider supports it).

### Failure modes
- Network/timeout → fall through to `forward` silently. The interview
  service receives the prompt and the user gets at least the
  conversation behavior.
- Invalid JSON / unknown verb → log, fall through to `forward`.
- `kind == verb` but the verb requires a session and there is none →
  emit a system note ("session needed for `diff` — start a new one or
  resume one") and don't dispatch. Don't forward either; the user
  asked for a verb, not a chat reply.

## 6. Module placement

| Path | Responsibility |
|---|---|
| `core/intent/router.go` | pure classification logic — given a prompt and context, builds the LLM call, parses the result, returns `Classification`. No I/O beyond the LLM call. |
| `core/intent/catalog.go` | the verb table (canonical name, args schema, prose hint). Source of truth shared with `cli/internal/chat/repl/slash.go` so adding a verb is a single place. |
| `core/intent/router_test.go` | table-driven tests against a stub LLM that returns canned JSON. Covers each verb's happy path, ambiguous case, malformed-JSON fallback, missing-session guard. |
| `server/internal/service/intent.go` | gRPC `IntentService.Classify` — thin adapter that loads the active session context and invokes `core/intent.Router`. |
| `proto/gil/v1/intent.proto` | new RPC definition. |
| `cli/internal/chat/repl/loop.go` | calls `IntentService.Classify` on every `InputPrompt`, surfaces the rationale as a `SystemNote(NoteSystem, …)`, then either dispatches the verb (re-uses the existing slash dispatch in `runSlash`) or forwards the prompt. |
| `proto/gil/v1/spec.proto` | add `ModelChoice intent = 9;` to `ModelConfig`. |
| `core/workspace/apply.go` | propagate provider to the new `intent` slot (already covered by the propagate-to-all-slots fix from #214). |

The cli REPL is the only consumer in V1. The TUI chat surface (`tui/`)
will adopt the same flow once it has a stream pump for system notes
(after T8 — already landed).

## 7. UX — the system note

Before dispatching the verb, the renderer emits one short line:

```
›  show me the diff
   → diff
```

The arrow + verb name is the entire note. No "I think you mean…" prose,
no confidence percentage. Compact. The user reads it and either
proceeds (the dispatch happens immediately after) or types
`/switch <id>` (or another slash) to override.

When `kind == ambiguous`:
```
›  switch to the dark one
   ?  which one — "add dark mode" (01kqep) or "dark theme refactor" (01kqax)?
```

When `kind == forward` (the common case during interview):
```
›  yes, use postgres
   (no system note — the prompt flows to the interview as before)
```

Quiet by default, visible when something happens.

## 8. Latency budget

The router fires before every prompt dispatch. Budget targets:
- p50 < 400ms (Haiku-class, no streaming, ~200 input tokens).
- p99 < 1.5s.
- Hard timeout: 3s. On timeout, fall through to `forward`.

The render loop must NOT block the prompt panel during the router call.
Show a brief in-flight indicator (`›  show me the diff` with a faint
spinner glyph after it) and replace it with the routing arrow once the
classification returns. If the user submits another prompt while a
classification is in flight, cancel the previous one (parallel to the
stream-cancel pattern in T8).

## 9. Slash table — what changes

- `IsKnownSlash` and the dispatch table stay exactly as they are.
- `/help` text changes to: `"slash commands are an escape hatch — try natural language first. Available: /sessions /switch /new /spec /status /diff /merge /run /quit /help"`.
- The affordance line stops listing `/  / cmds` as a primary hint. Replace with `"› ask, or /help for slash"`.
- Adding a new verb requires adding it to `core/intent/catalog.go` (the source of truth) AND `slash.go` (which now derives its set from the catalog).

## 10. Testing

| Layer | What |
|---|---|
| `core/intent/router_test.go` | unit tests with a stubbed LLM client returning canned JSON. Each verb has at least 3 paraphrase examples. Covers ambiguous, malformed-JSON, timeout. |
| `cli/internal/chat/repl/loop_test.go` | extend with: prompt → router (mocked) → SystemNote emitted → verb dispatched. Verifies the note format, the dispatch wiring, and the "missing session" guard path. |
| `server/internal/service/intent_test.go` | tests the gRPC adapter against an in-process router. |
| dogfood | manual: type "show me sessions" / "what's the spec" / "save it" / nonsense ("foo bar"). Each should produce the right routing note + correct dispatch (or sensible forward). |

## 11. Acceptance

A user who has never read `/help` can:
1. Open `gil`, see the pre-first-turn session list (already shipped).
2. Type "switch to the oauth one" — router resolves to `switch` with `target=01kqab…`, system note shows `→ switch oauth`, session activates.
3. Type "show me the diff" — router resolves to `diff`, diff appears.
4. Type "merge it" — router resolves to `merge`, confirmation dialog appears.
5. Type "actually let me see the spec first" — router resolves to `spec`.
6. Type "ok run it" while phase != AwaitingConfirm — router resolves to `run` but the session-state guard fires; system note explains why.

Slash users keep working unchanged. Scripts that pipe `/sessions\n` into stdin keep working unchanged.

## 12. Implementation order

This design corresponds to task #235 (the implementation). Suggested
task decomposition (one PR per row):

1. `core/intent/catalog.go` + verb table + `Classification` types — pure data, no LLM call.
2. `core/intent/router.go` — `Router.Classify(ctx, prompt, sessionCtx)` with a `LLMClient` interface; unit tests with a stub.
3. `proto/gil/v1/intent.proto` + `spec.proto` `ModelChoice intent = 9;` regen.
4. `server/internal/service/intent.go` — gRPC adapter; integration test with router stub.
5. `cli/internal/chat/repl/loop.go` — wire the router into the prompt path; emit system note; dispatch verb or forward; guard missing-session.
6. Affordance line + `/help` copy update (cli + tui).
7. `--no-intent-router` flag wiring.
8. Manual dogfood: 5-prompt acceptance script per §11.

Each step is independently shippable (the router landing without REPL
wiring is dead code, but the cli wiring landing without the router
breaks the build — so order matters).
