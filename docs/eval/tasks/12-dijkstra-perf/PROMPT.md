Implement single-source shortest path on a weighted directed graph in Go in the current working directory.

API:
- `Graph` type — your choice of representation, but must support adding edges with non-negative weights
- `New() *Graph`
- `(g *Graph) AddEdge(from, to int, weight int)` — add a directed edge
- `(g *Graph) ShortestPath(source, target int) (dist int, path []int, ok bool)` — return shortest distance + the path (sequence of node IDs source..target inclusive), ok=false if no path exists

**Performance is part of the spec.** Your implementation will be tested on three graph sizes:

1. **Small (N=10)**: hand-crafted graph, exact path correctness check
2. **Medium (N=100)**: random graph, correctness against a brute-force baseline
3. **Large (N=2000, dense)**: random graph with ~10000 edges. The test asserts the call returns within **500ms**. A naive O(V²) scan-the-unvisited-set Dijkstra will time out; you'll need a heap-based O((V+E) log V) implementation.

Include `go test ./...` with all 3 test cases (small / medium / large). The large test must use `t.Deadline()` or a manual timer to fail if the call takes > 500ms.

Initialize go.mod yourself. Done when all three tests pass within 500ms each.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications. If your first implementation passes small + medium but fails the large performance bar, you must redesign — patching won't help.
