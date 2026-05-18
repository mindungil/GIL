Implement a Sudoku solver in Go in the current working directory.

API:
- `Solve(puzzle [9][9]int) (solution [9][9]int, ok bool)` — solve in place; 0 represents empty cells. Returns `ok=false` if no solution exists.
- Algorithm: backtracking with constraint propagation. Plain DFS without smart cell selection will time out on hard puzzles.

Include `go test ./...` covering at minimum 5 named puzzles (you choose) of increasing difficulty:

1. **Easy** (lots of given cells): solved in <10ms
2. **Medium**: solved in <50ms
3. **Hard** (Norvig-style "hardest"): solved in <100ms
4. **Validation**: input that has no solution → returns ok=false (e.g. two same digits in same row)
5. **Already solved**: solved board returned unchanged with ok=true

For the hard puzzle, use the Norvig "world's hardest" or similar:
```
8 . . . . . . . .
. . 3 6 . . . . .
. 7 . . 9 . 2 . .
. 5 . . . 7 . . .
. . . . 4 5 7 . .
. . . 1 . . . 3 .
. . 1 . . . . 6 8
. . 8 5 . . . 1 .
. 9 . . . . 4 . .
```
Solution exists and is unique.

**Timing requirement**: the hard puzzle test must complete in <100ms. Plain DFS without picking the most-constrained cell first will time out on hard puzzles — use either:
- Most-constrained-variable heuristic (pick cell with fewest candidates first)
- AC-3 / arc consistency
- Or any equivalent constraint propagation

Initialize go.mod yourself. Done when `go test ./...` passes including the timing test.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
