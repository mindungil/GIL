Implement a thread-safe in-memory key-value store with **atomic batch operations** in Go in the current working directory.

API:
- `Store` type (concurrent-safe)
- `New() *Store`
- `(s *Store) Get(key string) (value string, ok bool)`
- `(s *Store) Set(key, value string) error`
- `(s *Store) Delete(key string) bool`
- `(s *Store) Batch(ops []Op) error` — apply a sequence of operations **atomically**: either all succeed, or none are visible

The `Op` type carries either a Set or Delete request:
```go
type Op struct {
    Kind  string // "set" or "delete"
    Key   string
    Value string // only used for "set"
}
```

Validation rules (apply per-op during `Batch` pre-flight):
- `Kind` must be "set" or "delete" (any other → error, no ops applied)
- `Key` must be non-empty and ≤ 256 chars (otherwise → error, no ops applied)

**Atomicity requirements:**
1. If ANY op in the batch fails validation, NO ops in the batch are applied.
2. Concurrent `Get` calls during an in-progress `Batch` must see a consistent snapshot — either fully pre-batch or fully post-batch values, never a half-applied state.

This means a naive "iterate and apply" implementation is wrong on requirement 2. You'll need either a copy-on-write snapshot, a staging map merged at commit, or equivalent. A single sync.Mutex around the existing map is NOT sufficient because Get-during-Batch can interleave between op applications.

Include `go test ./...` covering at minimum:
1. Basic Set + Get
2. Delete
3. Batch with all-valid ops → all visible after
4. Batch with one invalid op (empty key) → none of the other ops applied (rollback)
5. **Concurrent read during batch**: launch a goroutine doing `Get` on a key in a tight loop; from main, run `Batch` that modifies that key plus several others; verify the goroutine never observed an intermediate state (use channels to coordinate; the goroutine reports each observed value and the test asserts only old-value OR new-value were seen, never anything else). Use `-race` clean.

Run tests with `go test -race ./...` in your verify cycle.

Initialize go.mod yourself. Done when `go test -race ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. If your first design uses a single mutex around per-op application, the concurrent test will catch it — you must redesign.
