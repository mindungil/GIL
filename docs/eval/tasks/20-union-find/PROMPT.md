Implement Union-Find (Disjoint Set Union) with **path compression AND union by rank** in Go in the current working directory.

API:
- `UnionFind` type
- `New(n int) *UnionFind` — initialize with n disjoint sets (elements 0..n-1)
- `(u *UnionFind) Find(x int) int` — return representative of x's set; must apply path compression
- `(u *UnionFind) Union(x, y int) bool` — merge x's set and y's set; must use union by rank. Returns true if a merge happened (they were in different sets), false if already in same set.
- `(u *UnionFind) Count() int` — number of disjoint sets currently
- `(u *UnionFind) SameSet(x, y int) bool` — convenience

Include `go test ./...` covering at minimum:
1. Initial state: `New(5)` has Count()==5; SameSet(0,1) is false; Find(0) == 0.
2. Basic union: `Union(0,1); Union(2,3)` → Count()==3, SameSet(0,1) true, SameSet(0,2) false.
3. Transitive: `Union(0,1); Union(1,2)` → SameSet(0,2) true.
4. Duplicate union returns false.
5. **Performance test**: Initialize `New(100000)`. Run 200000 random Union(x,y) operations. Then run 100000 random Find(x) calls. Assert total elapsed time < 100ms. This pressures both path compression (Find should be near-O(1) amortized after compression) and union-by-rank (otherwise tree depth grows linearly).

Initialize go.mod yourself. Done when `go test ./...` passes including the 100ms perf bar.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.

Trap: a naive impl without path compression OR without union-by-rank will pass the small tests but time out on test 5. You need BOTH optimizations.
