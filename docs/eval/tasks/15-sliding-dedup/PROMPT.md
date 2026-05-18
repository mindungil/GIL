Implement a sliding-window string deduplicator in Go in the current working directory.

API:
- `Dedup` type (concurrent-safe)
- `NewDedup(window time.Duration) *Dedup`
- `(d *Dedup) Observe(s string) bool` — record `s` as observed at the current time. Returns true if this is the FIRST observation in the past `window`, false if `s` was already observed within the last `window` duration.
- `(d *Dedup) Size() int` — current number of strings being tracked (those whose last observation is within `window` of now)

Semantics:
- "Within the last window" means `now - last_observed_at < window`.
- After a string falls outside the window, it should be **physically evicted** from internal state (not just ignored on read).
- Concurrent Observe calls must be safe.

Include `go test ./...` covering at minimum:
1. Basic dedup: Observe("a") returns true; immediate Observe("a") returns false.
2. Window expiry: Observe("a") returns true; sleep > window; Observe("a") returns true again.
3. Distinct keys are independent.
4. **Memory bound (the critical test)**: Use `New(50ms)`. Send 10000 unique strings, each separated by 1ms. Then sleep 200ms. Assert `Size() == 0` (all evicted). Then send 10000 more unique strings rapid-fire. Assert `Size() <= 12000` mid-stream (some eviction happened, NOT unbounded growth).
5. Concurrent observe-and-size: 8 goroutines each observing 1000 strings; final Size() reflects only items within window. Run with `-race`.

Initialize go.mod yourself. Run with `go test -race ./...`.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. Watch test 4 — a design that only checks timestamps on read (no eviction) will leak memory and fail it. You will need an explicit eviction mechanism (heap, sorted list, periodic sweep, etc.) — pick one.
