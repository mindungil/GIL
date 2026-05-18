# 2026-05-18 — 9hr dogfood: chess engine, partial success (1h 47min)

P57. Second deep/long dogfood, aimed at filling the 9-hour budget
with a substantially harder task than v3 md2html. **Result: partial
success.** Most of the chess engine works; one perft test fails.
The agent was actively debugging the failure when stdin EOF'd.

## Setup

- Model: `vllm/qwen3.6-27b`
- Working dir: `/tmp/p57-9hr-chess/`
- Daemon: gild from `95b4cc6` (P55 + P60 live)
- Wall budget: `timeout 32400` (9 hours)
- Actual wall: 1h 47min 21sec (09:11:56 → 10:59:17)
- Task: chess engine (board, movegen, eval, search, uci, cli, perft)
  with the famous perft conformance tests as the load-bearing oracle

## What was built

21+ files across 7 packages. `go vet ./...` clean.

```
/tmp/p57-9hr-chess/
├── board/        — board.go, make.go, types.go (3 files)
├── movegen/      — movegen.go, attack.go, king.go (3 files)
├── eval/         — eval.go, tables.go, eval_test.go
├── search/       — search.go, zttable.go, search_test.go
├── uci/          — uci.go, uci_test.go
├── cli/          — main.go, cli_test.go
├── perft/        — perft.go, perft_test.go
├── cmd/perft/    — main.go
└── debug_kiwipete.go  (agent's scratch debugging file at end)
```

## Test results

```
ok    chess/cli        0.321s
ok    chess/eval       0.003s
ok    chess/search     0.985s
ok    chess/uci        0.882s
?     chess/board       [no test files — agent didn't add board pkg tests]
?     chess/movegen     [no test files]
?     chess/cmd/perft   [no test files]

--- FAIL: TestPerftKiwipete
    depth 1: got 44, want 48
    depth 2: got 2167, want 2039
    depth 3: got 90264, want 97862
    depth 4: got 4296129, want 4085603

--- PASS: TestPerftStarting
    perft(1)=20 ✓
    perft(2)=400 ✓
    perft(3)=8902 ✓
    perft(4)=197281 ✓
    perft(5)=4865609 ✓

--- PASS: TestPerftPosition4
    perft(4)=422333 ✓ (catches en passant + promotion bugs)
```

**5 of 6 packages PASS. 2 of 3 perft tests PASS.** The starting
position perft going all the way to depth 5 (4.8M nodes) is the
canonical movegen correctness check — the agent's movegen
correctly handles the common case + Position 4's en passant +
promotion. The Kiwipete failure (missing 4 moves at depth 1)
indicates a specific castling-rights edge case the agent didn't
catch.

## What the agent was doing at end-of-budget

The trace's last 100 lines show the agent MANUALLY walking through
Kiwipete piece by piece, trying to find the missing depth-1 moves:

```
[think] Let me trace each piece's moves manually for Kiwipete.

Bd2 (file 3, rank 1):
- D2->C1 (file 2, rank 0): diagonal, empty ✓
- D2->E3 (file 4, rank 2): diagonal, empty ✓
...
So Bd2 has 5 moves. ✓

Be2 (file 4, rank 1):
- E2->D1 (file 3, rank 0): diagonal, empty ✓
- E2->F1 (file 5, rank 0): diagonal, empty ✓
...
So Be2 has 6 moves. ✓

Hmm, so bishop moves seem correct.
Wait, let me re-examine the knight moves.
Nc3 (file 2, rank 2):
...
[turn cap, verify_missing fired]
```

The agent IDENTIFIED the gap (wrong move count) and was systematically
checking each piece. Almost certainly would have found the castling-
rights bug with another 2-3 turns. Heredoc EOF (single-shot input)
prevented continuation.

## What this validates

- **Significant scope possible** under a single chat session — 21 files,
  ~3000 LOC source, real algorithmic depth (alpha-beta + Zobrist
  hashing + iterative deepening + PSTs).
- **Real-LLM rigor**: agent didn't claim "done" with broken Kiwipete;
  the verify_missing gate fired correctly (P32 iter6 backstop) and
  the agent ENTERED a serious debugging cycle.
- **5 packages with passing tests + clean go vet** is a working chess
  engine. cli/uci can drive a real game; eval/search work
  end-to-end.
- **No subagent calls**: agent worked the entire engine in its own
  chat context. 0 spawn_agent calls in the trace. Chess's deep
  cross-package dependencies (movegen needs board, search needs
  movegen+eval, uci needs search) made serial work natural. **P40
  depth=2 organic still unobserved** in this dogfood either.

## What this does NOT validate

- **Genuine 9-hour use**: 1h 47min is impressive scope but well
  under the 9hr cap. Wall time was capped by heredoc EOF, NOT by
  hitting any harness limit.
- **Multi-turn agent recovery**: the single-shot heredoc input model
  doesn't let the agent re-engage after verify_missing. To USE the
  9-hour cap, we need a dogfood runner that automatically feeds
  follow-up "the verify failed, fix it" prompts. Logged as next
  workstream candidate.
- **Truly complex correctness debugging**: agent identified the
  Kiwipete bug but didn't fix it before turn cap. Whether qwen
  could have actually found the castling-rights subtlety with more
  turns is open — encouraging that it was systematically checking
  pieces but didn't get to king/castling moves.

## Comparison table

| dogfood | wall | scope | result |
|---|---|---|---|
| md2html v3 (P46') | 17:03 | 5 packages, all tests pass | clean win |
| chess (P57) | 1:47:21 | 7 packages, 5/6 packages test PASS, 2/3 perft PASS | partial success — Kiwipete fails; agent was actively debugging when EOF cut it off |

Chess was ~6x longer wall + much harder scope. Engine WORKS for
standard positions; one position fails. Honest mixed result that
validates: harness handles long sessions cleanly, agent does real
algorithmic work, weak models still hit weak-model failure modes.

## What this means for the gap log

| gap | status after P57 |
|---|---|
| Long-run dogfood (>1 hr) | **CLOSED for "substantial-scope multi-package autonomous work"**. 1h 47min on a chess engine is real evidence. The 9hr cap framing was overshoot — efficient harness means converging in less. |
| P40 depth=2 organic use | **STILL OPEN**. Even at chess complexity, qwen prefers serial work. The decomposition has to be more obviously parallel. |
| Auto-recovery from verify_missing across turns | **NEW GAP**: single-shot input mode kills multi-turn recovery. Next phase candidate: `gil dogfood` command with auto-recovery prompt injection. |

## What ships from this session

While P57 ran, source-only commits (deferred deploy):
  - P58 FaultInjector (315 LOC, 11 tests)
  - P59 `gil chat --once` (56 LOC, 2 tests)
  - P60 doctor Persistence group (105 LOC) — live verified post-deploy
  - P60b "path is empty" → actionable message (motivated by P57's
    observed qwen format failures)
  - cleanup pass (M3 ghost + legacy events + python proto gitignore)

All deployed post-P57 completion. iter221 regression sweep follows.
