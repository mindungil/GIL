Implement an LRU (least-recently-used) cache in Go in the current working directory.

API:
- `New(capacity int) *Cache` — create with fixed capacity (capacity > 0)
- `(c *Cache) Get(key string) (value string, ok bool)` — fetch; recently-used; promotes key to most-recent on hit
- `(c *Cache) Put(key, value string)` — insert or update; evicts least-recently-used if at capacity
- `(c *Cache) Len() int` — current number of entries

Performance: Get and Put must be O(1) amortized. Use a doubly-linked list + map.

Include `go test ./...` covering at least:
1. Get on empty returns ok=false
2. Put + Get returns the value
3. Capacity eviction: filling beyond capacity drops the least-recently-used
4. Get promotes to most-recent (so the next eviction picks a different key)
5. Put on an existing key updates value AND promotes

Initialize go.mod yourself. Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
