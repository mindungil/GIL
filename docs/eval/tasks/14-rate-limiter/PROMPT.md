Implement a token-bucket rate limiter in Go in the current working directory.

API:
- `Limiter` type (concurrent-safe)
- `NewLimiter(ratePerSec int, burst int) *Limiter` — `ratePerSec` tokens added per second, up to `burst` capacity
- `(l *Limiter) Allow() bool` — try to consume one token; return true if granted, false if rate-limited

Semantics:
- Bucket starts full (`burst` tokens available).
- Tokens are added continuously over time (you can use lazy refill on Allow() call rather than a background timer).
- Allow() is non-blocking — returns immediately with the verdict.
- Must be safe under concurrent Allow() from many goroutines.

Include `go test ./...` covering at minimum:
1. Initial burst: with `New(10, 5)`, the first 5 Allow() return true, the 6th returns false (before any refill).
2. Refill over time: with `New(10, 5)`, drain all 5 tokens, sleep 200ms, expect 2 more tokens available (10 per sec × 0.2 sec).
3. Steady-state rate: with `New(10, 10)`, drain burst, then call Allow() in a tight loop for 1 second; the count of "true" results should be approximately 10 (within ±2). Use `time.After` to bound the loop.
4. **Concurrency**: with `New(50, 50)`, launch 200 goroutines each doing 10 Allow() calls. After all done, the total count of "true" results across all goroutines must be ≤ 50 (initial burst). Run with `-race`. Naive `time.Now()`-based check + non-atomic counter will produce overruns OR data race.
5. Burst > rate ratio: with `New(1, 100)`, allow 100 immediate, then verify the 101st is denied (refill not yet triggered in same microsecond).

Run tests with `go test -race ./...`.

Initialize go.mod yourself. Done when `go test -race ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. If your first implementation uses `time.Now()` + non-atomic comparisons, test 4 will catch it under -race or the count will overrun — you must redesign.
