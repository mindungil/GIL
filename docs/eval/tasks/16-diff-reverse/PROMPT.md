Implement a versioned-config diff/apply/reverse library in Go in the current working directory.

Types:
- `Config = map[string]string` (the data model)
- `Op` type carrying one of three kinds: `add` (new key, with value), `remove` (existing key, with old value preserved), `modify` (key whose value is changing, both old and new values preserved). Concretely:
  ```go
  type Op struct {
      Kind     string // "add" | "remove" | "modify"
      Key      string
      OldValue string // empty for "add"
      NewValue string // empty for "remove"
  }
  type Patch []Op
  ```

API:
- `Apply(c Config, p Patch) (Config, error)` — returns a new Config; never mutates input. Errors:
  - `add` on existing key
  - `remove` or `modify` on missing key
  - `modify`/`remove` where `OldValue` doesn't match current value
- `Diff(from, to Config) Patch` — compute the minimal patch transforming `from` into `to`. Use `modify` (not remove+add) for keys present in both with different values. Order of ops in the patch is deterministic (sort by Key).
- `Reverse(p Patch) Patch` — produce a patch that undoes `p`: swap add↔remove, swap OldValue↔NewValue for modify, reverse op order.

**Properties to verify in tests** (each is a separate `go test` case, table-driven or explicit):

1. `Apply(c, Diff(c, c)) == c` (empty patch round-trips)
2. `Apply(a, Diff(a, b)) == b` — for arbitrary `a`, `b`
3. `Apply(b, Reverse(Diff(a, b))) == a` — reverse undoes the diff
4. `Reverse(Reverse(p)) == p` — double reverse is identity
5. `Apply` errors on `add` to existing key
6. `Apply` errors on `modify`/`remove` with stale `OldValue`
7. `Diff` uses `modify` (not `remove`+`add`) when key is present in both configs with different values

For property tests, use either explicit table-driven test cases (at least 5 different (a, b) pairs) or rapid-style property testing with 100+ random pairs — your call.

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. The trap: `Diff` must capture OldValue for `modify`/`remove` ops, or `Reverse` cannot reconstruct the original state. A naive design that only stores `Key + NewValue` per op cannot round-trip.
