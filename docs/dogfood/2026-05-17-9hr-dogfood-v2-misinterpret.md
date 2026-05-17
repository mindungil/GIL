# 2026-05-17 — 9hr dogfood v2: qwen misinterpreted the prompt

Second attempt at the long-run dogfood. Daemon-untouched policy
enforced (P53 policy). **Outcome: model misinterpretation, no
deliverable.** Another honest data point.

## What happened

Started fresh at 23:59:23 with the same prompt as v1 (build a
Markdown-to-HTML converter, 4 packages, corpus tests, README).

By turn 3 the trace showed the agent thinking the user (heredoc
input) was feeding **one AST node type at a time**:

> [think] The user is continuing to send AST node types one at a
> time. Got HorizontalRule — it has no fields, just a simple node
> type. Let me confirm and wait for the rest.

The agent kept printing:
> Still need: nodes for `HORIZONTAL_RULE`, `TABLE_ROW`, and all the
> inline node types. Sending more when ready.

Then waited for more input that never came (heredoc closed).

After ~5 min of this, I cancelled (the agent's deliverable was just
go.mod — no source code written).

## The misinterpretation pattern

The v1/v2 prompt was structured as a numbered list:
```
Implement these packages (under the module root):
1. `lexer/` — tokenizer producing a stream of tokens. Token types: ...
2. `parser/` — consumes tokens, builds AST. Node types: ...
3. `renderer/` — walks the AST, produces HTML string output...
4. `cli/` — `main` function...
```

qwen3.6-27b read each numbered item as a **separate message in a
conversation**. After processing item 1, it asked "OK, what's next?"
But heredoc input has no "next" — the whole prompt was one shot.

The model's chat-mode instinct (in qwen's training) prioritized
**interactive collection** over **batch execution** even when the
spec was clearly batched.

## Fixes for v3

Rewrote the prompt with explicit:
- "EXECUTE THIS COMPLETE TASK without asking me anything"
- "Everything you need to know is in this single message"
- "REMEMBER: I am not interactive. This single message contains the
  entire spec. DO NOT ask for clarifications. Just execute."

v3 started 00:06:28. Result pending.

## What this validates about the harness

The harness behaved correctly:
- gild stayed up the whole time (P36-P38 working)
- Chat persistence (P34) held the conversation
- The agent's tool calls (only think + reasoning, no spawn/write
  in v2) all executed
- The chat REPL didn't crash, didn't hang on stuck detection (the
  agent's thinking-only turns didn't trigger P39 since no tool
  calls repeated)

What the harness DIDN'T do (and shouldn't):
- Override the model's interpretation
- Force-execute a partial spec

That's correct: the harness provides scaffolding, not direction.
The model's interpretation is its own.

## What this DOES validate about real-LLM behavior

**qwen3.6-27b has weak batch-vs-interactive instruction following.**
For autonomous use, prompts MUST be explicitly framed as
"non-interactive, single shot, execute now." Without that framing,
the model may interpret structured prompts as conversation
openings.

This is a real-world dogfood finding worth recording for future
autonomy work. Possible system-side mitigations:
- Auto-inject a "this is a single-shot execution context" line in
  the chat agent's system prompt when stdin is NOT a TTY
- Detect "are you done?"-style endings and inject a "yes, execute
  the spec now" synthetic continuation
- Use a different model

Documenting as a finding, not a phase. Worth revisiting when we
move to more capable models or richer prompting infrastructure.
